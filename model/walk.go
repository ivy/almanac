package model

import (
	"maps"
	"slices"
)

// Ending is one way a run can end: a rule of a decide step, or a screen that stops it.
type Ending struct {
	// Step is the step that ends the run.
	Step string
	// Rule is the rule that matched, or empty for a screen.
	Rule  string
	Label string
	// Effects are what the run produces when it ends here.
	Effects []Effect
}

// Endings are every way a run of w can end, in flow order. A run whose event is not wanted,
// or whose screen stops it, ends with no effects; a run that ends at a rule ends with the
// rule's effects.
func (w Workflow) Endings() []Ending {
	var endings []Ending
	for _, step := range w.Flow {
		switch step.Kind {
		case Screen:
			endings = append(endings, Ending{Step: step.ID, Label: step.Label})
		case Decide:
			for _, rule := range step.Decide {
				endings = append(endings, Ending{Step: step.ID, Rule: rule.ID, Label: rule.Label, Effects: rule.Then})
			}
		}
	}
	return endings
}

// Refs are every reference to a fact in w, in the order they are written.
func (w Workflow) Refs() []Ref {
	var refs []Ref
	if w.When != nil {
		refs = append(refs, w.When.Refs()...)
	}
	refs = append(refs, w.Key...)
	for _, subject := range append(slices.Clone(w.Subjects), w.Links...) {
		refs = append(refs, subject.Of...)
	}
	for _, step := range w.Flow {
		refs = append(refs, step.Refs()...)
	}
	return refs
}

// Refs are every reference to a fact in s, in the order they are written. An ask that is
// made once reads the fact "attempts".
func (s Step) Refs() []Ref {
	var refs []Ref
	switch {
	case s.Observe != nil:
		refs = append(refs, s.Observe.Args...)
	case s.Screen != nil:
		refs = append(refs, s.Screen.Text)
	case s.Hold != nil:
		refs = append(refs, s.Hold.Repo, s.Hold.Sha)
	case s.Measure != nil:
		refs = append(refs, s.Measure.Args...)
	case s.CheckPatch != nil:
		refs = append(refs, s.CheckPatch.Patch, s.CheckPatch.Base)
	case s.Agent != nil:
		refs = append(refs, sortedValues(s.Agent.Inputs)...)
		refs = append(refs, sortedValues(s.Agent.Vars)...)
		if c := s.Agent.Checkout; c != nil {
			refs = append(refs, c.Repo, c.Sha)
		}
	}
	for _, rule := range s.Decide {
		if rule.When != nil {
			refs = append(refs, rule.When.Refs()...)
		}
		for _, e := range rule.Then {
			refs = append(refs, e.Refs()...)
		}
	}
	return refs
}

// Refs are every reference to a fact in c.
func (c Cond) Refs() []Ref {
	refs := slices.Clone(c.Args)
	if c.Not != nil {
		refs = append(refs, c.Not.Refs()...)
	}
	for _, each := range slices.Concat(c.All, c.Any) {
		refs = append(refs, each.Refs()...)
	}
	return refs
}

// Refs are every reference to a fact in e.
func (e Effect) Refs() []Ref {
	switch {
	case e.Attest != nil:
		return []Ref{e.Attest.Repo, e.Attest.Sha}
	case e.File != nil:
		return append([]Ref{e.File.Target}, e.File.Title.refs(e.File.Body)...)
	case e.Ask != nil:
		refs := []Ref{e.Ask.Target}
		if e.Ask.Pull != "" {
			refs = append(refs, e.Ask.Pull)
		}
		if e.Ask.Once {
			refs = append(refs, AttemptsFact)
		}
		return append(refs, e.Ask.Title.refs(e.Ask.Body)...)
	case e.Push != nil:
		return []Ref{e.Push.Checked, e.Push.Repo, e.Push.Pull, e.Push.Branch}
	}
	return nil
}

func (t Title) refs(body *Body) []Ref {
	refs := slices.Clone(t.Values)
	if body != nil && body.Showing != "" {
		refs = append(refs, body.Showing)
	}
	return refs
}

func sortedValues(m map[string]Ref) []Ref {
	var refs []Ref
	for _, key := range slices.Sorted(maps.Keys(m)) {
		refs = append(refs, m[key])
	}
	return refs
}
