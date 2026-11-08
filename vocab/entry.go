package vocab

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/workflow"
)

// Kind is what a part of the vocabulary is.
type Kind string

// The kinds of part.
const (
	// EventKind parses a delivery into an event.
	EventKind Kind = "event"
	// ObserverKind reads from outside, through providers, from the facts it is given.
	ObserverKind Kind = "observer"
	// PublishedKind reads when a release was published, for a measure step.
	PublishedKind Kind = "published"
	// PredicateKind answers yes or no about facts. A classifier is a predicate that answers
	// with one of its Values, and a condition names the value it wants.
	PredicateKind Kind = "predicate"
	// MapKind turns a hostile fact into hostile text, for an effect's body.
	MapKind Kind = "map"
	// AgentKind is an agent a workflow may call. Its name is the agent's.
	AgentKind Kind = "agent"
	// ScanKind lists a workflow's current subjects as events.
	ScanKind Kind = "scan"
	// PolicyKind is a patch policy, for a check_patch step.
	PolicyKind Kind = "policy"
)

// Entry is one registered part, as package check and the engine see it. Its typed
// counterpart, such as a [PredicatePart], is what a workflow is written with.
type Entry struct {
	Kind  Kind
	Name  string
	Label string
	// Params are the Go types the part reads, in order. A hostile parameter is a hostile.Of.
	Params []reflect.Type
	// Result is the Go type of what the part gives: an event's type, an observer's reading,
	// or an agent's answer as a fact holds it, which is a hostile.Of unless the agent is a
	// worker. Nil for other parts.
	Result reflect.Type
	// Values are a classifier's answers, by name. Nil for any other part.
	Values []string
	// Needs are the providers the part reads through.
	Needs []providers.Requirement
	// Kinds are the kinds of delivery an event is parsed from: its own, then its also-kinds.
	Kinds []string
	// Agent is an agent's declaration as the manifest shows it. Nil for any other part.
	Agent *agent.Info

	parse     func(json.RawMessage) (any, error)
	observe   func(context.Context, workflow.Lookup, []any) (any, error)
	published func(context.Context, workflow.Lookup, []any) (cooled.Published, error)
	holds     func([]any, string) (bool, error)
	show      func(any) (hostile.Of[string], error)
	call      func(agent.Vars, any, cooled.Cooled) (agent.Call, error)
	answer    func(hostile.Of[json.RawMessage]) (any, error)
	scan      func(context.Context, *workflow.Scanner) ([]workflow.Found, error)
	policy    patch.Policy
}

func (e *Entry) wrongKind(want Kind) error {
	return fmt.Errorf("%s is a %s, not a %s: a checked model never asks this, so the caller is wrong", e.Name, e.Kind, want)
}

// Parse reads an event from a delivery's payload. The event is a value of e.Result. A payload
// that does not parse is a *wire.Malformed.
func (e *Entry) Parse(payload json.RawMessage) (any, error) {
	if e.parse == nil {
		return nil, e.wrongKind(EventKind)
	}
	return e.parse(payload)
}

// Observe reads from outside, with args of e.Params' types, and returns the reading as JSON,
// ready to record. Decode it with [Decode] into e.Result.
func (e *Entry) Observe(ctx context.Context, from workflow.Lookup, args []any) (json.RawMessage, error) {
	if e.observe == nil {
		return nil, e.wrongKind(ObserverKind)
	}
	reading, err := e.observe(ctx, from, args)
	if err != nil {
		return nil, err
	}
	return json.Marshal(reading)
}

// Published reads when a release was published, with args of e.Params' types.
func (e *Entry) Published(ctx context.Context, from workflow.Lookup, args []any) (cooled.Published, error) {
	if e.published == nil {
		return cooled.Published{}, e.wrongKind(PublishedKind)
	}
	return e.published(ctx, from, args)
}

// Holds answers a predicate over args of e.Params' types. For a classifier, it holds when the
// answer is the value named is.
func (e *Entry) Holds(args []any, is string) (bool, error) {
	if e.holds == nil {
		return false, e.wrongKind(PredicateKind)
	}
	return e.holds(args, is)
}

// Show turns a hostile fact of e.Params[0]'s type into hostile text.
func (e *Entry) Show(arg any) (hostile.Of[string], error) {
	if e.show == nil {
		return hostile.Of[string]{}, e.wrongKind(MapKind)
	}
	return e.show(arg)
}

// Call makes a call to the agent, ready for the run's context to run. A fixer needs the proof
// that the update has cooled, and refuses a zero one; other agents ignore it.
func (e *Entry) Call(vars agent.Vars, input any, proof cooled.Cooled) (agent.Call, error) {
	if e.call == nil {
		return agent.Call{}, e.wrongKind(AgentKind)
	}
	return e.call(vars, input, proof)
}

// Answer reads the agent's answer as the fact holds it, a value of e.Result: still hostile,
// unless the agent is a worker, whose answer is decoded into a clean type.
func (e *Entry) Answer(raw hostile.Of[json.RawMessage]) (any, error) {
	if e.answer == nil {
		return nil, e.wrongKind(AgentKind)
	}
	return e.answer(raw)
}

// Scan lists the subjects a scan finds, as events.
func (e *Entry) Scan(ctx context.Context, scanner *workflow.Scanner) ([]workflow.Found, error) {
	if e.scan == nil {
		return nil, e.wrongKind(ScanKind)
	}
	return e.scan(ctx, scanner)
}

// Policy is the patch policy a policy part holds.
func (e *Entry) Policy() (patch.Policy, error) {
	if e.Kind != PolicyKind {
		return patch.Policy{}, e.wrongKind(PolicyKind)
	}
	return e.policy, nil
}

// Decode reads JSON, such as a recorded reading, as a value of t. Hostile values stay hostile:
// a hostile.Of decodes only from its tagged form.
func Decode(t reflect.Type, raw json.RawMessage) (any, error) {
	value := reflect.New(t)
	if err := json.Unmarshal(raw, value.Interface()); err != nil {
		return nil, fmt.Errorf("a recorded value does not decode as a %s: the vocabulary changed what it reads, so replay this run with the version that recorded it: %w", t, err)
	}
	return value.Elem().Interface(), nil
}

// arg is args[i] as a T, or an error naming what the caller passed instead.
func arg[T any](name string, args []any, i int) (T, error) {
	var zero T
	if i >= len(args) {
		return zero, fmt.Errorf("%s takes %d arguments and was given %d", name, i+1, len(args))
	}
	typed, ok := args[i].(T)
	if !ok {
		return zero, fmt.Errorf("%s takes a %s as argument %d and was given a %T", name, reflect.TypeFor[T](), i+1, args[i])
	}
	return typed, nil
}
