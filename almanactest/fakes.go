// Package almanactest holds fakes, so a workflow and the runtime are testable with no model,
// service or network, and the runtime's conformance scenarios, which any store.Store can run
// under (see [Conformance]). For time, use clock.Fake.
package almanactest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/effect"
)

// ScriptedHarness is an agent that answers the same every time, and keeps what it was given.
type ScriptedHarness struct {
	mu      sync.Mutex
	reply   json.RawMessage
	grants  []agent.Grant
	inputs  []json.RawMessage
	prompts []string
	gate    chan struct{}
}

// Replying is a harness whose agent answers reply, which must be JSON.
func Replying(reply string) *ScriptedHarness {
	h := &ScriptedHarness{}
	h.Reply(reply)
	return h
}

// Reply changes the agent's answer.
func (h *ScriptedHarness) Reply(reply string) {
	if !json.Valid([]byte(reply)) {
		panic(fmt.Sprintf("almanactest: a scripted reply must be JSON, and %q is not", reply))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reply = json.RawMessage(reply)
}

// Gated makes each session wait, once it has been counted, until Release lets one through, so
// a spec can act while a run is under way.
func (h *ScriptedHarness) Gated() *ScriptedHarness {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gate = make(chan struct{})
	return h
}

// Release lets one waiting session finish. It blocks until one is waiting.
func (h *ScriptedHarness) Release() { h.gate <- struct{}{} }

// Run implements agent.Harness. A session waiting at the gate ends when ctx does.
func (h *ScriptedHarness) Run(ctx context.Context, _ agent.Pass, prompt string, grant agent.Grant, input json.RawMessage) (json.RawMessage, error) {
	h.mu.Lock()
	h.prompts = append(h.prompts, prompt)
	h.grants = append(h.grants, grant)
	h.inputs = append(h.inputs, slices.Clone(input))
	gate, reply := h.gate, h.reply
	h.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return reply, nil
}

// Calls is how many sessions were started.
func (h *ScriptedHarness) Calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.inputs)
}

// Inputs are each session's input as sent, so a spec can check hostile values are still tagged
// where they leave Almanac.
func (h *ScriptedHarness) Inputs() []json.RawMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.inputs)
}

// Grants are what each session was given.
func (h *ScriptedHarness) Grants() []agent.Grant {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.grants)
}

// Prompts are each session's instructions, as rendered.
func (h *ScriptedHarness) Prompts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.prompts)
}

// FixedClassifier answers every question with the same probability.
type FixedClassifier struct {
	probability float64
	mu          sync.Mutex
	calls       int
}

// Answering is a classifier that answers p.
func Answering(p float64) *FixedClassifier { return &FixedClassifier{probability: p} }

// YesProbability implements agent.Classifier.
func (c *FixedClassifier) YesProbability(context.Context, agent.Pass, string, json.RawMessage) (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.probability, nil
}

// Calls is how many questions it was asked.
func (c *FixedClassifier) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// Status is a status a forge holds: which commit and context it is on, and its state.
type Status struct {
	// On is "{repo}@{sha}:{context}".
	On    string
	State effect.AttestState
}

// Pushed is a commit a forge accepted, under the key it was pushed with.
type Pushed struct {
	Key    string
	Commit effect.Commit
}

// FakeForge keeps statuses and pushes, and applies a key only once, as a real forge's API
// does.
type FakeForge struct {
	mu       sync.Mutex
	calls    []string
	statuses []Status
	pushes   []Pushed
	failNext int
}

// FailNext makes the next n calls fail, as a forge that is down does.
func (f *FakeForge) FailNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = n
}

var errForgeDown = errors.New("almanactest: the forge is down")

// Attest implements effect.Forge. A status replaces the one on the same commit and context.
func (f *FakeForge) Attest(_ context.Context, key string, status effect.Attest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	if f.failNext > 0 {
		f.failNext--
		return errForgeDown
	}
	on := fmt.Sprintf("%s@%s:%s", status.Repo, status.Sha, status.Context)
	f.statuses = slices.DeleteFunc(f.statuses, func(s Status) bool { return s.On == on })
	f.statuses = append(f.statuses, Status{On: on, State: status.State})
	return nil
}

// Push implements effect.Forge. A key already pushed changes nothing.
func (f *FakeForge) Push(_ context.Context, key string, commit effect.Commit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	if f.failNext > 0 {
		f.failNext--
		return errForgeDown
	}
	if !slices.ContainsFunc(f.pushes, func(p Pushed) bool { return p.Key == key }) {
		f.pushes = append(f.pushes, Pushed{Key: key, Commit: commit})
	}
	return nil
}

// Calls are the keys of every call, in order, including those that failed.
func (f *FakeForge) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Statuses are the statuses the forge holds, latest last.
func (f *FakeForge) Statuses() []Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.statuses)
}

// Pushes are the commits the forge accepted, in order.
func (f *FakeForge) Pushes() []Pushed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.pushes)
}

// Filed is work or a question a tracker holds. Bodies are left out, because they stay hostile.
type Filed struct {
	Key    string
	Target string
	// Title is the issue's title, or the question asked.
	Title string
}

// FakeTracker keeps filed work and questions, and applies a key only once.
type FakeTracker struct {
	mu    sync.Mutex
	calls []string
	filed []Filed
}

// File implements effect.Tracker.
func (t *FakeTracker) File(_ context.Context, key string, issue effect.Issue) error {
	t.keep(Filed{Key: key, Target: issue.Target.String(), Title: issue.Title})
	return nil
}

// Ask implements effect.Tracker.
func (t *FakeTracker) Ask(_ context.Context, key string, question effect.Question) error {
	t.keep(Filed{Key: key, Target: question.Target.String(), Title: question.Question})
	return nil
}

func (t *FakeTracker) keep(filed Filed) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, filed.Key)
	if !slices.ContainsFunc(t.filed, func(f Filed) bool { return f.Key == filed.Key }) {
		t.filed = append(t.filed, filed)
	}
}

// Calls are the keys of every call, in order.
func (t *FakeTracker) Calls() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.calls)
}

// Filed is what the tracker holds, in order.
func (t *FakeTracker) Filed() []Filed {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.filed)
}

// FakeSweep counts sweeps, and fails them when told to.
type FakeSweep struct {
	mu    sync.Mutex
	calls int
	fails bool
}

// Failing is a sweep that always fails.
func Failing() *FakeSweep { return &FakeSweep{fails: true} }

var errSweep = errors.New("almanactest: the sweep failed")

// Sweep implements effect.Sweep.
func (s *FakeSweep) Sweep(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fails {
		return 0, errSweep
	}
	return 0, nil
}

// Calls is how many sweeps were asked for.
func (s *FakeSweep) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
