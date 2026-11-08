package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// recorder is a Cx that keeps recordings in memory, as the runtime's does in the store. A
// second recorder over the same recordings replays them.
type recorder struct {
	recorded  map[string][]json.RawMessage
	counts    map[string]int
	reads     int
	providers *providers.Set
	answers   map[string]json.RawMessage
	calls     []agent.Call
}

func newRecorder() *recorder {
	return &recorder{recorded: map[string][]json.RawMessage{}, counts: map[string]int{}, providers: providers.New(), answers: map[string]json.RawMessage{}}
}

// replay is a recorder over r's recordings, as a replayed run sees them.
func (r *recorder) replay() *recorder {
	again := newRecorder()
	again.recorded = r.recorded
	return again
}

func (r *recorder) Record(ctx context.Context, name string, read func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	occurrence := r.counts[name]
	r.counts[name]++
	if occurrence < len(r.recorded[name]) {
		return r.recorded[name][occurrence], nil
	}
	r.reads++
	value, err := read(ctx)
	if err != nil {
		return nil, err
	}
	r.recorded[name] = append(r.recorded[name], value)
	return value, nil
}

func (r *recorder) Attempts(context.Context) (workflow.Attempts, error) {
	return workflow.Attempts{}, nil
}

func (r *recorder) Age(context.Context, time.Duration, func(context.Context) (cooled.Published, error)) (cooled.Age, cooled.Cooled, error) {
	return cooled.Unknown, cooled.Cooled{}, nil
}

func (r *recorder) Hold(context.Context, string, hostile.Slug, hostile.Sha) error { return nil }

func (r *recorder) Screen(context.Context, string, hostile.Of[string], float64) error { return nil }

func (r *recorder) Agent(ctx context.Context, call agent.Call) (hostile.Of[json.RawMessage], error) {
	r.calls = append(r.calls, call)
	answer, ok := r.answers[call.Agent()]
	if !ok {
		return hostile.Of[json.RawMessage]{}, fmt.Errorf("agent %s is not declared", call.Agent())
	}
	return call.Run(ctx, scripted(answer))
}

func (r *recorder) Provider(need providers.Requirement) (any, error) {
	found, ok := r.providers.Get(need)
	if !ok {
		return nil, errors.New("no provider for " + need.Name())
	}
	return found, nil
}

// scripted is a harness that answers the same every time.
type scripted json.RawMessage

func (s scripted) Run(context.Context, agent.Pass, string, agent.Grant, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(s), nil
}

// opened is a pull request event, as generated events are written.
type opened struct {
	Number uint64
	Repo   hostile.Slug
	Title  hostile.Of[string]
}

func (opened) Kind() string        { return "github.pull_request.opened" }
func (opened) AlsoKinds() []string { return []string{"github.pull_request.reopened"} }
func (opened) Operation() string   { return "pulls/get" }
func (opened) Projections() []wire.Projection {
	return []wire.Projection{
		{From: "/number", To: "/number"},
		{From: "/base/repo/full_name", To: "/repository/full_name"},
		{From: "/title", To: "/pull_request/title"},
	}
}

func (e *opened) Parse(p *wire.Payload) {
	e.Number = wire.Field[uint64](p, "/number")
	e.Repo = wire.Field[hostile.Slug](p, "/repository/full_name")
	e.Title = p.Text("/pull_request/title")
}

type noTracker struct{}

func (noTracker) File(context.Context, string, effect.Issue) error   { return nil }
func (noTracker) Ask(context.Context, string, effect.Question) error { return nil }

func providersWith(tracker effect.Tracker) *providers.Set {
	return providers.Provide(providers.New(), tracker)
}
