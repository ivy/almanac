package model

import (
	"maps"
	"slices"
)

// Clone is a copy of w that shares no memory with it, so that changing one cannot change the
// other.
func (w Workflow) Clone() Workflow {
	w.When = w.When.clone()
	w.Key = slices.Clone(w.Key)
	w.Subjects = cloneSubjects(w.Subjects)
	w.Links = cloneSubjects(w.Links)
	w.May = slices.Clone(w.May)
	w.Facts = slices.Clone(w.Facts)
	if w.Flow != nil {
		flow := make([]Step, len(w.Flow))
		for i, s := range w.Flow {
			flow[i] = s.Clone()
		}
		w.Flow = flow
	}
	return w
}

// Clone is a copy of s that shares no memory with it.
func (s Step) Clone() Step {
	if s.Observe != nil {
		o := *s.Observe
		o.Args = slices.Clone(o.Args)
		s.Observe = &o
	}
	s.Screen = clonePointer(s.Screen)
	s.Hold = clonePointer(s.Hold)
	if s.Measure != nil {
		m := *s.Measure
		m.Args = slices.Clone(m.Args)
		s.Measure = &m
	}
	s.CheckPatch = clonePointer(s.CheckPatch)
	if s.Agent != nil {
		a := *s.Agent
		a.Inputs = maps.Clone(a.Inputs)
		a.Vars = maps.Clone(a.Vars)
		a.Checkout = clonePointer(a.Checkout)
		s.Agent = &a
	}
	if s.Decide != nil {
		rules := make([]Rule, len(s.Decide))
		for i, r := range s.Decide {
			r.When = r.When.clone()
			r.Then = cloneEffects(r.Then)
			rules[i] = r
		}
		s.Decide = rules
	}
	return s
}

func (c *Cond) clone() *Cond {
	if c == nil {
		return nil
	}
	copied := *c
	copied.Args = slices.Clone(c.Args)
	copied.Not = c.Not.clone()
	copied.All = cloneConds(c.All)
	copied.Any = cloneConds(c.Any)
	return &copied
}

func cloneConds(cs []Cond) []Cond {
	if cs == nil {
		return nil
	}
	out := make([]Cond, len(cs))
	for i := range cs {
		out[i] = *cs[i].clone()
	}
	return out
}

func cloneEffects(effects []Effect) []Effect {
	if effects == nil {
		return nil
	}
	out := make([]Effect, len(effects))
	for i, e := range effects {
		e.Attest = clonePointer(e.Attest)
		e.Await = clonePointer(e.Await)
		if e.File != nil {
			f := *e.File
			f.Title.Values = slices.Clone(f.Title.Values)
			f.Body = clonePointer(f.Body)
			e.File = &f
		}
		if e.Ask != nil {
			a := *e.Ask
			a.Title.Values = slices.Clone(a.Title.Values)
			a.Body = clonePointer(a.Body)
			e.Ask = &a
		}
		e.Push = clonePointer(e.Push)
		out[i] = e
	}
	return out
}

func cloneSubjects(subjects []Subject) []Subject {
	if subjects == nil {
		return nil
	}
	out := make([]Subject, len(subjects))
	for i, s := range subjects {
		s.Of = slices.Clone(s.Of)
		out[i] = s
	}
	return out
}

// clonePointer is a pointer to a copy of what p points to, for a struct holding no slices,
// maps or pointers.
func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	copied := *p
	return &copied
}
