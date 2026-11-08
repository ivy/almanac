// Package sandbox is where hostile code runs. The interface is remote-shaped:
// nothing in it names a host path or a mount, and files cross only as tar
// archives, so a hosted provider fits as well as rootless Podman. See
// docs/adrs/0001-agent-sandbox.md.
//
// Providers are in sandbox/podman and sandbox/fake. The dependency fetch step
// that runs before an agent's sandbox is built is [FetchDependencies].
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"
)

// Workdir is where a sandbox's files live. Besides a private scratch /tmp, it
// is the only place a sandboxed process can write.
const Workdir = "/work"

// Sandbox is a provider of sandboxes. Every method is safe for concurrent use.
type Sandbox interface {
	// Create starts a sandbox from spec, refusing a spec that fails
	// [Spec.Validate].
	Create(ctx context.Context, spec Spec) (ID, error)

	// Exec runs command through a shell in [Workdir], and kills it once
	// timeout has passed. A command that ran and failed is an Output with a
	// non-zero ExitCode, not an error.
	Exec(ctx context.Context, id ID, command string, timeout time.Duration) (Output, error)

	// Upload unpacks a tar archive into [Workdir].
	Upload(ctx context.Context, id ID, archive []byte) error

	// Download returns a tar archive of path, which is relative to
	// [Workdir]. A file comes back wrapped in an archive of its own.
	Download(ctx context.Context, id ID, path string) ([]byte, error)

	// Destroy ends the sandbox and everything in it. Destroying a sandbox
	// that is already gone succeeds.
	Destroy(ctx context.Context, id ID) error
}

// ID names a sandbox to the provider that created it.
type ID string

// Output is what a command printed and how it ended. Every byte of it came
// from inside a sandbox, so it is hostile.
type Output struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// TimedOut is set when the command was killed for running past its
	// time limit.
	TimedOut bool
	// Truncated is set when a stream was longer than a provider keeps, so
	// only its end is here. A caller that needs the whole of it, such as a
	// diff, must refuse a truncated Output.
	Truncated bool
}

// Purpose is why a sandbox exists, which decides what it may be given. The
// zero value is [Agent], the most restricted.
type Purpose int

const (
	// Agent runs commands an agent chose. It never has network access.
	Agent Purpose = iota
	// Fetch fetches dependencies, with no agent present and nothing worth
	// stealing.
	Fetch
)

func (p Purpose) String() string {
	switch p {
	case Agent:
		return "agent"
	case Fetch:
		return "fetch"
	default:
		return fmt.Sprintf("Purpose(%d)", int(p))
	}
}

// Egress is where a sandbox may connect. The zero value allows nothing.
type Egress struct {
	allowed bool
	domains []string
}

// NoEgress allows no connections at all.
var NoEgress = Egress{}

// AllowDomains allows connections to domains, and only to a fetch sandbox.
// Podman cannot filter by domain, so there it means the default network, which
// is safe only because a fetch sandbox holds nothing and runs no agent.
func AllowDomains(domains ...string) Egress {
	return Egress{allowed: true, domains: slices.Clone(domains)}
}

// None reports whether e allows no connections.
func (e Egress) None() bool { return !e.allowed }

// Domains lists the domains e allows.
func (e Egress) Domains() []string { return slices.Clone(e.domains) }

// Spec describes a sandbox to create. Build one with [AgentSpec] or
// [FetchSpec], which set every limit, and adjust from there.
type Spec struct {
	Image   string
	Purpose Purpose
	Egress  Egress

	// Limits. Hostile code runs here by design, so every one must be set.
	MemoryMB int
	CPUs     int
	PIDs     int
	// WorkMB is the size of Workdir, which lives in memory.
	WorkMB int
	// Lifetime is how long the sandbox may exist at all. One whose owner
	// was killed outright, and so never destroyed it, ends itself by this.
	Lifetime time.Duration

	// Env is set for every command. A provider sets HOME and TMPDIR itself,
	// to scratch space outside the checkout.
	Env map[string]string
}

// DefaultLifetime is longer than any session.
const DefaultLifetime = 2 * time.Hour

// AgentSpec is a sandbox for an agent's commands: no network, and the
// default limits.
func AgentSpec(image string) Spec {
	return Spec{
		Image:    image,
		Purpose:  Agent,
		Egress:   NoEgress,
		MemoryMB: 4096,
		CPUs:     2,
		PIDs:     512,
		WorkMB:   4096,
		Lifetime: DefaultLifetime,
	}
}

// FetchSpec is a sandbox for fetching dependencies, which may reach domains.
func FetchSpec(image string, domains ...string) Spec {
	spec := AgentSpec(image)
	spec.Purpose = Fetch
	spec.Egress = AllowDomains(domains...)
	return spec
}

// ErrRefused is a spec no provider will create.
var ErrRefused = errors.New("refused")

// Validate refuses a spec that is a mistake whatever the provider: an agent
// sandbox with any egress, or a limit left unset.
func (s Spec) Validate() error {
	refuse := func(reason string) error {
		return &Error{Op: "create", Err: fmt.Errorf("%w: %s", ErrRefused, reason)}
	}
	if s.Purpose == Agent && !s.Egress.None() {
		return refuse("an agent sandbox must have no network access; give egress only to a fetch sandbox")
	}
	if s.Purpose != Agent && s.Purpose != Fetch {
		return refuse(fmt.Sprintf("unknown purpose %s; use sandbox.Agent or sandbox.Fetch", s.Purpose))
	}
	if strings.TrimSpace(s.Image) == "" {
		return refuse("the spec names no image; start from sandbox.AgentSpec or sandbox.FetchSpec")
	}
	limits := []struct {
		name  string
		value int
	}{{"MemoryMB", s.MemoryMB}, {"CPUs", s.CPUs}, {"PIDs", s.PIDs}, {"WorkMB", s.WorkMB}}
	for _, limit := range limits {
		if limit.value <= 0 {
			return refuse(fmt.Sprintf("%s is %d, and every limit must be positive; start from sandbox.AgentSpec or sandbox.FetchSpec", limit.name, limit.value))
		}
	}
	if s.Lifetime < time.Second {
		return refuse(fmt.Sprintf("Lifetime is %s, and a sandbox must end itself after at least a second", s.Lifetime))
	}
	for _, name := range slices.Sorted(maps.Keys(s.Env)) {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return refuse(fmt.Sprintf("%q is not an environment variable name", name))
		}
	}
	return nil
}

// CheckPath refuses a path that is not relative to [Workdir] or that leaves
// it, and returns it cleaned.
func CheckPath(p string) (string, error) {
	clean := path.Clean(p)
	if p == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q is not a path inside %s; name a path relative to it", ErrRefused, p, Workdir)
	}
	return clean, nil
}

// ErrNotFound is a sandbox that does not exist, or no longer does.
var ErrNotFound = errors.New("no such sandbox: it was destroyed, or ended itself after its lifetime; create another")

// Error is a sandbox operation that failed. Match its cause with errors.Is
// against [ErrRefused], [ErrNotFound] or [ErrFetchFailed].
type Error struct {
	// Op is the operation: create, exec, upload, download, destroy, or fetch.
	Op string
	// Sandbox is empty when no sandbox was created.
	Sandbox ID
	Err     error
}

func (e *Error) Error() string {
	if e.Sandbox == "" {
		return fmt.Sprintf("sandbox %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("sandbox %s %s: %v", e.Op, e.Sandbox, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// CommandFailed is a command in a sandbox that exited with a non-zero code or
// ran out of time. Output came from inside the sandbox, so it is hostile and
// stays out of the message: a caller that shows it must treat it as such.
type CommandFailed struct {
	Command string
	Output  Output
}

func (e *CommandFailed) Error() string {
	if e.Output.TimedOut {
		return fmt.Sprintf("`%s` ran past its time limit and was killed", e.Command)
	}
	return fmt.Sprintf("`%s` exited with code %d", e.Command, e.Output.ExitCode)
}

// run is Exec for commands of Almanac's own, where a non-zero exit is an error.
func run(ctx context.Context, sb Sandbox, id ID, command string, timeout time.Duration) error {
	out, err := sb.Exec(ctx, id, command, timeout)
	if err != nil {
		return err
	}
	if out.ExitCode != 0 || out.TimedOut {
		return &CommandFailed{Command: command, Output: out}
	}
	return nil
}
