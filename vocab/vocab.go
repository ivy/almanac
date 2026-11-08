// Package vocab is what workflow models are made of: events, observers, publish-time sources,
// predicates, narrowing predicates and classifiers over hostile facts, maps that show hostile
// facts, agents, scans and patch policies. Each part is Go, compiled into the binary, and
// registered under a name with a label a person can read. A model refers to parts only by name,
// so a model can combine the vocabulary but never add to it (principle 2).
//
// Registering a part returns a typed value, such as a [PredicatePart] over a renovate.Pull,
// that package dsl takes, so the Go compiler refuses a part applied to a fact of the wrong type.
// Package check resolves a model's names against a [Vocabulary], and the engine calls parts
// through [Entry].
//
// A predicate over a hostile fact is a narrowing predicate. Its function takes a hostile.Of and
// is a declared function that calls hostile.Narrow or hostile.Classify itself, because
// tools/hostilevet refuses a function value handed to them:
//
//	func notesSayNothing(h hostile.Of[Review]) bool { return hostile.Narrow(h, emptyNotes) }
//	var Silent = vocab.Narrowing(v, "reviewer.silent", "the reviewer wrote no notes", notesSayNothing)
package vocab

import (
	"cmp"
	"regexp"
	"slices"

	"github.com/ivy/almanac/diag"
)

// Vocabulary is the parts one almanac's workflows can use. Every vocabulary holds the built-in
// parts: see [Young], [Spent], [Accepted] and [DefaultPolicy].
//
// Registering a part never fails on the spot, so a vocabulary can be built in package
// variables. A part that cannot be registered is kept as a problem, and [Vocabulary.Err]
// reports every one; package check refuses every model against a vocabulary with problems.
type Vocabulary struct {
	entries  map[string]*Entry
	order    []string
	problems []error
}

// New is a vocabulary holding only the built-in parts.
func New() *Vocabulary {
	v := &Vocabulary{entries: map[string]*Entry{}}
	builtins(v)
	return v
}

// Lookup is the part registered under name.
func (v *Vocabulary) Lookup(name string) (*Entry, bool) {
	entry, ok := v.entries[name]
	return entry, ok
}

// Entries are every part, in the order they were registered.
func (v *Vocabulary) Entries() []*Entry {
	entries := make([]*Entry, 0, len(v.order))
	for _, name := range v.order {
		entries = append(entries, v.entries[name])
	}
	return entries
}

// Err is every registration that failed, each a *diag.Error, or nil.
func (v *Vocabulary) Err() error {
	if len(v.problems) == 0 {
		return nil
	}
	return diag.Problems(slices.Clone(v.problems))
}

var (
	partName  = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`)
	agentName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// add registers entry, or keeps the reason it cannot be.
func (v *Vocabulary) add(entry *Entry, problem string) {
	valid := partName
	if entry.Kind == AgentKind {
		valid = agentName
	}
	switch {
	case problem != "":
	case !valid.MatchString(entry.Name):
		problem = "a name is lowercase words joined by dots, such as renovate.not_actionable"
		if entry.Kind == AgentKind {
			problem = "an agent's name is one lowercase word, such as fixer"
		}
	case entry.Label == "":
		problem = "it has no label, and views and the manifest show a part by its label"
	}
	if problem != "" {
		v.problems = append(v.problems, diag.InvalidVocabulary(cmp.Or(entry.Name, "(no name)"), problem))
		return
	}
	if _, taken := v.entries[entry.Name]; taken {
		v.problems = append(v.problems, diag.DuplicateVocabulary(entry.Name))
		return
	}
	v.entries[entry.Name] = entry
	v.order = append(v.order, entry.Name)
}
