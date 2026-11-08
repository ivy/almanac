package workflow

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/wire"
)

// Coded is a workflow written as Go functions of an event E and the facts F it gathers, for
// tests and samples that drive the runtime directly. An almanac's own workflows are models,
// which the engine runs. Gather does the reading, through Cx; Decide is a plain function of
// what was gathered, so policy is table-testable and re-runs on replay.
type Coded[E, F any] struct {
	Name        string
	Intent      string
	May         []effect.Kind
	RunsPerHour int
	Agents      []agent.Info
	Requires    []providers.Requirement

	// Wanted is whether an event deserves a run at all. Nil wants every event.
	Wanted func(E) bool
	// Key allows one active run per key.
	Key func(E) string
	// Subjects are what the event is about. Nil has none.
	Subjects func(E) []Subject
	Gather   func(ctx context.Context, cx Cx, event E) (F, error)
	Decide   func(F) []effect.Effect
	// Links are subjects only known after gathering. Nil has none.
	Links func(F) []Subject
	// Scan lists current subjects. Nil does not scan.
	Scan func(ctx context.Context, scanner *Scanner) ([]Found, error)
}

// Code makes a Registered workflow from w.
func Code[E any, P interface {
	*E
	wire.Event
}, F any](w Coded[E, F]) Registered {
	return coded[E, P, F]{w}
}

type coded[E any, P interface {
	*E
	wire.Event
}, F any] struct{ w Coded[E, F] }

func (c coded[E, P, F]) Name() string                          { return c.w.Name }
func (c coded[E, P, F]) Intent() string                        { return c.w.Intent }
func (c coded[E, P, F]) Kind() string                          { return P(new(E)).Kind() }
func (c coded[E, P, F]) RunsPerHour() int                      { return c.w.RunsPerHour }
func (c coded[E, P, F]) May() []effect.Kind                    { return slices.Clone(c.w.May) }
func (c coded[E, P, F]) Agents() []agent.Info                  { return slices.Clone(c.w.Agents) }
func (c coded[E, P, F]) Requirements() []providers.Requirement { return slices.Clone(c.w.Requires) }

func (c coded[E, P, F]) Handles(kind string) bool {
	event := P(new(E))
	return kind == event.Kind() || slices.Contains(event.AlsoKinds(), kind)
}

func (c coded[E, P, F]) Identify(payload json.RawMessage) (Identity, error) {
	event, err := wire.Parse[E, P](payload)
	if err != nil {
		return Identity{}, err
	}
	identity := Identity{Key: c.w.Key(event), Wanted: true}
	if c.w.Subjects != nil {
		identity.Subjects = c.w.Subjects(event)
	}
	if c.w.Wanted != nil {
		identity.Wanted = c.w.Wanted(event)
	}
	return identity, nil
}

func (c coded[E, P, F]) Execute(ctx context.Context, cx Cx, payload json.RawMessage) (Decided, error) {
	event, err := wire.Parse[E, P](payload)
	if err != nil {
		return Decided{}, err
	}
	facts, err := c.w.Gather(ctx, cx, event)
	if err != nil {
		return Decided{}, err
	}
	decided := Decided{Effects: c.w.Decide(facts)}
	if c.w.Links != nil {
		decided.Links = c.w.Links(facts)
	}
	return decided, nil
}

func (c coded[E, P, F]) Scan(ctx context.Context, scanner *Scanner) ([]Found, error) {
	if c.w.Scan == nil {
		return nil, nil
	}
	found, err := c.w.Scan(ctx, scanner)
	if err != nil {
		return nil, err
	}
	for i := range found {
		found[i].Kind = c.Kind()
	}
	return found, nil
}

func (c coded[E, P, F]) Manifest() Manifest {
	return Manifest{
		Workflow:    c.w.Name,
		Intent:      c.w.Intent,
		On:          c.Kind(),
		Scans:       c.w.Scan != nil,
		RunsPerHour: c.w.RunsPerHour,
		May:         c.May(),
		Agents:      c.Agents(),
		Requires:    c.Requirements(),
	}
}
