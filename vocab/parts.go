package vocab

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// part is what every typed part holds: the name a model refers to it by.
type part struct{ name string }

// Name is the name a model refers to the part by.
func (p part) Name() string { return p.name }

// EventPart is an event a workflow can trigger on, of Go type E.
type EventPart[E any] struct{ part }

// Event registers the event E under name. It is parsed from deliveries of E's kind and its
// also-kinds, which several events may share: a security update and any opened pull request
// are both read from "github.pull_request.opened". Its fields hold only clean values and
// hostile text ([hostile.CheckObserved]).
func Event[E any, P interface {
	*E
	wire.Event
}](v *Vocabulary, name, label string) EventPart[E] {
	event := P(new(E))
	entry := &Entry{
		Kind:   EventKind,
		Name:   name,
		Label:  label,
		Result: reflect.TypeFor[E](),
		Kinds:  append([]string{event.Kind()}, event.AlsoKinds()...),
		parse: func(payload json.RawMessage) (any, error) {
			return wire.Parse[E, P](payload)
		},
	}
	v.add(entry, shapeProblem(hostile.CheckObserved(entry.Result)))
	return EventPart[E]{part{name}}
}

// ObserverPart reads an Out from outside, given an In.
type ObserverPart[In, Out any] struct{ part }

// Observer registers read, which reads an Out through the providers it needs, given an In. Out
// must keep what it read hostile ([hostile.CheckObserved]); an Out that is clean is a fact a
// condition can test directly.
func Observer[In, Out any](v *Vocabulary, name, label string, read func(context.Context, workflow.Lookup, In) (Out, error), needs ...providers.Requirement) ObserverPart[In, Out] {
	entry := &Entry{
		Kind:   ObserverKind,
		Name:   name,
		Label:  label,
		Params: []reflect.Type{reflect.TypeFor[In]()},
		Result: reflect.TypeFor[Out](),
		Needs:  slices.Clone(needs),
		observe: func(ctx context.Context, from workflow.Lookup, args []any) (any, error) {
			in, err := arg[In](name, args, 0)
			if err != nil {
				return nil, err
			}
			return read(ctx, from, in)
		},
	}
	v.add(entry, shapeProblem(hostile.CheckObserved(entry.Result)))
	return ObserverPart[In, Out]{part{name}}
}

// PublishedPart reads when a release was published, given an In.
type PublishedPart[In any] struct{ part }

// PublishTime registers read, which finds when the release an In names was published, for a
// measure step to measure its age.
func PublishTime[In any](v *Vocabulary, name, label string, read func(context.Context, workflow.Lookup, In) (cooled.Published, error), needs ...providers.Requirement) PublishedPart[In] {
	v.add(&Entry{
		Kind:   PublishedKind,
		Name:   name,
		Label:  label,
		Params: []reflect.Type{reflect.TypeFor[In]()},
		Needs:  slices.Clone(needs),
		published: func(ctx context.Context, from workflow.Lookup, args []any) (cooled.Published, error) {
			in, err := arg[In](name, args, 0)
			if err != nil {
				return cooled.Published{}, err
			}
			return read(ctx, from, in)
		},
	}, "")
	return PublishedPart[In]{part{name}}
}

// PredicatePart answers yes or no about a T. A T that is a hostile.Of makes it a narrowing
// predicate.
type PredicatePart[T any] struct {
	part
	is string
}

// Is is the classifier's value the predicate tests for, or empty for a yes-or-no predicate.
func (p PredicatePart[T]) Is() string { return p.is }

// Predicate registers test, a yes-or-no question about a T. The label says what a yes means,
// such as "not found, conflicted, or CI still running".
func Predicate[T any](v *Vocabulary, name, label string, test func(T) bool) PredicatePart[T] {
	v.add(&Entry{
		Kind:   PredicateKind,
		Name:   name,
		Label:  label,
		Params: []reflect.Type{reflect.TypeFor[T]()},
		holds: func(args []any, _ string) (bool, error) {
			value, err := arg[T](name, args, 0)
			if err != nil {
				return false, err
			}
			return test(value), nil
		},
	}, "")
	return PredicatePart[T]{part: part{name}}
}

// Narrowing registers test, a yes-or-no question about a hostile T. Test is a declared function
// that calls hostile.Narrow itself.
func Narrowing[T any](v *Vocabulary, name, label string, test func(hostile.Of[T]) bool) PredicatePart[hostile.Of[T]] {
	return Predicate(v, name, label, test)
}

// Predicate2Part answers yes or no about an A and a B together.
type Predicate2Part[A, B any] struct{ part }

// Predicate2 registers test, a yes-or-no question about an A and a B together.
func Predicate2[A, B any](v *Vocabulary, name, label string, test func(A, B) bool) Predicate2Part[A, B] {
	v.add(&Entry{
		Kind:   PredicateKind,
		Name:   name,
		Label:  label,
		Params: []reflect.Type{reflect.TypeFor[A](), reflect.TypeFor[B]()},
		holds: func(args []any, _ string) (bool, error) {
			a, err := arg[A](name, args, 0)
			if err != nil {
				return false, err
			}
			b, err := arg[B](name, args, 1)
			if err != nil {
				return false, err
			}
			return test(a, b), nil
		},
	}, "")
	return Predicate2Part[A, B]{part{name}}
}

// Narrowing2 registers test, a yes-or-no question about two hostile values together, such as
// whether a patch takes back what an update added. Test is a declared function that zips them
// and calls hostile.Narrow itself.
func Narrowing2[A, B any](v *Vocabulary, name, label string, test func(hostile.Of[A], hostile.Of[B]) bool) Predicate2Part[hostile.Of[A], hostile.Of[B]] {
	return Predicate2(v, name, label, test)
}

// ClassifierPart sorts a hostile T into a closed set C.
type ClassifierPart[T any, C hostile.Finite] struct {
	part
	names map[C]string
}

// Classifier registers classify, which sorts a hostile T into the closed set C with
// hostile.Classify. Values names each value of C a condition can test for.
func Classifier[T any, C hostile.Finite](v *Vocabulary, name, label string, classify func(hostile.Of[T]) C, values map[string]C) ClassifierPart[T, C] {
	names := map[C]string{}
	problem := ""
	for valueName, value := range values {
		if other, taken := names[value]; taken {
			problem = fmt.Sprintf("its values %q and %q are the same value", min(other, valueName), max(other, valueName))
		}
		names[value] = valueName
	}
	if len(values) == 0 {
		problem = "it names no values for a condition to test for"
	}
	v.add(&Entry{
		Kind:   PredicateKind,
		Name:   name,
		Label:  label,
		Params: []reflect.Type{reflect.TypeFor[hostile.Of[T]]()},
		Values: slices.Sorted(maps.Keys(values)),
		holds: func(args []any, is string) (bool, error) {
			h, err := arg[hostile.Of[T]](name, args, 0)
			if err != nil {
				return false, err
			}
			want, ok := values[is]
			if !ok {
				return false, fmt.Errorf("%s has no value named %q", name, is)
			}
			return classify(h) == want, nil
		},
	}, problem)
	return ClassifierPart[T, C]{part: part{name}, names: names}
}

// Is is the narrowing predicate that holds when the classifier answers value. A value the
// classifier does not name gives a predicate the checker refuses.
func (c ClassifierPart[T, C]) Is(value C) PredicatePart[hostile.Of[T]] {
	return PredicatePart[hostile.Of[T]]{part: c.part, is: c.names[value]}
}

// MapPart turns a hostile T into hostile text.
type MapPart[T any] struct{ part }

// Map registers show, which turns a hostile T into hostile text for an effect's body, such as
// a patch's diff. Show is a declared function that calls hostile.Map itself.
func Map[T any](v *Vocabulary, name, label string, show func(hostile.Of[T]) hostile.Of[string]) MapPart[T] {
	v.add(&Entry{
		Kind:   MapKind,
		Name:   name,
		Label:  label,
		Params: []reflect.Type{reflect.TypeFor[hostile.Of[T]]()},
		Result: reflect.TypeFor[hostile.Of[string]](),
		show: func(value any) (hostile.Of[string], error) {
			h, err := arg[hostile.Of[T]](name, []any{value}, 0)
			if err != nil {
				return hostile.Of[string]{}, err
			}
			return show(h), nil
		},
	}, "")
	return MapPart[T]{part{name}}
}

// ReaderPart is a quarantined agent whose answer is a hostile Out.
type ReaderPart[Out any] struct{ part }

// Reader registers a quarantined agent, which reads hostile input and answers a hostile Out.
func Reader[Out any](v *Vocabulary, label string, a agent.Quarantined) ReaderPart[Out] {
	registerAgent(v, label, a.Info(), reflect.TypeFor[hostile.Of[Out]](),
		func(vars agent.Vars, input any, _ cooled.Cooled) (agent.Call, error) { return a.Read(vars, input) },
		decodeHostile[Out], "")
	return ReaderPart[Out]{part{a.Name()}}
}

// FixerPart is a fixer agent whose answer is a hostile Out, usually a patch.Patch.
type FixerPart[Out any] struct{ part }

// Fixer registers a fixer agent, which repairs from hostile input inside a sandbox once an
// update has cooled, and answers a hostile Out.
func Fixer[Out any](v *Vocabulary, label string, a agent.Fixer) FixerPart[Out] {
	registerAgent(v, label, a.Info(), reflect.TypeFor[hostile.Of[Out]](), a.Repair, decodeHostile[Out], "")
	return FixerPart[Out]{part{a.Name()}}
}

// WorkerPart is a worker agent whose answer is a clean Out.
type WorkerPart[Out any] struct{ part }

// Worker registers a worker agent, which acts on clean input. Its answer is read into Out,
// which must be clean ([hostile.CheckClean]).
func Worker[Out any](v *Vocabulary, label string, a agent.Worker) WorkerPart[Out] {
	registerAgent(v, label, a.Info(), reflect.TypeFor[Out](),
		func(vars agent.Vars, input any, _ cooled.Cooled) (agent.Call, error) { return a.Act(vars, input) },
		decodeClean[Out], shapeProblem(hostile.CheckClean(reflect.TypeFor[Out]())))
	return WorkerPart[Out]{part{a.Name()}}
}

// registerAgent registers an agent. Its prompt is part of its declaration, so a call cannot
// choose one, and an agent declared with a prompt that was never parsed is refused here rather
// than when a run first calls it.
func registerAgent(v *Vocabulary, label string, info agent.Info, result reflect.Type, call func(agent.Vars, any, cooled.Cooled) (agent.Call, error), answer func(hostile.Of[json.RawMessage]) (any, error), problem string) {
	if problem == "" && info.Prompt == "" {
		problem = "its prompt was never parsed: declare the agent with a prompt from agent.ParsePrompt or agent.LoadPrompt, under a name the manifest shows"
	}
	v.add(&Entry{
		Kind:   AgentKind,
		Name:   info.Name,
		Label:  label,
		Result: result,
		Agent:  &info,
		call:   call,
		answer: answer,
	}, problem)
}

func decodeHostile[Out any](raw hostile.Of[json.RawMessage]) (any, error) {
	return hostile.Decode[Out](raw)
}

func decodeClean[Out any](raw hostile.Of[json.RawMessage]) (any, error) {
	var out Out
	if err := hostile.DecodeClean(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ScanPart lists a workflow's current subjects.
type ScanPart struct{ part }

// Scan registers scan, which lists current subjects as events for backfill and catch-up,
// through the providers it needs.
func Scan(v *Vocabulary, name, label string, scan func(context.Context, *workflow.Scanner) ([]workflow.Found, error), needs ...providers.Requirement) ScanPart {
	v.add(&Entry{Kind: ScanKind, Name: name, Label: label, Needs: slices.Clone(needs), scan: scan}, "")
	return ScanPart{part{name}}
}

func shapeProblem(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
