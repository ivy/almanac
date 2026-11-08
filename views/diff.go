package views

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

// Changes are the differences between two versions of a workflow, as a reviewer of a change
// asks about them: which endings a run can reach now that it could not, which it can no longer
// reach, and which still exist but do something else or are reached another way.
type Changes struct {
	// Old and New are the digests of the versions compared.
	Old string `json:"old"`
	New string `json:"new"`
	// Settings are the declarations that changed, such as a limit.
	Settings []Setting `json:"settings,omitempty"`
	// Steps are the steps added, removed or changed.
	Steps []StepChange `json:"steps,omitempty"`
	// Added are endings only the new version has, and Removed those only the old one has.
	Added   []Outcome `json:"added,omitempty"`
	Removed []Outcome `json:"removed,omitempty"`
	// Changed are endings both have whose label, condition or effects differ.
	Changed []Change `json:"changed,omitempty"`
	// PathOnly are endings both have, with the same condition and effects, that a run
	// reaches another way: past other conditions, steps, agents or holds.
	PathOnly []Change `json:"path_only,omitempty"`
}

// Empty reports whether the two versions have the same outcomes, steps and settings.
func (c Changes) Empty() bool {
	return len(c.Settings)+len(c.Steps)+len(c.Added)+len(c.Removed)+len(c.Changed)+len(c.PathOnly) == 0
}

// Setting is a declaration that changed.
type Setting struct {
	Name string `json:"name"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// StepChange is a step added, removed or changed.
type StepChange struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Change string `json:"change"`
	// Notes say what changed in a changed step.
	Notes []string `json:"notes,omitempty"`
}

// The kinds of change to a step.
const (
	Added   = "added"
	Removed = "removed"
	Changed = "changed"
)

// Change is an ending both versions have, and what differs about it.
type Change struct {
	// Ending is the ending's ID, "step/rule".
	Ending string   `json:"ending"`
	Label  string   `json:"label"`
	Notes  []string `json:"notes"`
}

// Diff compares two versions of a workflow by their outcomes.
func Diff(v *vocab.Vocabulary, older, newer model.Workflow) (Changes, error) {
	before, err := Outcomes(v, older)
	if err != nil {
		return Changes{}, fmt.Errorf("the old version: %w", err)
	}
	after, err := Outcomes(v, newer)
	if err != nil {
		return Changes{}, fmt.Errorf("the new version: %w", err)
	}
	return Compare(before, after), nil
}

// Compare compares two summaries of a workflow. An ending is the same ending in both if it has
// the same ID; a step, if it has the same ID.
func Compare(older, newer Summary) Changes {
	c := Changes{Old: older.Digest, New: newer.Digest, Settings: settings(older, newer)}
	c.Steps = steps(older.Steps, newer.Steps)

	before := map[string]Outcome{}
	for _, o := range older.Endings {
		before[o.ID()] = o
	}
	after := map[string]bool{}
	for _, o := range newer.Endings {
		after[o.ID()] = true
		was, ok := before[o.ID()]
		if !ok {
			c.Added = append(c.Added, o)
			continue
		}
		own, path := differences(was, o)
		switch {
		case len(own) > 0:
			c.Changed = append(c.Changed, Change{Ending: o.ID(), Label: o.Label, Notes: append(own, path...)})
		case len(path) > 0:
			c.PathOnly = append(c.PathOnly, Change{Ending: o.ID(), Label: o.Label, Notes: path})
		}
	}
	for _, o := range older.Endings {
		if !after[o.ID()] {
			c.Removed = append(c.Removed, o)
		}
	}
	return c
}

func settings(older, newer Summary) []Setting {
	pairs := []Setting{
		{"intent", older.Intent, newer.Intent},
		{"on", older.On.Name, newer.On.Name},
		{"when", testText(older.When), testText(newer.When)},
		{"key", refs(older.Key), refs(newer.Key)},
		{"may", fmt.Sprint(older.May), fmt.Sprint(newer.May)},
		{"runs per hour", strconv.Itoa(older.Limits.RunsPerHour), strconv.Itoa(newer.Limits.RunsPerHour)},
		{"attempts", strconv.Itoa(older.Limits.Attempts), strconv.Itoa(newer.Limits.Attempts)},
		{"scan", partName(older.Scan), partName(newer.Scan)},
	}
	var out []Setting
	for _, s := range pairs {
		if s.Old != s.New {
			out = append(out, s)
		}
	}
	return out
}

func steps(older, newer []Step) []StepChange {
	var out []StepChange
	before := map[string]Step{}
	for _, s := range older {
		before[s.ID] = s
	}
	after := map[string]bool{}
	for _, s := range newer {
		after[s.ID] = true
		was, ok := before[s.ID]
		if !ok {
			out = append(out, StepChange{ID: s.ID, Label: s.Label, Change: Added})
			continue
		}
		var notes []string
		notes = changed(notes, "label", was.Label, s.Label)
		notes = changed(notes, "kind", string(was.Kind), string(s.Kind))
		notes = changed(notes, "what it does", was.Does, s.Does)
		notes = changed(notes, "what it produces", was.Produces, s.Produces)
		if len(notes) > 0 {
			out = append(out, StepChange{ID: s.ID, Label: s.Label, Change: Changed, Notes: notes})
		}
	}
	for _, s := range older {
		if !after[s.ID] {
			out = append(out, StepChange{ID: s.ID, Label: s.Label, Change: Removed})
		}
	}
	return out
}

// differences are what changed about an ending itself, and what changed only on the path to
// it.
func differences(older, newer Outcome) (own, path []string) {
	own = changed(own, "label", older.Label, newer.Label)
	own = changed(own, "ends when", testText(older.When), testText(newer.When))
	own = gained(own, "effect", effectTexts(older.Effects), effectTexts(newer.Effects))

	path = gained(path, "on the path", pathTexts(older.Path), pathTexts(newer.Path))
	path = gained(path, "step passed", older.Through, newer.Through)
	path = gained(path, "agent called", older.Agents, newer.Agents)
	path = gained(path, "applied along the way", effectTexts(older.Along), effectTexts(newer.Along))
	return own, path
}

func changed(notes []string, what, older, newer string) []string {
	if older == newer {
		return notes
	}
	return append(notes, fmt.Sprintf("%s: “%s” → “%s”", what, older, newer))
}

// gained notes what newer has that older lacks, and what older has that newer lacks.
func gained(notes []string, what string, older, newer []string) []string {
	for _, s := range newer {
		if !slices.Contains(older, s) {
			notes = append(notes, fmt.Sprintf("%s, added: %s", what, s))
		}
	}
	for _, s := range older {
		if !slices.Contains(newer, s) {
			notes = append(notes, fmt.Sprintf("%s, removed: %s", what, s))
		}
	}
	return notes
}

func testText(t *Test) string {
	if t == nil {
		return "always"
	}
	return t.String()
}

// pathTexts are the tests on a path with where each is tested, since two rules can test the
// same condition.
func pathTexts(ts []Test) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.String() + ", at " + t.At
	}
	return out
}

func effectTexts(es []Effect) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Text
	}
	return out
}

func refs(rs []model.Ref) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return strings.Join(out, ", ")
}

func partName(p *Part) string {
	if p == nil {
		return "none"
	}
	return p.Name
}
