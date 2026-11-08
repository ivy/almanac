// Package diag holds every error a person or an agent can meet. Each says what
// happened, why it matters, and what to do next, under a stable code that has a
// page in docs/errors, which Explain prints.
package diag

import (
	"fmt"
	"strings"
	"time"
)

// Code names an error for good. Scripts and agents match on it, so a code is
// never renamed or reused.
type Code string

// Every code an almanac can report. Each has a page, docs/errors/<code>.md with
// "::" written as ".", or docs/errors-go/<code>.md for a code only the Go almanac has.
const (
	ConfigRead       Code = "almanac::config::read"
	ConfigParse      Code = "almanac::config::parse"
	ConfigUnknownKey Code = "almanac::config::unknown_key"
	ConfigSecret     Code = "almanac::config::secret"
	ConfigEnvValue   Code = "almanac::config::env_value"
	ConfigEnvMissing Code = "almanac::config::env_missing"
	Usage            Code = "almanac::usage"
	ProviderMissing  Code = "almanac::provider::missing"
	StoreFailed      Code = "almanac::store::failed"
	InputUnreadable  Code = "almanac::input::unreadable"
	InputInvalidJSON Code = "almanac::input::invalid_json"
	RuntimeStartup   Code = "almanac::runtime::startup"
	UnknownCode      Code = "almanac::explain::unknown_code"
	ScanFailed       Code = "almanac::scan::failed"
	ForgeRateLimited Code = "almanac::forge::rate_limited"
	ForgeRefused     Code = "almanac::forge::refused"
	ForgeUnreachable Code = "almanac::forge::unreachable"
	ForgeReadOnly    Code = "almanac::forge::read_only"
	ForgePushRefused Code = "almanac::forge::push_refused"
)

// Codes lists every code, in the order explain documents them.
func Codes() []Code {
	return append([]Code{
		ConfigRead, ConfigParse, ConfigUnknownKey, ConfigSecret, ConfigEnvValue, ConfigEnvMissing,
		Usage, ProviderMissing, StoreFailed, InputUnreadable, InputInvalidJSON, RuntimeStartup,
		UnknownCode, ScanFailed, ForgeRateLimited, ForgeRefused, ForgeUnreachable, ForgeReadOnly,
		ForgePushRefused,
	}, modelCodes()...)
}

// Error is an error an almanac reports. Message says what happened and Help
// what to do next. Why it matters is the same for every error with a code, so
// it comes from the code's page: see [Error.Why].
//
// Build one with the constructor for its code, so that every error with a code
// carries the same help.
type Error struct {
	Code    Code
	Message string
	Help    string
	// At points into the file the error is about, such as a line of
	// almanac.toml. Nil when the error is not about a file.
	At *Label
	// Cause is what failed underneath, kept for errors.Is and errors.As.
	Cause error
}

// Label marks the part of a file an error is about.
type Label struct {
	Source Source
	Span   Span
	// Text is printed beside the mark, such as "move this to the environment".
	Text string
}

// Source is a file as an error shows it. Text never holds a secret: whoever
// builds a Label redacts it first.
type Source struct {
	// Name is the file as a person knows it, such as almanac.toml.
	Name string
	Text string
}

// Span is a run of bytes in a Source's Text.
type Span struct {
	Offset int
	Length int
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Cause }

// Why says why the error matters, from its code's page.
func (e *Error) Why() string { return why(e.Code) }

// ReadingConfig is a config file that exists but cannot be read.
func ReadingConfig(path string, cause error) *Error {
	return &Error{
		Code:    ConfigRead,
		Message: "cannot read config file " + path,
		Help:    "check the path and its permissions, or unset ALMANAC_CONFIG to run with defaults",
		Cause:   cause,
	}
}

// InvalidConfig is a config file that does not parse, or a value of the wrong
// type or out of range.
func InvalidConfig(message string, source Source, span Span) *Error {
	return &Error{
		Code:    ConfigParse,
		Message: "config file is not valid: " + message,
		Help:    "fix the marked line; run `explain almanac::config::parse` for the accepted keys",
		At:      &Label{Source: source, Span: span, Text: "here"},
	}
}

// UnknownConfigKey is a key in the config file that Almanac does not read.
func UnknownConfigKey(key string, source Source, span Span) *Error {
	return &Error{
		Code:    ConfigUnknownKey,
		Message: fmt.Sprintf("unknown config key `%s`", key),
		Help:    "config can only tighten limits: the keys are listen, max_chain_depth, runs_per_hour_cap, overall_runs_per_hour_cap, concurrency, and scan_interval_minutes",
		At:      &Label{Source: source, Span: span, Text: "Almanac does not read this"},
	}
}

// SecretInConfig is a key in the config file that looks like a credential.
// The source must already have the secret redacted.
func SecretInConfig(key string, source Source, span Span) *Error {
	return &Error{
		Code:    ConfigSecret,
		Message: fmt.Sprintf("`%s` looks like a secret, and secrets come from the environment", key),
		Help:    "remove it from the file and set it in the environment; rotate it if it was committed",
		At:      &Label{Source: source, Span: span, Text: "move this to the environment"},
	}
}

// InvalidEnv is an ALMANAC_* variable that should hold a whole number and does not.
func InvalidEnv(name string) *Error {
	return &Error{
		Code:    ConfigEnvValue,
		Message: name + " must be a whole number",
		Help:    fmt.Sprintf("set %s to a whole number, or unset it", name),
	}
}

// MissingEnv is a variable an almanac needs and cannot find. Purpose says what
// it is for, so a person knows what to set it to.
func MissingEnv(name, purpose string) *Error {
	return &Error{
		Code:    ConfigEnvMissing,
		Message: name + " is not set",
		Help:    fmt.Sprintf("set %s in the environment: %s", name, purpose),
	}
}

// NotACommand is a command line that matches no verb. Usage is printed as the help.
func NotACommand(given, usage string) *Error {
	return &Error{
		Code:    Usage,
		Message: "not a command: " + given,
		Help:    usage,
	}
}

// MissingProviders lists what the binary's workflows need and nobody set up,
// one entry per missing provider.
func MissingProviders(missing []string) *Error {
	return &Error{
		Code:    ProviderMissing,
		Message: strings.Join(missing, "; "),
		Help:    "set the credentials or settings it reads, or add it where the almanac sets up its providers",
	}
}

// Store is a store that could not be reached or refused a write. Its message
// is the store's own.
func Store(cause error) *Error {
	return &Error{
		Code:    StoreFailed,
		Message: cause.Error(),
		Help:    "check DATABASE_URL and that PostgreSQL is running; undelivered events will be retried",
		Cause:   cause,
	}
}

// UnreadableInput is an event file that cannot be read.
func UnreadableInput(path string, cause error) *Error {
	return &Error{
		Code:    InputUnreadable,
		Message: "cannot read " + path,
		Help:    "check the path and its permissions",
		Cause:   cause,
	}
}

// InvalidJSON is an event file that is not JSON.
func InvalidJSON(path string, cause error) *Error {
	return &Error{
		Code:    InputInvalidJSON,
		Message: path + " is not valid JSON",
		Help:    "pass the raw JSON body of the event",
		Cause:   cause,
	}
}

// Startup is a runtime that cannot start, because the host refused it resources.
func Startup(cause error) *Error {
	return &Error{
		Code:    RuntimeStartup,
		Message: "cannot start the runtime",
		Help:    "check the host's limits on threads and open files",
		Cause:   cause,
	}
}

// NoSuchCode is a code given to explain that no error has.
func NoSuchCode(code string) *Error {
	return &Error{
		Code:    UnknownCode,
		Message: fmt.Sprintf("no error has the code `%s`", code),
		Help:    "copy the code exactly from the error, such as almanac::config::unknown_key",
	}
}

// Scan is a workflow's scan that stopped part way.
func Scan(workflow, reason string) *Error {
	return &Error{
		Code:    ScanFailed,
		Message: fmt.Sprintf("the scan for %s failed: %s", workflow, reason),
		Help:    "fix what this names and run backfill again; scanning twice is safe",
	}
}

// RateLimited is GitHub's rate limit, reached while doing action. It resets
// after wait.
func RateLimited(action string, wait time.Duration) *Error {
	return &Error{
		Code:    ForgeRateLimited,
		Message: fmt.Sprintf("GitHub's rate limit was reached on %s, and it resets in %ds", action, int64(wait.Seconds())),
		Help:    "wait for the reset; effects stay queued and are retried",
	}
}

// Refused is GitHub answering action with an HTTP error status.
func Refused(action string, status int) *Error {
	return &Error{
		Code:    ForgeRefused,
		Message: fmt.Sprintf("GitHub refused %s: %d", action, status),
		Help:    "check the GitHub App's permissions and that it is installed on the repository",
	}
}

// Unreachable is GitHub not answering action at all.
func Unreachable(action, reason string) *Error {
	return &Error{
		Code:    ForgeUnreachable,
		Message: fmt.Sprintf("GitHub could not be reached for %s: %s", action, reason),
		Help:    "check the network and GitHub's status; effects stay queued and are retried",
	}
}

// ReadOnly is a write asked of a client that only reads.
func ReadOnly(action string) *Error {
	return &Error{
		Code:    ForgeReadOnly,
		Message: action + " is a write, and this client is read-only",
		Help:    "a dry run should never reach this: if it did, that is a bug",
	}
}

// PushRefused is a push to branch that the forge or the patch policy turned down.
func PushRefused(branch, reason string) *Error {
	return &Error{
		Code:    ForgePushRefused,
		Message: fmt.Sprintf("push to %s refused: %s", branch, reason),
		Help:    "a later run will read the branch again and decide afresh",
	}
}
