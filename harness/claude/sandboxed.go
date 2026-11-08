package claude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/sandbox"
)

const (
	// patchTimeout bounds each of the harness's own commands in the sandbox.
	patchTimeout = time.Minute
	// tailLines is how much of the verify command's output is kept.
	tailLines = 60
	// Git's modes for a symbolic link and an executable file.
	linkMode       = "120000"
	executableMode = "100755"
)

// Answer is what a session that may write returns, as JSON. It reads as a
// [patch.Patch].
type Answer struct {
	// Answer is the agent's closing message: JSON if it was JSON, or else
	// the text as a JSON string, because the patch is the result and the
	// message only describes it.
	Answer json.RawMessage `json:"answer"`
	// Changes is what the agent changed in the checkout.
	Changes []patch.Change `json:"changes"`
	// Diff is the same changes, for people to read.
	Diff  string `json:"diff"`
	Usage Usage  `json:"usage"`
	// Verified is how Config.Verify ended, or null if there is none.
	Verified *Verified `json:"verified"`
}

// Verified is how the repository's own checks ended after the agent's work.
type Verified struct {
	ExitCode int `json:"exit_code"`
	// Tail is the end of the checks' output, where a failure usually shows.
	Tail string `json:"tail"`
}

// sandboxed runs a session in a new sandbox, which it always destroys.
func (h *Harness) sandboxed(ctx context.Context, prompt string, input json.RawMessage) (json.RawMessage, error) {
	id, err := h.sandbox.Create(ctx, h.spec)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSandbox, err)
	}
	answer, err := h.inSandbox(ctx, id, prompt, input)
	// Clean up even when ctx was cancelled.
	if destroyErr := h.sandbox.Destroy(context.WithoutCancel(ctx), id); destroyErr != nil {
		err = errors.Join(err, fmt.Errorf("%w: %w", ErrSandbox, destroyErr))
	}
	if err != nil {
		return nil, err
	}
	return answer, nil
}

func (h *Harness) inSandbox(ctx context.Context, id sandbox.ID, prompt string, input json.RawMessage) (json.RawMessage, error) {
	input, err := h.prepare(ctx, id, input)
	if err != nil {
		return nil, err
	}
	if err := baseline(ctx, h.sandbox, id); err != nil {
		return nil, err
	}
	tools, err := serveTools(h.sandbox, id, h.config.Timeout)
	if err != nil {
		return nil, err
	}
	text, usage, err := h.conversation(ctx, prompt, input, tools)
	_ = tools.Close()
	if err != nil {
		return nil, err
	}
	answer, err := parseAnswer(text)
	if err != nil {
		answer, _ = json.Marshal(text)
	}
	// Read first: checks can leave files behind, and those are not the
	// agent's change.
	changes, err := changes(ctx, h.sandbox, id)
	if err != nil {
		return nil, err
	}
	diff, err := captured(ctx, h.sandbox, id, "git diff --cached --binary")
	if err != nil {
		return nil, err
	}
	var verified *Verified
	if h.config.Verify != "" {
		out, err := h.sandbox.Exec(ctx, id, h.config.Verify, h.config.Timeout)
		if err != nil {
			return nil, fmt.Errorf("%w: running the checks: %w", ErrSandbox, err)
		}
		verified = &Verified{ExitCode: out.ExitCode, Tail: tail(out.Stdout + out.Stderr)}
	}
	return json.Marshal(Answer{Answer: answer, Changes: changes, Diff: diff, Usage: usage, Verified: verified})
}

// prepare puts a workspace or a checkout named in input into the sandbox, and
// returns input without them, so neither ever reaches the model.
func (h *Harness) prepare(ctx context.Context, id sandbox.ID, input json.RawMessage) (json.RawMessage, error) {
	fields, err := objectFields(input)
	if err != nil || fields == nil {
		return input, err
	}
	workspace, hasWorkspace := fields[WorkspaceField]
	wanted, hasCheckout := fields[CheckoutField]
	if !hasWorkspace && !hasCheckout {
		return input, nil
	}
	delete(fields, WorkspaceField)
	delete(fields, CheckoutField)
	if hasWorkspace {
		var encoded string
		if err := json.Unmarshal(workspace, &encoded); err != nil {
			return nil, fmt.Errorf("%w: %s must be a base64 tar archive as a JSON string", ErrRefused, WorkspaceField)
		}
		archive, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("%w: %s is not base64; encode the archive with base64.StdEncoding", ErrRefused, WorkspaceField)
		}
		if err := h.sandbox.Upload(ctx, id, archive); err != nil {
			return nil, fmt.Errorf("%w: uploading the workspace: %w", ErrSandbox, err)
		}
	}
	if hasCheckout {
		if err := h.checkOut(ctx, id, wanted); err != nil {
			return nil, err
		}
	}
	return json.Marshal(fields)
}

// checkOut unpacks a commit into the sandbox, after its dependencies if
// fetching is configured, so the source overwrites nothing the cache holds.
func (h *Harness) checkOut(ctx context.Context, id sandbox.ID, wanted json.RawMessage) error {
	if h.checkouts == nil {
		return fmt.Errorf("%w: the input asks for a checkout and none is configured; configure one with Harness.WithCheckouts", ErrRefused)
	}
	var checkout Checkout
	if err := json.Unmarshal(wanted, &checkout); err != nil {
		return fmt.Errorf("%w: %s must be a claude.Checkout naming a repository and a full commit hash", ErrRefused, CheckoutField)
	}
	tarball, err := h.checkouts.Tarball(ctx, checkout.Repo, checkout.Sha)
	if err != nil {
		return fmt.Errorf("%w: fetching the checkout: %w", ErrSandbox, err)
	}
	if h.fetch != nil {
		deps, err := sandbox.FetchDependencies(ctx, h.sandbox, *h.fetch, tarball)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrSandbox, err)
		}
		if err := sandbox.Install(ctx, h.sandbox, id, deps); err != nil {
			return fmt.Errorf("%w: installing the dependencies: %w", ErrSandbox, err)
		}
	}
	if err := sandbox.Unpack(ctx, h.sandbox, id, tarball); err != nil {
		return fmt.Errorf("%w: unpacking the checkout: %w", ErrSandbox, err)
	}
	return nil
}

// baseline commits the checkout as it is, so the patch holds only what the
// agent changed. Scratch space, the dependency cache, and build output are
// excluded, because a repository may not ignore them.
func baseline(ctx context.Context, sb sandbox.Sandbox, id sandbox.ID) error {
	const command = `git init -q . && mkdir -p .git/info && ` +
		`printf '.tmp/\n.cargo/\n.cache/\ntarget/\nnode_modules/\n__pycache__/\n' >> .git/info/exclude && git add -A && ` +
		`git -c user.name=almanac -c user.email=almanac@localhost commit -q --allow-empty -m baseline`
	_, err := captured(ctx, sb, id, command)
	return err
}

// captured runs one of the harness's own commands and returns its whole
// output. A command that failed, or printed more than the sandbox returns, is
// an error, because a patch read in part is a different patch.
func captured(ctx context.Context, sb sandbox.Sandbox, id sandbox.ID, command string) (string, error) {
	out, err := sb.Exec(ctx, id, command, patchTimeout)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSandbox, err)
	}
	if out.ExitCode != 0 || out.TimedOut {
		return "", fmt.Errorf("%w: %w", ErrSandbox, &sandbox.CommandFailed{Command: command, Output: out})
	}
	if out.Truncated {
		return "", fmt.Errorf("%w: `%s` printed more than the sandbox returns, so the patch cannot be read whole; "+
			"a patch this large needs a person", ErrSandbox, command)
	}
	return out.Stdout, nil
}

// changes is what the agent changed, as file changes. Everything read here is
// the agent's to shape, and a false report only changes what the patch policy
// sees and what gets pushed.
func changes(ctx context.Context, sb sandbox.Sandbox, id sandbox.ID) ([]patch.Change, error) {
	listing, err := captured(ctx, sb, id, "git add -A && git diff --cached --raw --no-renames --abbrev=40 -z")
	if err != nil {
		return nil, err
	}
	unreadable := fmt.Errorf("%w: git's list of changes is unreadable; the agent may have replaced git in the sandbox", ErrSandbox)
	fields := strings.Split(listing, "\x00")
	changes := []patch.Change{}
	for i := 0; i+1 < len(fields); i += 2 {
		meta, path := fields[i], fields[i+1]
		columns := strings.Split(strings.TrimPrefix(meta, ":"), " ")
		if len(columns) != 5 {
			return nil, unreadable
		}
		mode, blob, status := columns[1], columns[3], columns[4]
		switch {
		case status == "D":
			changes = append(changes, patch.Change{Op: patch.Delete, Path: path})
		case mode == linkMode:
			changes = append(changes, patch.Change{Op: patch.Link, Path: path})
		case isObjectName(blob):
			content, err := captured(ctx, sb, id, "git cat-file blob "+blob+" | base64 | tr -d '\\n'")
			if err != nil {
				return nil, err
			}
			changes = append(changes, patch.Change{Op: patch.Write, Path: path, Content: content, Executable: mode == executableMode})
		default:
			return nil, unreadable
		}
	}
	return changes, nil
}

func isObjectName(text string) bool {
	if len(text) != 40 {
		return false
	}
	for _, r := range text {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// tail is the end of a command's output, where a failure usually shows.
func tail(output string) string {
	if output == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-tailLines):], "\n")
}
