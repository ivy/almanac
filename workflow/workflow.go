// Package workflow is the seam between workflows and the runtime. A workflow is a [Registered]:
// it says what it handles, what it may do and what it needs, identifies the event a payload
// holds, and executes a run against a [Cx]. The runtime implements Cx and runs Registered
// workflows without knowing how one is written: the engine interprets a model as one, and
// [Code] makes one from Go functions.
//
// Everything a run depends on outside itself goes through Cx and is recorded by name and
// occurrence, so a replay reaches the same decision without reading or spending again
// (principle 7).
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
)

// Registered is a workflow as the runtime holds it.
type Registered interface {
	// Name names the workflow in the store, the manifest and every diagnostic.
	Name() string
	// Intent is what the workflow is for. A guard checks actions against it.
	Intent() string
	// Kind is the kind of event the workflow is triggered by, and the kind its scans find.
	Kind() string
	// Handles reports whether a delivery of this kind can trigger the workflow: its own
	// kind, or another its event is also parsed from.
	Handles(kind string) bool
	// RunsPerHour is the workflow's own budget. Config can lower it and never raise it.
	RunsPerHour() int
	// May is the effects the workflow may produce. The runtime denies any other.
	May() []effect.Kind
	// Agents are the agents the workflow may call. Calling any other fails.
	Agents() []agent.Info
	// Requirements are the providers the workflow needs, checked when the binary starts.
	Requirements() []providers.Requirement
	// Identify parses a payload just far enough to correlate it: its key, its subjects, and
	// whether it deserves a run at all. A payload that does not parse is a *wire.Malformed.
	Identify(payload json.RawMessage) (Identity, error)
	// Execute runs the workflow on a payload. A recorded decision to stop is a *Halt; any
	// other error is a failure.
	Execute(ctx context.Context, cx Cx, payload json.RawMessage) (Decided, error)
	// Scan lists the workflow's current subjects as events, for backfill and catch-up. It
	// must find the same key a webhook would for the same subject, or the two do not
	// coalesce. A workflow that does not scan finds nothing.
	Scan(ctx context.Context, scanner *Scanner) ([]Found, error)
	// Manifest is the reviewable declaration: trigger, limits, effects and capabilities.
	Manifest() Manifest
}

// Identity is what correlates a delivery with a run, before any run starts.
type Identity struct {
	// Key allows one active run per key.
	Key      string
	Subjects []Subject
	// Wanted is false for an event that deserves no run, so most deliveries cost nothing.
	Wanted bool
}

// Decided is what a run decided: the effects to apply, and subjects only known once the run
// had gathered, such as a package read from a lockfile.
type Decided struct {
	Effects []effect.Effect
	Links   []Subject
}

// Manifest is a workflow's reviewable declaration, so a change in what it may do shows in
// review.
type Manifest struct {
	Workflow    string                  `json:"workflow"`
	Intent      string                  `json:"intent"`
	On          string                  `json:"on"`
	Scans       bool                    `json:"scans"`
	RunsPerHour int                     `json:"runs_per_hour"`
	May         []effect.Kind           `json:"may"`
	Agents      []agent.Info            `json:"agents"`
	Requires    []providers.Requirement `json:"requires"`
}

// Halt is a run that decided to stop: a recorded decision, not a failure.
type Halt struct {
	Reason string
}

func (h *Halt) Error() string { return "stopped: " + h.Reason }

// Stop ends a run on a decision, for a reason a person can read.
func Stop(reason string) error { return &Halt{Reason: reason} }

// Subject is a durable link between runs, so a workflow can find what another learned.
type Subject struct{ text string }

// RepoSubject is a repository as a subject.
func RepoSubject(repo hostile.Slug) Subject { return Subject{text: "repo:" + repo.String()} }

// PackageSubject is a package in a repository as a subject.
func PackageSubject(repo, pkg hostile.Slug) Subject {
	return Subject{text: "pkg:" + repo.String() + ":" + pkg.String()}
}

func (s Subject) String() string { return s.text }

// MarshalText writes the subject as the store keys it, such as "repo:octo-org/hello".
func (s Subject) MarshalText() ([]byte, error) { return []byte(s.text), nil }

var errNotASubject = errors.New(`workflow: a subject is "repo:" or "pkg:" and a slug`)

// UnmarshalText reads a subject as MarshalText writes it, and refuses any other text.
func (s *Subject) UnmarshalText(text []byte) error {
	kind, rest, _ := strings.Cut(string(text), ":")
	if kind != "repo" && kind != "pkg" {
		return errNotASubject
	}
	if _, err := hostile.ParseSlug(rest); err != nil {
		return errNotASubject
	}
	*s = Subject{text: string(text)}
	return nil
}
