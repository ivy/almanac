package dsl

import (
	"time"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/vocab"
)

// Step is one step of a flow, for Define.
type Step struct {
	step  model.Step
	facts []model.Fact
}

func (s Step) apply(b *builder) {
	b.w.Flow = append(b.w.Flow, s.step)
	b.steps = append(b.steps, s)
}

// Model is the step as the model holds it.
func (s Step) Model() model.Step { return s.step }

func step(id, label string, kind model.StepKind, produces ...model.Fact) Step {
	return Step{step: model.Step{ID: id, Label: label, Kind: kind}, facts: produces}
}

// Observe reads out from outside with the observer from, given in, and records it.
func Observe[In, Out any](id, label string, out Ref[Out], from vocab.ObserverPart[In, Out], in Ref[In]) Step {
	s := step(id, label, model.Observe, out.declared())
	s.step.Observe = &model.ObserveStep{Fact: string(out.ref()), From: from.Name(), Args: []model.Ref{in.ref()}}
	return s
}

// Screen asks a classifier question about text, and ends the run with no effects unless the
// probability of yes is above above. It gates spending, never safety: hostile text can move a
// classifier.
func Screen(id, label, question string, text HostileFact[string], above float64) Step {
	s := step(id, label, model.Screen)
	s.step.Screen = &model.ScreenStep{Question: question, Text: text.name, Above: above}
	return s
}

// Hold sets the status context pending on sha in repo now, before an agent starts, so nothing
// can merge while the run is under way. It is the only effect that applies before a run ends.
func Hold(id, label, context string, repo CleanFact[hostile.Slug], sha CleanFact[hostile.Sha]) Step {
	s := step(id, label, model.Hold)
	s.step.Hold = &model.HoldStep{Context: context, Repo: repo.name, Sha: sha.name}
	return s
}

// Measure reads when the release in names was published, with from, and measures its age
// against minAge, producing [Age]: Cooled, Young or Unknown. A fixer needs a measure before it.
func Measure[In any](id, label string, from vocab.PublishedPart[In], in Ref[In], minAge time.Duration) Step {
	s := step(id, label, model.Measure, Age.declared())
	s.step.Measure = &model.MeasureStep{From: from.Name(), Args: []model.Ref{in.ref()}, MinAge: model.Duration(minAge)}
	return s
}

// CountAttempts reads what was tried on the run's key before, producing [Attempts]. Without
// it, Attempts is read the first time a condition or an AskOnce needs it.
func CountAttempts(id, label string) Step {
	return step(id, label, model.Attempts, Attempts.declared())
}

// CheckPatch checks the agent's patch, made against base, with policy, producing [Checked]:
// Accepted, Empty or Refused. Only an accepted patch can be pushed.
func CheckPatch(id, label string, p HostileFact[patch.Patch], base CleanFact[hostile.Sha], policy vocab.PolicyPart) Step {
	s := step(id, label, model.CheckPatch, Checked.declared())
	s.step.CheckPatch = &model.CheckPatchStep{Patch: p.name, Base: base.name, Policy: policy.Name()}
	return s
}

// AgentOption is what an agent is given: an input, a prompt variable, or a checkout.
type AgentOption func(*model.AgentStep)

// Input gives the agent r, under name, as data it reads. It may be hostile.
func Input[T any](name string, r Ref[T]) AgentOption {
	return func(a *model.AgentStep) {
		if a.Inputs == nil {
			a.Inputs = map[string]model.Ref{}
		}
		a.Inputs[name] = r.ref()
	}
}

// Var fills the prompt variable name with the clean fact f.
func Var[T any](name string, f CleanFact[T]) AgentOption {
	return func(a *model.AgentStep) {
		if a.Vars == nil {
			a.Vars = map[string]model.Ref{}
		}
		a.Vars[name] = f.name
	}
}

// CheckoutAt has the harness check out sha in repo for the agent.
func CheckoutAt(repo CleanFact[hostile.Slug], sha CleanFact[hostile.Sha]) AgentOption {
	return func(a *model.AgentStep) { a.Checkout = &model.Checkout{Repo: repo.name, Sha: sha.name} }
}

func agentStep(id, label string, out model.Fact, name string, opts []AgentOption) Step {
	s := step(id, label, model.Agent, out)
	s.step.Agent = &model.AgentStep{Fact: out.Name, Agent: name}
	for _, opt := range opts {
		opt(s.step.Agent)
	}
	return s
}

// Read asks the quarantined agent a about its inputs. Its answer is the hostile fact out.
func Read[Out any](id, label string, out HostileFact[Out], a vocab.ReaderPart[Out], opts ...AgentOption) Step {
	return agentStep(id, label, out.declared(), a.Name(), opts)
}

// Repair asks the fixer a to repair from its inputs, in a sandbox at its checkout. Its answer
// is the hostile fact out, usually a patch for CheckPatch. A Measure must come before it.
func Repair[Out any](id, label string, out HostileFact[Out], a vocab.FixerPart[Out], opts ...AgentOption) Step {
	return agentStep(id, label, out.declared(), a.Name(), opts)
}

// Act asks the worker a to act on its inputs, which must be clean. Its answer is the clean
// fact out.
func Act[Out any](id, label string, out CleanFact[Out], a vocab.WorkerPart[Out], opts ...AgentOption) Step {
	return agentStep(id, label, out.declared(), a.Name(), opts)
}

// Decide tries rules in order. The first that matches ends the run with its effects. If none
// matches, the run goes on to the next step, so the last decide ends with Otherwise.
func Decide(id, label string, rules ...model.Rule) Step {
	s := step(id, label, model.Decide)
	s.step.Decide = rules
	return s
}

// Rule ends the run with then when the condition when holds. A rule with no effects ends the
// run having decided to do nothing.
func Rule(id, label string, when model.Cond, then ...Effect) model.Rule {
	return model.Rule{ID: id, Label: label, When: &when, Then: effects(then)}
}

// Otherwise ends the run with then. It always matches, so it is the last rule of a decide.
func Otherwise(id, label string, then ...Effect) model.Rule {
	return model.Rule{ID: id, Label: label, Then: effects(then)}
}

func effects(templates []Effect) []model.Effect {
	out := make([]model.Effect, 0, len(templates))
	for _, t := range templates {
		out = append(out, t.Model())
	}
	return out
}
