package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// maxEventBytes bounds one line of the CLI's output, which repeats what the
	// model wrote.
	maxEventBytes = 64 << 20
	// waitDelay is how long the CLI may hold its pipes open after it is
	// killed.
	waitDelay = 5 * time.Second
)

// args is the command line ADR 1 records. `--tools ""` removes every built-in
// tool from the request, so a call to one does nothing; `--strict-mcp-config`
// ignores every MCP server but the sandbox's; the isolation flags skip the
// operator's settings, memory and hooks; `--no-session-persistence` leaves no
// transcript behind.
func (c Config) args(prompt string, tools *toolServer) []string {
	args := []string{"-p", "--tools", "", "--strict-mcp-config"}
	args = append(args, c.Credential.isolation()...)
	args = append(args,
		"--no-session-persistence", "--verbose",
		"--output-format", "stream-json",
		"--max-turns", strconv.Itoa(c.MaxTurns),
		"--system-prompt", prompt)
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if tools != nil {
		args = append(args, "--mcp-config", tools.config, "--allowedTools")
		args = append(args, allowedTools()...)
	}
	return append(args, "--max-budget-usd", formatUSD(c.MaxBudgetUSD))
}

// environ is a session's whole environment, built from nothing, so no
// variable of the operator's reaches a process that parses hostile text.
func (c Config) environ(home string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	env = append(env, c.Credential.environ()...)
	if c.BaseURL != "" {
		env = append(env, "ANTHROPIC_BASE_URL="+c.BaseURL)
	}
	return env
}

// Usage is what a session used.
type Usage struct {
	Tokens  uint64   `json:"tokens"`
	CostUSD *float64 `json:"cost_usd"`
	Turns   *int     `json:"turns"`
}

// conversation runs the CLI once, in an empty home that is also its working
// directory, with input as the user turn. It returns the closing text.
func (h *Harness) conversation(ctx context.Context, prompt string, input json.RawMessage, tools *toolServer) (string, Usage, error) {
	home, err := os.MkdirTemp("", "almanac-claude-")
	if err != nil {
		return "", Usage{}, fmt.Errorf("%w: cannot make the session's home: %w", ErrFailed, err)
	}
	defer func() { _ = os.RemoveAll(home) }()

	limit := h.config.Timeout
	timeLimit := fmt.Errorf("%w: over its %s time limit; the agent may be looping, or the limit too short for the work", ErrKilled, limit)
	ctx, cancel := context.WithTimeoutCause(ctx, limit, timeLimit)
	defer cancel()

	cmd := exec.CommandContext(ctx, h.config.Binary, h.config.args(prompt, tools)...)
	cmd.Env = h.config.environ(home)
	cmd.Dir = home
	cmd.Stdin = bytes.NewReader(input)
	// Stderr is left nil, so it goes to the null device. The CLI runs in a process group of its own, so killing it kills
	// whatever it started too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = waitDelay
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", Usage{}, fmt.Errorf("%w: %w", ErrFailed, err)
	}
	if err := cmd.Start(); err != nil {
		return "", Usage{}, fmt.Errorf("%w: cannot run %s: %w; install the Claude Code CLI or name it in Config.Binary", ErrFailed, h.config.Binary, err)
	}

	type outcome struct {
		text  string
		usage Usage
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		text, usage, err := readResult(stdout, h.config.TokenBudget)
		done <- outcome{text, usage, err}
	}()
	var result outcome
	select {
	case result = <-done:
	case <-ctx.Done():
		result.err = context.Cause(ctx)
	}
	_ = killGroup(cmd)
	_ = cmd.Wait()
	return result.text, result.usage, result.err
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// event is the part of a stream-json line the harness reads.
type event struct {
	Type    string `json:"type"`
	Message struct {
		ID    string `json:"id"`
		Usage struct {
			Input         uint64 `json:"input_tokens"`
			Output        uint64 `json:"output_tokens"`
			CacheCreation uint64 `json:"cache_creation_input_tokens"`
			CacheRead     uint64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	IsError      bool     `json:"is_error"`
	Subtype      string   `json:"subtype"`
	Result       string   `json:"result"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
	NumTurns     *int     `json:"num_turns"`
}

// readResult reads the CLI's events until its result, counting tokens as they
// arrive and failing as soon as they pass budget.
func readResult(stdout io.Reader, budget uint64) (string, Usage, error) {
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64<<10), maxEventBytes)
	var used uint64
	counted := ""
	for lines.Scan() {
		var e event
		if json.Unmarshal(lines.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "assistant":
			// A message arrives as one event per content block, each
			// repeating its usage.
			if e.Message.ID == "" || e.Message.ID != counted {
				u := e.Message.Usage
				used += u.Input + u.Output + u.CacheCreation + u.CacheRead
			}
			counted = e.Message.ID
			if used > budget {
				return "", Usage{}, fmt.Errorf("%w: used %d tokens of a %d budget; the agent may be looping, or the budget too small for the work", ErrKilled, used, budget)
			}
		case "result":
			if e.IsError {
				return "", Usage{}, fmt.Errorf("%w: %s, costing %s USD; the CLI ended the session, and its reason says what to change", ErrFailed, reason(e.Subtype), cost(e.TotalCostUSD))
			}
			return e.Result, Usage{Tokens: used, CostUSD: e.TotalCostUSD, Turns: e.NumTurns}, nil
		}
	}
	if err := lines.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		return "", Usage{}, fmt.Errorf("%w: reading the CLI's output: %w", ErrFailed, err)
	}
	return "", Usage{}, fmt.Errorf("%w: the session ended without a result; the CLI exited early, so check that it runs and that its credential is valid", ErrFailed)
}

func reason(subtype string) string {
	if subtype == "" {
		return "unknown"
	}
	return subtype
}

func cost(usd *float64) string {
	if usd == nil {
		return "an unknown amount of"
	}
	return formatUSD(*usd)
}

// parseAnswer accepts JSON alone or inside a code fence. Anything else is a
// failure, not a guess. The text is the model's, so the error never quotes it.
func parseAnswer(text string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(text)
	inner := trimmed
	fenced, opened := strings.CutPrefix(trimmed, "```json")
	if !opened {
		fenced, opened = strings.CutPrefix(trimmed, "```")
	}
	if body, closed := strings.CutSuffix(fenced, "```"); opened && closed {
		inner = body
	}
	inner = strings.TrimSpace(inner)
	if !json.Valid([]byte(inner)) {
		return nil, fmt.Errorf("%w: the prompt must ask for a JSON answer, alone or in a code fence", ErrNotJSON)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(inner)); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotJSON, err)
	}
	return compact.Bytes(), nil
}
