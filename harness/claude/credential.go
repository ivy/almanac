package claude

import "errors"

type credentialKind int

const (
	noCredential credentialKind = iota
	apiKey
	oauthToken
)

// Credential is how a session authenticates to the model. It stays with the
// harness process and never enters a sandbox. It prints without its secret.
type Credential struct {
	kind   credentialKind
	secret string
}

// APIKey authenticates with an Anthropic API key. Prefer it where there is a
// choice: it allows `--bare`, the tighter mode.
func APIKey(key string) Credential { return Credential{kind: apiKey, secret: key} }

// OAuthToken authenticates with a token from `claude setup-token`. `--bare`
// refuses one, so a session with a token is isolated by flags and environment
// that stand in for it (ADR 1, Consequences). Its requests also carry an
// environment block naming the temporary directory, shell and OS version.
func OAuthToken(token string) Credential { return Credential{kind: oauthToken, secret: token} }

func (c Credential) String() string {
	switch c.kind {
	case apiKey:
		return "claude.APIKey(..)"
	case oauthToken:
		return "claude.OAuthToken(..)"
	default:
		return "claude.Credential(none)"
	}
}

// GoString keeps the secret out of %#v.
func (c Credential) GoString() string { return c.String() }

// unbareFlags and unbareEnv stand in for `--bare` in a session with an OAuth
// token. Tool search has to be off, or requests carry a placeholder tool.
var (
	unbareFlags = []string{"--setting-sources=", "--disable-slash-commands"}
	unbareEnv   = []string{
		"ENABLE_TOOL_SEARCH=false",
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	}
)

// isolation is the flags that keep a session from loading anything of the
// operator's: settings, memory, hooks, skills, commands.
func (c Credential) isolation() []string {
	if c.kind == oauthToken {
		return unbareFlags
	}
	return []string{"--bare"}
}

// environ is the credential's part of a session's environment.
func (c Credential) environ() []string {
	switch c.kind {
	case apiKey:
		return []string{"ANTHROPIC_API_KEY=" + c.secret}
	case oauthToken:
		return append([]string{"CLAUDE_CODE_OAUTH_TOKEN=" + c.secret}, unbareEnv...)
	default:
		return nil
	}
}

// Errors a session can end with. Match them with errors.Is.
var (
	// ErrMisconfigured is a [Config] that [Config.Validate] refuses.
	ErrMisconfigured = errors.New("claude harness misconfigured")
	// ErrRefused is a session this harness will not run.
	ErrRefused = errors.New("session refused")
	// ErrKilled is a session killed for passing its token budget or its
	// time limit.
	ErrKilled = errors.New("session killed")
	// ErrFailed is a session the CLI ended with an error, or that ended with
	// no result.
	ErrFailed = errors.New("session failed")
	// ErrNotJSON is an answer that is not JSON.
	ErrNotJSON = errors.New("the agent's answer is not JSON")
	// ErrSandbox is a session whose sandbox could not be prepared or read.
	ErrSandbox = errors.New("session sandbox failed")
)
