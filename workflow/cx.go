package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
)

// Cx is a run's context, which the runtime implements. Every outside read, model call and
// check of time goes through it and is recorded under a name and its occurrence in the run, so
// that a branch taken in one place cannot shift another's result. A replay returns what was
// recorded instead of doing the work again.
//
// The methods take and return data, so that the engine can call them from a model. A workflow
// written in Go calls the generic functions in this package, which type what Cx records:
// [Observe], [RecordFact], [Read], [Repair], [Act] and [Get].
type Cx interface {
	// Record returns what read returns, recorded under name. The first time a run reaches
	// name for the nth time, read runs and its result is recorded; a replay returns the nth
	// recording under name and does not call read. A failed read is not recorded. A value
	// round-trips through JSON even the first time, so a replay sees exactly what the first
	// run saw, and hostile values keep their tag.
	//
	// Cx records under "cooldown", "attempts", "hold", "screen" and each agent's name itself,
	// so a workflow's own reads take other names.
	Record(ctx context.Context, name string, read func(context.Context) (json.RawMessage, error)) (json.RawMessage, error)

	// Attempts is how many patches this run's key has had within AttemptWindow, and whether a
	// person was already asked about it. Recorded under "attempts".
	Attempts(ctx context.Context) (Attempts, error)

	// Age reads a release's publish time with read and measures it against minAge at the
	// clock's time now. Both times are recorded under "cooldown", so a replay reaches the
	// same answer. The proof is not zero only when the age is cooled.Passed.
	Age(ctx context.Context, minAge time.Duration, read func(context.Context) (cooled.Published, error)) (cooled.Age, cooled.Cooled, error)

	// Hold sets a pending status named status on sha now, before an agent starts, so nothing
	// can merge while the work is under way. Effects apply only when a run ends, which is too
	// late for this. It applies in live mode only, needs the workflow to declare it may
	// Attest, and is recorded under "hold". If the run dies, its requeued run posts the final
	// status over this one.
	Hold(ctx context.Context, status string, repo hostile.Slug, sha hostile.Sha) error

	// Screen asks the classifier how likely the answer to question is yes, for text, and
	// returns a *Halt unless it is above above. Recorded under "screen". It gates spending and
	// never safety, because hostile text can move a classifier.
	Screen(ctx context.Context, question string, text hostile.Of[string], above float64) error

	// Agent runs a call through the harness for its agent, recorded under the agent's name.
	// The agent must be one the workflow declared. The answer is hostile whatever the
	// profile; a Worker's is read with hostile.DecodeClean.
	Agent(ctx context.Context, call agent.Call) (hostile.Of[json.RawMessage], error)

	// Provider is the provider need names, set up in the binary. Reading through it is not
	// recorded: wrap the read in Record.
	Provider(need providers.Requirement) (any, error)
}

// AttemptWindow is how long a patch counts against a key. A branch is reused for later
// versions of a package, so old attempts age out.
const AttemptWindow = 14 * 24 * time.Hour

// Attempts is what has already been tried on a key. A cap on attempts is separate from chain
// depth: it decides when to stop and ask.
type Attempts struct {
	// Used is how many patches the key has had within AttemptWindow.
	Used int `json:"used"`
	// Asked is whether a person was already asked.
	Asked bool `json:"asked"`
}

// Lookup is something that holds providers: a [Cx] or a [Scanner].
type Lookup interface {
	Provider(need providers.Requirement) (any, error)
}

// Get is the provider of T.
func Get[T any](from Lookup) (T, error) {
	var zero T
	found, err := from.Provider(providers.Require[T]())
	if err != nil {
		return zero, err
	}
	typed, ok := found.(T)
	if !ok {
		return zero, fmt.Errorf("the provider of %s is a %T: provide it as a %[1]s", providers.Require[T]().Name(), found)
	}
	return typed, nil
}

// Observe reads from outside, through cx, recorded under name. T must keep observed text
// hostile ([hostile.CheckObserved]): a plain string is refused before read runs.
func Observe[T any](ctx context.Context, cx Cx, name string, read func(context.Context) (T, error)) (T, error) {
	if err := hostile.CheckObserved(reflect.TypeFor[T]()); err != nil {
		var zero T
		return zero, fmt.Errorf("observe %s: %w", name, err)
	}
	return record(ctx, cx, name, read)
}

// RecordFact records a clean value worked out from something hostile, such as a check that
// answers yes or no. T must be clean ([hostile.CheckClean]).
func RecordFact[T any](ctx context.Context, cx Cx, name string, read func(context.Context) (T, error)) (T, error) {
	if err := hostile.CheckClean(reflect.TypeFor[T]()); err != nil {
		var zero T
		return zero, fmt.Errorf("record %s: %w", name, err)
	}
	return record(ctx, cx, name, read)
}

func record[T any](ctx context.Context, cx Cx, name string, read func(context.Context) (T, error)) (T, error) {
	var value T
	raw, err := cx.Record(ctx, name, func(ctx context.Context) (json.RawMessage, error) {
		read, err := read(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(read)
	})
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("%s was recorded as something other than a %T: the workflow changed what it reads, so replay this run with the version that recorded it: %w", name, value, err)
	}
	return value, nil
}

// Read asks a quarantined agent about input, through cx. Its answer stays hostile, as an O.
func Read[O any](ctx context.Context, cx Cx, a agent.Quarantined, vars agent.Vars, input any) (hostile.Of[O], error) {
	call, err := a.Read(vars, input)
	if err != nil {
		return hostile.Of[O]{}, err
	}
	return answer[O](ctx, cx, call)
}

// Repair asks a fixer to repair from input, through cx, once the update has cooled. Its
// answer stays hostile, as an O: usually a patch.Patch, for patch.Policy.Check.
func Repair[O any](ctx context.Context, cx Cx, a agent.Fixer, vars agent.Vars, input any, proof cooled.Cooled) (hostile.Of[O], error) {
	call, err := a.Repair(vars, input, proof)
	if err != nil {
		return hostile.Of[O]{}, err
	}
	return answer[O](ctx, cx, call)
}

// Act asks a worker to act on clean input, through cx. Its answer is read into O, which must
// be clean ([hostile.CheckClean]).
func Act[O any](ctx context.Context, cx Cx, a agent.Worker, vars agent.Vars, input any) (O, error) {
	var out O
	call, err := a.Act(vars, input)
	if err != nil {
		return out, err
	}
	raw, err := cx.Agent(ctx, call)
	if err != nil {
		return out, err
	}
	if err := hostile.DecodeClean(raw, &out); err != nil {
		return out, fmt.Errorf("agent %s: %w", a.Name(), err)
	}
	return out, nil
}

func answer[O any](ctx context.Context, cx Cx, call agent.Call) (hostile.Of[O], error) {
	raw, err := cx.Agent(ctx, call)
	if err != nil {
		return hostile.Of[O]{}, err
	}
	decoded, err := hostile.Decode[O](raw)
	if err != nil {
		return hostile.Of[O]{}, fmt.Errorf("agent %s: %w", call.Agent(), err)
	}
	return decoded, nil
}
