package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/workflow"
)

// runCx is the workflow.Cx of one run. It records each read under the run's ID, a name, and
// how many times the run has reached that name, so a replay of the run returns what was read
// instead of reading again.
type runCx struct {
	rt     *Runtime
	run    store.Run
	may    []effect.Kind
	agents []string

	mu     sync.Mutex
	counts map[string]int
}

func (r *Runtime) newCx(run store.Run, w workflow.Registered) *runCx {
	agents := make([]string, 0, len(w.Agents()))
	for _, a := range w.Agents() {
		agents = append(agents, a.Name)
	}
	return &runCx{rt: r, run: run, may: w.May(), agents: agents, counts: map[string]int{}}
}

// Record implements workflow.Cx. What it returns is what the store holds, so the first run and
// every replay see the same value.
func (c *runCx) Record(ctx context.Context, name string, read func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	c.mu.Lock()
	c.counts[name]++
	occurrence := c.counts[name]
	c.mu.Unlock()

	s := c.rt.store
	if recorded, ok, err := s.Recorded(ctx, c.run.ID, name, occurrence); err != nil || ok {
		return recorded, err
	}
	value, err := read(ctx)
	if err != nil {
		return nil, err
	}
	value, err = store.Storable(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := s.Record(ctx, c.run.ID, name, occurrence, value); err != nil {
		return nil, err
	}
	return value, nil
}

// record is Record for a value of any type, as JSON.
func record[T any](ctx context.Context, c *runCx, name string, read func(context.Context) (T, error)) (T, error) {
	var value T
	raw, err := c.Record(ctx, name, func(ctx context.Context) (json.RawMessage, error) {
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
		return value, fmt.Errorf("%s was recorded as something other than a %T: %w", name, value, err)
	}
	return value, nil
}

// Attempts implements workflow.Cx: Push and Ask traces on the run's key within
// workflow.AttemptWindow.
func (c *runCx) Attempts(ctx context.Context) (workflow.Attempts, error) {
	return record(ctx, c, "attempts", func(ctx context.Context) (workflow.Attempts, error) {
		since := c.rt.clock.Now().Add(-workflow.AttemptWindow)
		traces, err := c.rt.store.Traces(ctx, c.run.Workflow, c.run.Key, since)
		if err != nil {
			return workflow.Attempts{}, err
		}
		var attempts workflow.Attempts
		for _, t := range traces {
			switch t.Kind {
			case effect.KindPush:
				attempts.Used++
			case effect.KindAsk:
				attempts.Asked = true
			}
		}
		return attempts, nil
	})
}

// cooldownCheck is what an age check recorded: the publish time read, and when it was read.
type cooldownCheck struct {
	Published cooled.Published `json:"published"`
	CheckedAt time.Time        `json:"checked_at"`
}

// Age implements workflow.Cx. The proof is not recorded: a replay measures the recorded times
// again, and so makes it again.
func (c *runCx) Age(ctx context.Context, minAge time.Duration, read func(context.Context) (cooled.Published, error)) (cooled.Age, cooled.Cooled, error) {
	check, err := record(ctx, c, "cooldown", func(ctx context.Context) (cooldownCheck, error) {
		published, err := read(ctx)
		if err != nil {
			return cooldownCheck{}, err
		}
		return cooldownCheck{Published: published, CheckedAt: c.rt.clock.Now()}, nil
	})
	if err != nil {
		return 0, cooled.Cooled{}, err
	}
	age, proof := cooled.Measure(check.Published, minAge, check.CheckedAt)
	return age, proof, nil
}

// Hold implements workflow.Cx. Its key is the run's, so a retry of the same run posts the same
// status, and a requeued run posts its final status over it.
func (c *runCx) Hold(ctx context.Context, status string, repo hostile.Slug, sha hostile.Sha) error {
	if !slices.Contains(c.may, effect.KindAttest) {
		return fmt.Errorf("this workflow may not Attest, so it cannot hold %s: declare Attest among what it may do", status)
	}
	key := c.run.ID + ":hold:" + status
	_, err := record(ctx, c, "hold", func(ctx context.Context) (bool, error) {
		if c.rt.mode != effect.Live {
			return true, nil
		}
		forge, err := provided[effect.Forge](c.rt.providers, "Forge")
		if err != nil {
			return false, err
		}
		return true, forge.Attest(ctx, key, effect.Attest{Context: status, State: effect.Pending, Repo: repo, Sha: sha})
	})
	return err
}

// Screen implements workflow.Cx.
func (c *runCx) Screen(ctx context.Context, question string, text hostile.Of[string], above float64) error {
	classifier, ok := providers.Lookup[agent.Classifier](c.rt.providers)
	if !ok {
		return fmt.Errorf("no agent.Classifier is provided, so this workflow cannot screen: provide one where the almanac sets up its providers, and require it so start-up checks it")
	}
	p, err := record(ctx, c, "screen", func(ctx context.Context) (float64, error) {
		return agent.Screen(ctx, classifier, question, text)
	})
	if err != nil {
		return err
	}
	if p > above {
		return nil
	}
	return workflow.Stop("below screen threshold")
}

// Agent implements workflow.Cx. The answer is recorded tagged as hostile.
func (c *runCx) Agent(ctx context.Context, call agent.Call) (hostile.Of[json.RawMessage], error) {
	name := call.Agent()
	if !slices.Contains(c.agents, name) {
		return hostile.Of[json.RawMessage]{}, fmt.Errorf("agent %q is not declared by this workflow, so it is not called: declare it among the workflow's agents, so the manifest shows it", name)
	}
	harness, ok := c.rt.providers.HarnessFor(name)
	if !ok {
		return hostile.Of[json.RawMessage]{}, fmt.Errorf("no harness for agent %q: set one up where the almanac sets up its providers; start-up checks this", name)
	}
	return record(ctx, c, name, func(ctx context.Context) (hostile.Of[json.RawMessage], error) {
		return call.Run(ctx, harness)
	})
}

// Provider implements workflow.Cx.
func (c *runCx) Provider(need providers.Requirement) (any, error) {
	found, ok := c.rt.providers.Get(need)
	if !ok {
		return nil, fmt.Errorf("no provider for %s: provide one where the almanac sets up its providers, and require it so start-up checks it", need.Name())
	}
	return found, nil
}
