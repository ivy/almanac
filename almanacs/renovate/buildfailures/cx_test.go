package buildfailures_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/workflow"
)

// walking is a workflow.Cx for one walk of a model, outside a runtime: it reads through the
// providers in set as a first run does, takes the attempts a spec gives it, and keeps what the
// walk held and which agents it called. It applies nothing.
type walking struct {
	set      *providers.Set
	clock    clock.Clock
	attempts workflow.Attempts

	holds []string
	calls []string
}

func (c *walking) Record(ctx context.Context, _ string, read func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	return read(ctx)
}

func (c *walking) Attempts(context.Context) (workflow.Attempts, error) { return c.attempts, nil }

func (c *walking) Age(ctx context.Context, minAge time.Duration, read func(context.Context) (cooled.Published, error)) (cooled.Age, cooled.Cooled, error) {
	published, err := read(ctx)
	if err != nil {
		return 0, cooled.Cooled{}, err
	}
	age, proof := cooled.Measure(published, minAge, c.clock.Now())
	return age, proof, nil
}

func (c *walking) Hold(_ context.Context, status string, repo hostile.Slug, sha hostile.Sha) error {
	c.holds = append(c.holds, fmt.Sprintf("%s@%s:%s", repo, sha, status))
	return nil
}

func (c *walking) Screen(context.Context, string, hostile.Of[string], float64) error {
	return errors.New("build_failures screens nothing")
}

func (c *walking) Agent(ctx context.Context, call agent.Call) (hostile.Of[json.RawMessage], error) {
	c.calls = append(c.calls, call.Agent())
	harness, ok := c.set.HarnessFor(call.Agent())
	if !ok {
		return hostile.Of[json.RawMessage]{}, fmt.Errorf("no harness for agent %s", call.Agent())
	}
	return call.Run(ctx, harness)
}

func (c *walking) Provider(need providers.Requirement) (any, error) {
	found, ok := c.set.Get(need)
	if !ok {
		return nil, fmt.Errorf("no provider for %s", need.Name())
	}
	return found, nil
}
