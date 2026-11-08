package dsl

import (
	"reflect"

	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

// Ref is a fact, or a field of one, that vocabulary code receives as a T: a [CleanFact] of T,
// or a [HostileFact] whose T is a hostile.Of. Only this package's handles are Refs.
type Ref[T any] interface {
	ref() model.Ref
	declared() model.Fact
	as(T)
}

// Clean is any clean fact or field, whatever its type, for a title or a key.
type Clean interface {
	ref() model.Ref
	clean()
}

// CleanFact is a fact, or a field of one, that a condition can test and an effect can be
// filled from. Its fields may still hold hostile text, which only a narrowing predicate reads.
type CleanFact[T any] struct{ name model.Ref }

// Fact names a clean fact of type T. A step that produces it, or reads it, takes the handle:
//
//	var Pull = Fact[renovate.Pull]("pull")
func Fact[T any](name string) CleanFact[T] { return CleanFact[T]{model.Ref(name)} }

// Ref is the reference the model holds.
func (f CleanFact[T]) Ref() model.Ref { return f.name }

// Is is the condition that p holds for the fact.
func (f CleanFact[T]) Is(p vocab.PredicatePart[T]) model.Cond {
	return model.Cond{Pred: p.Name(), Args: []model.Ref{f.name}, Is: p.Is()}
}

func (f CleanFact[T]) ref() model.Ref { return f.name }
func (CleanFact[T]) as(T)             {}
func (CleanFact[T]) clean()           {}
func (f CleanFact[T]) declared() model.Fact {
	return model.Fact{Name: string(f.name), Type: reflect.TypeFor[T]().String()}
}

// HostileFact is a fact, or a field of one, that holds a hostile T: text an attacker may have
// written, or something an agent answered. Only a narrowing predicate, a map, an observer or
// an agent reads it, so it has no fields and cannot fill a title or a key.
type HostileFact[T any] struct{ name model.Ref }

// Hostile names a hostile fact holding a T.
//
//	var Logs = Hostile[string]("logs")
func Hostile[T any](name string) HostileFact[T] { return HostileFact[T]{model.Ref(name)} }

// Ref is the reference the model holds.
func (h HostileFact[T]) Ref() model.Ref { return h.name }

// Is is the condition that the narrowing predicate p holds for the fact.
func (h HostileFact[T]) Is(p vocab.PredicatePart[hostile.Of[T]]) model.Cond {
	return model.Cond{Pred: p.Name(), Args: []model.Ref{h.name}, Is: p.Is()}
}

func (h HostileFact[T]) ref() model.Ref { return h.name }
func (HostileFact[T]) as(hostile.Of[T]) {}
func (h HostileFact[T]) declared() model.Fact {
	return model.Fact{Name: string(h.name), Type: reflect.TypeFor[T]().String(), Hostile: true}
}

// Field is the clean field name, of type F, inside the clean fact of. Name is the field as the
// fact's JSON names it.
//
//	var Head = Field[hostile.Sha](Pull, "head")
func Field[F, T any](of CleanFact[T], name string) CleanFact[F] {
	return CleanFact[F]{of.name + "." + model.Ref(name)}
}

// HostileField is the hostile field name, holding an F, inside the clean fact of.
//
//	var Body = HostileField[renovate.PullText](Pull, "text")
func HostileField[F, T any](of CleanFact[T], name string) HostileFact[F] {
	return HostileFact[F]{of.name + "." + model.Ref(name)}
}

// EventOf is the fact "event": the event that triggered the run, of e's type.
func EventOf[E any](vocab.EventPart[E]) CleanFact[E] { return Fact[E](model.EventFact) }

// The built-in facts. Measure produces Age, CheckPatch produces Checked, and Attempts is read
// the first time anything needs it, or by CountAttempts.
var (
	Age      = Fact[cooled.Age](model.AgeFact)
	Attempts = Fact[vocab.Attempts](model.AttemptsFact)
	Checked  = Fact[vocab.PatchOutcome](model.CheckedFact)
	// AttemptsUsed is how many patches the key has had within the attempt window.
	AttemptsUsed = Field[int](Attempts, "used")
)

// The built-in predicates over the built-in facts.
var (
	// Cooled holds for an Age older than the cooldown.
	Cooled = vocab.Cooled
	// Young holds for an Age younger than the cooldown.
	Young = vocab.Young
	// Unknown holds for an Age no publish time dates.
	Unknown = vocab.Unknown
	// Spent holds for Attempts that reached the workflow's attempts limit.
	Spent = vocab.Spent
	// Asked holds for Attempts where a person was already asked.
	Asked = vocab.Asked
	// Accepted holds for a patch the policy accepted.
	Accepted = vocab.Accepted
	// Empty holds for a patch that changes nothing.
	Empty = vocab.Empty
	// Refused holds for a patch the policy refused.
	Refused = vocab.Refused
	// DefaultPolicy is the default patch policy.
	DefaultPolicy = vocab.DefaultPolicy
)

// Not holds when c does not.
func Not(c model.Cond) model.Cond { return model.Cond{Not: &c} }

// All holds when every one of cs holds.
func All(cs ...model.Cond) model.Cond { return model.Cond{All: cs} }

// Any holds when at least one of cs holds.
func Any(cs ...model.Cond) model.Cond { return model.Cond{Any: cs} }

// Both is the condition that p holds for a and b together, such as a patch and the lines an
// update added.
func Both[A, B any](a Ref[A], b Ref[B], p vocab.Predicate2Part[A, B]) model.Cond {
	return model.Cond{Pred: p.Name(), Args: []model.Ref{a.ref(), b.ref()}}
}
