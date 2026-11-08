// Package agent declares the agents a workflow may call. An agent's profile is part of its
// type and fixes what it may do (R-19, principle 5):
//
//   - [Quarantined] reads hostile text. It may not write, reach the network or hold secrets,
//     and what it answers stays hostile.
//   - [Fixer] repairs from hostile text, and only once the update has cooled. It may write
//     inside a sandbox, but holds no secrets and has no network, and what it answers stays
//     hostile.
//   - [Worker] can change things, so it takes only clean input, and its answer is read only
//     into a clean type.
//
// A sandbox beneath the [Harness] enforces the [Grant]: the type is the declaration, not the
// enforcement. A workflow makes a [Call] from an agent, and the run's context makes the call,
// so every answer is recorded.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
)

// Grant is what a session is given. The sandbox beneath the harness must enforce it.
type Grant struct {
	Write   bool `json:"write"`
	Egress  bool `json:"egress"`
	Secrets bool `json:"secrets"`
}

var (
	quarantinedGrant = Grant{}
	fixerGrant       = Grant{Write: true}
	workerGrant      = Grant{Write: true, Egress: true, Secrets: true}
)

// Info is how an agent appears in the manifest, so a change of capability shows in review.
type Info struct {
	Name       string   `json:"name"`
	Profile    string   `json:"profile"`
	Grant      Grant    `json:"grant"`
	Prompt     string   `json:"prompt"`
	PromptVars []string `json:"prompt_vars"`
}

type declared struct {
	name   string
	prompt Prompt
}

// Name is the agent's name. A run's record of its answers is kept under it.
func (d declared) Name() string { return d.name }

func (d declared) info(profile string, grant Grant) Info {
	return Info{Name: d.name, Profile: profile, Grant: grant, Prompt: d.prompt.Name(), PromptVars: d.prompt.Vars()}
}

func (d declared) call(grant Grant, vars Vars, input any) (Call, error) {
	if d.name == "" {
		return Call{}, errors.New("agent: an agent must be declared with NewQuarantined, NewFixer or NewWorker")
	}
	prompt, err := d.prompt.Render(vars)
	if err != nil {
		return Call{}, fmt.Errorf("agent %s: %w", d.name, err)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return Call{}, fmt.Errorf("agent %s: its input has no JSON form: %w", d.name, err)
	}
	return Call{agent: d.name, grant: grant, prompt: prompt, input: encoded}, nil
}

// Quarantined is an agent that reads hostile text and holds the least power.
type Quarantined struct{ declared }

// NewQuarantined declares a quarantined agent.
func NewQuarantined(name string, prompt Prompt) Quarantined {
	return Quarantined{declared{name: name, prompt: prompt}}
}

func (a Quarantined) Info() Info { return a.info("quarantined", quarantinedGrant) }

// Read asks the agent about input, which may be hostile. Its answer is hostile.
func (a Quarantined) Read(vars Vars, input any) (Call, error) {
	return a.call(quarantinedGrant, vars, input)
}

// Fixer is an agent that repairs from hostile input inside a sandbox.
type Fixer struct{ declared }

// NewFixer declares a fixer.
func NewFixer(name string, prompt Prompt) Fixer {
	return Fixer{declared{name: name, prompt: prompt}}
}

func (a Fixer) Info() Info { return a.info("fixer", fixerGrant) }

// Repair asks the agent to repair from input, which may be hostile. No agent reads an update
// until it has cooled, so this takes the proof, and refuses a zero one. Its answer is hostile.
func (a Fixer) Repair(vars Vars, input any, proof cooled.Cooled) (Call, error) {
	if proof.IsZero() {
		return Call{}, fmt.Errorf("agent %s is a fixer, which reads an update only after it has cooled: measure the release's age and pass the proof it gives", a.name)
	}
	return a.call(fixerGrant, vars, input)
}

// Worker is an agent that can change things, so it reads nothing hostile.
type Worker struct{ declared }

// NewWorker declares a worker.
func NewWorker(name string, prompt Prompt) Worker {
	return Worker{declared{name: name, prompt: prompt}}
}

func (a Worker) Info() Info { return a.info("worker", workerGrant) }

// Act asks the agent to act on input, which must be clean ([hostile.CheckClean]). Read its
// answer with hostile.DecodeClean.
func (a Worker) Act(vars Vars, input any) (Call, error) {
	if input != nil {
		if err := hostile.CheckClean(reflect.TypeOf(input)); err != nil {
			return Call{}, fmt.Errorf("agent %s is a worker, which takes only clean input: %w", a.name, err)
		}
	}
	return a.call(workerGrant, vars, input)
}

// Call is one call to an agent, ready to run: its rendered prompt, its grant and its input.
// Only an agent's Read, Repair or Act makes one, after checking what its profile requires.
type Call struct {
	agent  string
	grant  Grant
	prompt string
	input  json.RawMessage
}

// Agent is the name of the agent called.
func (c Call) Agent() string { return c.agent }

// Grant is what the session is given.
func (c Call) Grant() Grant { return c.grant }

// IsZero reports whether c was not made by an agent.
func (c Call) IsZero() bool { return c.agent == "" }

// Run makes the call through h. The answer is hostile whatever the profile, because a model
// can repeat what it read. Only the run's context should run a call, so that the answer is
// recorded and a replay does not call again.
func (c Call) Run(ctx context.Context, h Harness) (hostile.Of[json.RawMessage], error) {
	if c.IsZero() {
		return hostile.Of[json.RawMessage]{}, errors.New("agent: a call must be made by an agent's Read, Repair or Act")
	}
	answer, err := h.Run(ctx, Pass{issued: true}, c.prompt, c.grant, c.input)
	if err != nil {
		return hostile.Of[json.RawMessage]{}, err
	}
	return hostile.New(answer), nil
}
