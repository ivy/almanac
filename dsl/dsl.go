// Package dsl writes workflow models in Go. Define takes a workflow's declarations, then its
// steps in the order a run takes them, and returns a model.Workflow: plain data that package
// check accepts or refuses and the engine runs. Nothing written here runs during an event.
//
// Facts are typed handles. A [CleanFact] can be tested by a predicate and fill an effect; a
// [HostileFact] can only be tested by a narrowing predicate, shown through a map, or read by an
// observer or an agent, so a hostile fact in a key, a title or a clean predicate does not
// compile. The checker still runs on every model, for the rules types cannot carry: a fact
// read before a step produces it, an effect the workflow may not produce, a run with no ending.
//
// The package is meant to be dot-imported in a file that defines workflows:
//
//	var BuildFailures = Define("build_failures",
//		Intent("Repair a Renovate pull request whose build failed, after its cooldown."),
//		On(renovate.BuildChanged),
//		...
//		Observe("pull", "Read the pull request", Pull, renovate.PullForBranch, Event),
//		Decide("actionable", "Is there anything to do?",
//			Rule("nothing", "Nothing to do", Pull.Is(renovate.NotActionable)),
//		),
//		...
//	)
package dsl

import (
	"slices"

	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

// Part is a declaration or a step, for Define.
type Part interface{ apply(*builder) }

type builder struct {
	w     model.Workflow
	steps []Step
}

type partFunc func(*builder)

func (f partFunc) apply(b *builder) { f(b) }

// Define is the workflow name, made of parts: declarations, in any order, and steps, in the
// order a run takes them. It never fails: package check says what is wrong with the result.
// The workflow shares no memory with its parts, so one step can be used in several workflows.
func Define(name string, parts ...Part) model.Workflow {
	b := &builder{w: model.Workflow{Name: name}}
	for _, p := range parts {
		p.apply(b)
	}
	b.w.Facts = facts(b.w.Facts, b.steps)
	return b.w.Clone()
}

// facts lists the facts the flow produces after declared, which holds the event: the attempts
// fact first where a step reads it without producing it, then each step's own. A fact two steps
// produce is listed once, and the checker refuses the second step.
func facts(declared []model.Fact, steps []Step) []model.Fact {
	have := func(name string) bool {
		return slices.ContainsFunc(declared, func(f model.Fact) bool { return f.Name == name })
	}
	for _, s := range steps {
		if !have(model.AttemptsFact) && slices.ContainsFunc(s.step.Refs(), readsAttempts) {
			declared = append(declared, Attempts.declared())
		}
		for _, f := range s.facts {
			if !slices.Contains(declared, f) {
				declared = append(declared, f)
			}
		}
	}
	return declared
}

func readsAttempts(r model.Ref) bool { return r.Fact() == model.AttemptsFact }

// Intent is what the workflow is for, in a sentence a person reads in review.
func Intent(text string) Part { return partFunc(func(b *builder) { b.w.Intent = text }) }

// On is the event that triggers the workflow.
func On[E any](event vocab.EventPart[E]) Part {
	return partFunc(func(b *builder) {
		b.w.On = event.Name()
		b.w.Facts = append(b.w.Facts, EventOf(event).declared())
	})
}

// When is whether an event deserves a run at all, tested over the event alone before any run
// starts, so most deliveries cost nothing.
func When(c model.Cond) Part { return partFunc(func(b *builder) { b.w.When = &c }) }

// Key is the event's fields whose values, joined with ":", allow one active run.
func Key(fields ...Clean) Part {
	return partFunc(func(b *builder) {
		for _, f := range fields {
			b.w.Key = append(b.w.Key, f.ref())
		}
	})
}

// Subjects are what the event is about, so another workflow can find what this one learned.
func Subjects(subjects ...model.Subject) Part {
	return partFunc(func(b *builder) { b.w.Subjects = append(b.w.Subjects, subjects...) })
}

// Links are subjects only known once a run has gathered, read from its facts when it ends.
func Links(subjects ...model.Subject) Part {
	return partFunc(func(b *builder) { b.w.Links = append(b.w.Links, subjects...) })
}

// RepoSubject is a repository as a subject.
func RepoSubject(repo CleanFact[hostile.Slug]) model.Subject {
	return model.Subject{Kind: model.RepoSubject, Of: []model.Ref{repo.name}}
}

// PackageSubject is a package in a repository as a subject.
func PackageSubject(repo, pkg CleanFact[hostile.Slug]) model.Subject {
	return model.Subject{Kind: model.PackageSubject, Of: []model.Ref{repo.name, pkg.name}}
}

// The kinds of effect a workflow may produce, for May.
const (
	Attests = effect.KindAttest
	Awaits  = effect.KindAwait
	Files   = effect.KindFile
	Asks    = effect.KindAsk
	Pushes  = effect.KindPush
)

// May is every kind of effect the workflow may produce. The runtime applies no other.
func May(kinds ...effect.Kind) Part {
	return partFunc(func(b *builder) { b.w.May = append(b.w.May, kinds...) })
}

// Limit is one of a workflow's budgets.
type Limit func(*model.Limits)

// Limits are the workflow's own budgets. Config can lower them, never raise them.
func Limits(limits ...Limit) Part {
	return partFunc(func(b *builder) {
		for _, l := range limits {
			l(&b.w.Limits)
		}
	})
}

// PerHour is how many runs the workflow may start in an hour.
func PerHour(runs int) Limit { return func(l *model.Limits) { l.RunsPerHour = runs } }

// MaxAttempts is how many patches a key may have within the attempt window before Spent holds.
func MaxAttempts(n int) Limit { return func(l *model.Limits) { l.Attempts = n } }

// Scan is how the workflow finds its current subjects, for backfill and catch-up.
func Scan(scan vocab.ScanPart) Part { return partFunc(func(b *builder) { b.w.Scan = scan.Name() }) }
