package engine

import (
	"fmt"
	"slices"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/workflow"
)

// plan is a model with every part it names resolved. A walk reaches parts only through it.
type plan struct {
	event *vocab.Entry
	scan  *vocab.Entry
	when  *cond
	steps []step
}

// step is a model step and the part it uses: an observer, publish-time source, agent or patch
// policy. A decide uses its rules instead.
type step struct {
	model.Step
	part  *vocab.Entry
	rules []rule
}

type rule struct {
	model.Rule
	when *cond
	then []template
}

// cond is a model condition with its predicate resolved.
type cond struct {
	op   op
	pred *vocab.Entry
	args []model.Ref
	is   string
	not  *cond
	all  []cond
	any  []cond
}

// op is what a condition does with its parts.
type op int

const (
	opPred op = iota
	opNot
	opAll
	opAny
)

// template is an effect template, with the map its body shows through, if any.
type template struct {
	model.Effect
	show *vocab.Entry
}

// recordedByCx are the names a run records under besides its observations: Cx's own, the
// runtime's guard, and the model's digest, besides each agent's name. An observed fact under
// one of these would share its recordings.
var recordedByCx = []string{"cooldown", model.AttemptsFact, "hold", "screen", "guard", modelRecord}

// resolve looks up every part w names in v, once. Check has accepted w, so a part it cannot
// find is a mistake in the caller, such as a different vocabulary passed to each.
func resolve(v *vocab.Vocabulary, w model.Workflow, manifest workflow.Manifest) (plan, error) {
	r := resolver{v: v, w: w}
	var p plan
	p.event = r.part(w.Name, w.On, vocab.EventKind)
	if w.Scan != "" {
		p.scan = r.part(w.Name, w.Scan, vocab.ScanKind)
	}
	if w.When != nil {
		p.when = r.cond(w.Name+", in when", *w.When)
	}
	agents := make([]string, len(manifest.Agents))
	for i, a := range manifest.Agents {
		agents[i] = a.Name
	}
	for _, s := range w.Flow {
		at := fmt.Sprintf("%s, step %q", w.Name, s.ID)
		resolved := step{Step: s}
		switch s.Kind {
		case model.Observe:
			resolved.part = r.part(at, s.Observe.From, vocab.ObserverKind)
			if slices.Contains(recordedByCx, s.Observe.Fact) || slices.Contains(agents, s.Observe.Fact) {
				r.problems = append(r.problems, diag.BadDeclaration(at, fmt.Sprintf(
					"the fact %q is recorded under its name, which the run's context records %v and each agent's answer under: name the fact something else",
					s.Observe.Fact, recordedByCx)))
			}
		case model.Measure:
			resolved.part = r.part(at, s.Measure.From, vocab.PublishedKind)
		case model.CheckPatch:
			resolved.part = r.part(at, s.CheckPatch.Policy, vocab.PolicyKind)
		case model.Agent:
			resolved.part = r.part(at, s.Agent.Agent, vocab.AgentKind)
		case model.Decide:
			for _, each := range s.Decide {
				ruleAt := fmt.Sprintf("%s, rule %q", at, each.ID)
				resolved.rules = append(resolved.rules, r.rule(ruleAt, each))
			}
		}
		p.steps = append(p.steps, resolved)
	}
	if len(r.problems) > 0 {
		return plan{}, r.problems
	}
	return p, nil
}

type resolver struct {
	v        *vocab.Vocabulary
	w        model.Workflow
	problems diag.Problems
}

func (r *resolver) part(at, name string, kind vocab.Kind) *vocab.Entry {
	entry, ok := r.v.Lookup(name)
	if !ok || entry.Kind != kind {
		r.problems = append(r.problems, diag.UnknownName(at, string(kind), name))
		return nil
	}
	return entry
}

func (r *resolver) rule(at string, m model.Rule) rule {
	resolved := rule{Rule: m}
	if m.When != nil {
		resolved.when = r.cond(at, *m.When)
	}
	for _, e := range m.Then {
		t := template{Effect: e}
		var body *model.Body
		switch {
		case e.File != nil:
			body = e.File.Body
		case e.Ask != nil:
			body = e.Ask.Body
		}
		if body != nil && body.Map != "" {
			t.show = r.part(at, body.Map, vocab.MapKind)
		}
		resolved.then = append(resolved.then, t)
	}
	return resolved
}

func (r *resolver) cond(at string, m model.Cond) *cond {
	c := &cond{args: m.Args, is: m.Is}
	switch {
	case m.Not != nil:
		c.op, c.not = opNot, r.cond(at, *m.Not)
	case m.All != nil:
		c.op = opAll
		for _, each := range m.All {
			c.all = append(c.all, *r.cond(at, each))
		}
	case m.Any != nil:
		c.op = opAny
		for _, each := range m.Any {
			c.any = append(c.any, *r.cond(at, each))
		}
	default:
		c.pred = r.part(at, m.Pred, vocab.PredicateKind)
	}
	return c
}
