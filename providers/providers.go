// Package providers holds what an almanac's workflows depend on, set up once where the binary
// is wired together: a forge, a tracker, a registry, harnesses for agents. A workflow declares
// what it requires, and the binary checks every requirement when it starts, before any run
// (R-40).
package providers

import (
	"maps"
	"reflect"
	"slices"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/hostile"
)

// Set is the providers a binary set up. Each is held under the type it is provided as, usually
// an interface such as effect.Forge.
type Set struct {
	items     map[reflect.Type]any
	harness   agent.Harness
	harnesses map[string]agent.Harness
	identity  []string
	alerts    *hostile.Slug
}

// New is an empty set.
func New() *Set {
	return &Set{items: map[reflect.Type]any{}, harnesses: map[string]agent.Harness{}}
}

// Clone is a copy of s. Providing to the copy leaves s as it was, so a runtime can replace what
// it must refuse, such as every write in a read-only pass, without changing the binary's set.
func (s *Set) Clone() *Set {
	c := &Set{
		items:     maps.Clone(s.items),
		harness:   s.harness,
		harnesses: maps.Clone(s.harnesses),
		identity:  slices.Clone(s.identity),
	}
	if s.alerts != nil {
		target := *s.alerts
		c.alerts = &target
	}
	return c
}

// Provide sets the provider of T. Name T where it is not the value's own type, as in
// providers.Provide[effect.Forge](set, forge), or a workflow requiring effect.Forge will not
// find it. Providing nil removes it.
func Provide[T any](s *Set, value T) *Set {
	key := reflect.TypeFor[T]()
	if any(value) == nil {
		delete(s.items, key)
		return s
	}
	s.items[key] = value
	return s
}

// Lookup is the provider of T, and false if there is none.
func Lookup[T any](s *Set) (T, bool) {
	value, ok := s.Get(Require[T]())
	if !ok {
		var zero T
		return zero, false
	}
	return value.(T), true
}

// Get is the provider a requirement names, and false if there is none.
func (s *Set) Get(need Requirement) (any, bool) {
	value, ok := s.items[need.t]
	return value, ok
}

// Has reports whether something provides what need names.
func (s *Set) Has(need Requirement) bool {
	_, ok := s.Get(need)
	return ok
}

// UseHarness sets the harness every agent uses unless it has its own.
func (s *Set) UseHarness(h agent.Harness) *Set {
	s.harness = h
	return s
}

// UseHarnessFor sets the harness the agent with this name uses.
func (s *Set) UseHarnessFor(agentName string, h agent.Harness) *Set {
	s.harnesses[agentName] = h
	return s
}

// HarnessFor is the harness the named agent uses: its own, or the one every agent uses.
func (s *Set) HarnessFor(agentName string) (agent.Harness, bool) {
	if h, ok := s.harnesses[agentName]; ok {
		return h, true
	}
	return s.harness, s.harness != nil
}

// ActAs sets the logins this almanac acts as, so that it can recognise its own echoes.
func (s *Set) ActAs(logins ...string) *Set {
	s.identity = append([]string(nil), logins...)
	return s
}

// Identity is the logins this almanac acts as.
func (s *Set) Identity() []string { return append([]string(nil), s.identity...) }

// AlertTo sets where a tripped budget asks a person.
func (s *Set) AlertTo(target hostile.Slug) *Set {
	s.alerts = &target
	return s
}

// Alerts is where a tripped budget asks a person, and false if nowhere is set.
func (s *Set) Alerts() (hostile.Slug, bool) {
	if s.alerts == nil {
		return hostile.Slug{}, false
	}
	return *s.alerts, true
}

// Requirement is a provider a workflow needs. A binary checks each when it starts.
type Requirement struct{ t reflect.Type }

// Require names T as needed.
func Require[T any]() Requirement { return Requirement{t: reflect.TypeFor[T]()} }

// Name is the required type, as Go writes it, such as "effect.Forge".
func (r Requirement) Name() string {
	if r.t == nil {
		return "nothing"
	}
	return r.t.String()
}

// MarshalText writes the name, so a manifest lists what a workflow requires.
func (r Requirement) MarshalText() ([]byte, error) { return []byte(r.Name()), nil }
