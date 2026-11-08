// Package claude is an [agent.Harness] on the Claude Code CLI, built as
// docs/adrs/0001-agent-sandbox.md decides: the agent loop runs on the host with
// no tools of its own, and its only tools are sandbox calls served over MCP by
// this process.
//
// A session runs `claude -p` with every built-in tool removed, no settings,
// memory or session files, an empty working directory and home, and an
// environment built from nothing but PATH, HOME and the model credential. The
// prompt is the system prompt and the input is the user turn, so hostile text
// is never presented as instructions.
//
// A session that may not write ([agent.Quarantined]) has no tools at all and
// answers with JSON. A session that may write ([agent.Fixer]) works in a new
// sandbox, which is destroyed when it ends, and answers with what it changed
// there, in the shape of a [patch.Patch]. This harness refuses any session with
// egress or secrets, so it never runs an [agent.Worker].
//
// The stub Messages API in harness/claude/stub lets specs run the real CLI with
// no credential and no spend.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/sandbox"
)

// Defaults for [DefaultConfig].
const (
	// DefaultTokenBudget is how many tokens a session may use, cached ones
	// included, before it is killed.
	DefaultTokenBudget = 2_000_000
	// DefaultTimeout is how long a session may run before it is killed.
	DefaultTimeout = 30 * time.Minute
	// DefaultMaxTurns is how many turns the CLI allows a session.
	DefaultMaxTurns = 60
	// DefaultMaxBudgetUSD stops a runaway session. It is a ceiling, not a
	// target.
	DefaultMaxBudgetUSD = 6.0
)

// Config is how sessions run. Start from [DefaultConfig], which sets every
// limit.
type Config struct {
	// Binary is the CLI to run, found on PATH if it has no slash.
	Binary string
	// Credential is how a session reaches the model. It also decides the
	// isolation flags, because `--bare` refuses an OAuth token.
	Credential Credential
	// BaseURL overrides the API endpoint. Specs point it at a stub.
	BaseURL string
	// Model is the model a session asks for. Empty leaves it to the CLI.
	Model string
	// TokenBudget is how many tokens a session may use before it is killed,
	// cached ones included, because a long session's context is nearly all
	// cache reads.
	TokenBudget uint64
	// Timeout is how long a session may run, and how long one of its tool
	// calls may run in the sandbox.
	Timeout time.Duration
	// MaxTurns is how many turns the CLI allows a session.
	MaxTurns int
	// MaxBudgetUSD is the spend at which the CLI stops a session.
	MaxBudgetUSD float64
	// Verify, if set, runs in the sandbox after the agent finishes and after
	// its changes are read, to record whether the repository's own checks
	// pass.
	Verify string
}

// DefaultConfig runs the `claude` on PATH with credential and the default
// limits.
func DefaultConfig(credential Credential) Config {
	return Config{
		Binary:       "claude",
		Credential:   credential,
		TokenBudget:  DefaultTokenBudget,
		Timeout:      DefaultTimeout,
		MaxTurns:     DefaultMaxTurns,
		MaxBudgetUSD: DefaultMaxBudgetUSD,
	}
}

// Validate refuses a config with a limit left unset, because an unset limit
// would let a session run without end.
func (c Config) Validate() error {
	refuse := func(what string) error {
		return fmt.Errorf("%w: %s; start from claude.DefaultConfig, which sets every limit", ErrMisconfigured, what)
	}
	switch {
	case c.Binary == "":
		return refuse("no CLI binary is named")
	case c.Credential.kind == noCredential:
		return refuse("no model credential is set; use claude.APIKey or claude.OAuthToken")
	case c.TokenBudget == 0:
		return refuse("the token budget is zero")
	case c.Timeout <= 0:
		return refuse("the time limit is not positive")
	case c.MaxTurns <= 0:
		return refuse("the turn limit is not positive")
	case !(c.MaxBudgetUSD > 0):
		return refuse("the spend limit is not positive")
	}
	return nil
}

// Checkout names a commit to check out into a session's sandbox. Put it in a
// Fixer's input under [CheckoutField].
type Checkout struct {
	Repo hostile.Slug `json:"repo"`
	Sha  hostile.Sha  `json:"sha"`
}

// Input fields the harness takes out of a sandboxed session's input before the
// model sees it.
const (
	// WorkspaceField holds a base64 tar archive, unpacked into the sandbox.
	WorkspaceField = "$workspace"
	// CheckoutField holds a [Checkout], fetched through [Checkouts] and
	// unpacked into the sandbox.
	CheckoutField = "$checkout"
)

// Checkouts is where a commit's source comes from.
type Checkouts interface {
	// Tarball returns the source of sha in repo as a gzipped tar with one top
	// directory, as a forge serves it.
	Tarball(ctx context.Context, repo hostile.Slug, sha hostile.Sha) ([]byte, error)
}

// Harness runs agent sessions on the Claude Code CLI. Build one with [New].
type Harness struct {
	config    Config
	sandbox   sandbox.Sandbox
	spec      sandbox.Spec
	checkouts Checkouts
	fetch     *sandbox.Spec
}

var _ agent.Harness = (*Harness)(nil)

// New is a harness that runs sessions as config says. It refuses a config
// [Config.Validate] refuses. A session that may write also needs
// [Harness.WithSandbox].
func New(config Config) (*Harness, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Harness{config: config}, nil
}

// WithSandbox is where sessions that may write do their work: each gets a new
// sandbox from spec. Without one they are refused.
func (h *Harness) WithSandbox(sb sandbox.Sandbox, spec sandbox.Spec) *Harness {
	h.sandbox = sb
	h.spec = withCaches(spec)
	return h
}

// WithCheckouts is where a [Checkout] in a session's input comes from.
func (h *Harness) WithCheckouts(checkouts Checkouts) *Harness {
	h.checkouts = checkouts
	return h
}

// WithFetching fetches a checkout's dependencies first, with
// [sandbox.FetchDependencies] in a sandbox made from spec, because the agent's
// sandbox has no network.
func (h *Harness) WithFetching(spec sandbox.Spec) *Harness {
	spec = withCaches(spec)
	h.fetch = &spec
	return h
}

// CacheEnv is where builds keep their dependency caches: inside [sandbox.Workdir],
// where [sandbox.FetchScript] fills them and an agent's build reads them with no
// network. The harness sets it on every sandbox it creates, because the fetch
// must write where the build will read.
func CacheEnv() map[string]string {
	return map[string]string{
		"CARGO_HOME":                      "/work/.cargo",
		"GOPATH":                          "/work/.cache/gopath",
		"GOMODCACHE":                      "/work/.cache/go",
		"GOCACHE":                         "/work/.cache/gobuild",
		"GOTOOLCHAIN":                     "local",
		"npm_config_cache":                "/work/.cache/npm",
		"npm_config_prefer_offline":       "true",
		"XDG_CACHE_HOME":                  "/work/.cache",
		"COREPACK_HOME":                   "/work/.cache/corepack",
		"COREPACK_ENABLE_DOWNLOAD_PROMPT": "0",
		"npm_config_store_dir":            "/work/.cache/pnpm",
		"pnpm_config_store_dir":           "/work/.cache/pnpm",
		"PNPM_STORE_DIR":                  "/work/.cache/pnpm",
	}
}

// withCaches is spec with [CacheEnv] set over its own env.
func withCaches(spec sandbox.Spec) sandbox.Spec {
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, CacheEnv())
	spec.Env = env
	return spec
}

// Run runs one session. Only a recorded call reaches it: a zero pass is
// refused. A session with egress or secrets is refused, and one that may write
// is refused without a sandbox.
func (h *Harness) Run(ctx context.Context, pass agent.Pass, prompt string, grant agent.Grant, input json.RawMessage) (json.RawMessage, error) {
	if pass.IsZero() {
		return nil, fmt.Errorf("%w: a session must come through a recorded call; run it with agent.Call.Run from the run's context", ErrRefused)
	}
	if grant.Egress || grant.Secrets {
		return nil, fmt.Errorf("%w: this harness runs only sessions with no egress and no secrets, and the agent was granted more; give the agent a Quarantined or Fixer profile", ErrRefused)
	}
	if !grant.Write {
		return h.converse(ctx, prompt, input)
	}
	if h.sandbox == nil {
		return nil, fmt.Errorf("%w: a session that may write needs a sandbox, and none is configured; configure one with Harness.WithSandbox", ErrRefused)
	}
	return h.sandboxed(ctx, prompt, input)
}

// converse runs a session with no tools and reads its answer as JSON.
func (h *Harness) converse(ctx context.Context, prompt string, input json.RawMessage) (json.RawMessage, error) {
	fields, err := objectFields(input)
	if err != nil {
		return nil, err
	}
	for _, field := range []string{WorkspaceField, CheckoutField} {
		if _, ok := fields[field]; ok {
			return nil, fmt.Errorf("%w: the input holds %s, which only a session that may write can use; give the agent a Fixer profile, or leave the field out", ErrRefused, field)
		}
	}
	text, _, err := h.conversation(ctx, prompt, input, nil)
	if err != nil {
		return nil, err
	}
	return parseAnswer(text)
}

// objectFields is input's fields, or none if it is not an object.
func objectFields(input json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) || len(input) == 0 {
			return nil, fmt.Errorf("%w: the session's input is not JSON; marshal it with encoding/json", ErrRefused)
		}
		return nil, nil
	}
	return fields, nil
}

// formatUSD writes a spend as the CLI reads it.
func formatUSD(usd float64) string {
	return strconv.FormatFloat(usd, 'f', -1, 64)
}
