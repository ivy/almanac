package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/workflow"
)

// recorded is a workflow.Cx over a run that was already recorded, as a replay sees it. Every
// read must have been recorded: one that was not fails, and no provider is reachable, so a walk
// over it shows a model reaching its ending from recorded facts alone.
type recorded struct {
	records   map[string][]json.RawMessage
	counts    map[string]int
	attempts  workflow.Attempts
	published cooled.Published
	checkedAt time.Time
	screened  float64
	answers   map[string]string
	// live reads what was not recorded, and records it, as a first run does.
	live bool
	// provided are the providers a live run reaches, by name.
	provided map[string]any

	reads     []string
	holds     []string
	screens   []string
	calls     []agent.Call
	inputs    []json.RawMessage
	providers []string
}

func newRecorded() *recorded {
	return &recorded{records: map[string][]json.RawMessage{}, counts: map[string]int{}, answers: map[string]string{}, checkedAt: now}
}

// now is when every recorded run checked its cooldown.
var now = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

// record keeps value as the next recording under name, as JSON.
func (r *recorded) record(name string, value any) *recorded {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	r.records[name] = append(r.records[name], raw)
	return r
}

func (r *recorded) Record(ctx context.Context, name string, read func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	occurrence := r.counts[name]
	r.counts[name]++
	if occurrence < len(r.records[name]) {
		return r.records[name][occurrence], nil
	}
	r.reads = append(r.reads, name)
	if !r.live {
		return nil, fmt.Errorf("nothing was recorded under %s #%d", name, occurrence+1)
	}
	value, err := read(ctx)
	if err != nil {
		return nil, err
	}
	r.records[name] = append(r.records[name], value)
	return value, nil
}

func (r *recorded) Attempts(context.Context) (workflow.Attempts, error) { return r.attempts, nil }

func (r *recorded) Age(_ context.Context, minAge time.Duration, _ func(context.Context) (cooled.Published, error)) (cooled.Age, cooled.Cooled, error) {
	age, proof := cooled.Measure(r.published, minAge, r.checkedAt)
	return age, proof, nil
}

func (r *recorded) Hold(_ context.Context, status string, repo hostile.Slug, sha hostile.Sha) error {
	r.holds = append(r.holds, fmt.Sprintf("%s@%s:%s", repo, sha, status))
	return nil
}

func (r *recorded) Screen(_ context.Context, question string, _ hostile.Of[string], above float64) error {
	r.screens = append(r.screens, question)
	if r.screened > above {
		return nil
	}
	return workflow.Stop("below screen threshold")
}

func (r *recorded) Agent(ctx context.Context, call agent.Call) (hostile.Of[json.RawMessage], error) {
	r.calls = append(r.calls, call)
	answer, ok := r.answers[call.Agent()]
	if !ok {
		return hostile.Of[json.RawMessage]{}, fmt.Errorf("no answer was recorded for agent %s", call.Agent())
	}
	return call.Run(ctx, harnessFunc(func(input json.RawMessage) (json.RawMessage, error) {
		r.inputs = append(r.inputs, input)
		return json.RawMessage(answer), nil
	}))
}

func (r *recorded) Provider(need providers.Requirement) (any, error) {
	r.providers = append(r.providers, need.Name())
	if found, ok := r.provided[need.Name()]; ok && r.live {
		return found, nil
	}
	return nil, errors.New("a recorded run reaches no provider")
}

// harnessFunc is a harness that answers with a function of the input it is given.
type harnessFunc func(input json.RawMessage) (json.RawMessage, error)

func (h harnessFunc) Run(_ context.Context, _ agent.Pass, _ string, _ agent.Grant, input json.RawMessage) (json.RawMessage, error) {
	return h(input)
}
