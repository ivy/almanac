// Package engine runs a workflow model as a workflow.Registered, so the runtime runs a model
// exactly as it runs any workflow and never knows it is one.
//
// [New] checks a model against its vocabulary with package check, then resolves every part it
// names once, before any event arrives. A run walks the resolved steps in order and reaches
// only those parts: a name in the model is never looked up while a run is under way, so an
// observer, agent or effect the model does not declare cannot be reached. An observer or
// publish-time source reaches only the providers its registration says it needs.
//
// Every read a walk makes goes through the run's workflow.Cx, which records it by name and
// occurrence: the model's digest under "model", an observation under its fact's name, and an
// age, attempts count, hold, screen and agent answer under the names Cx gives them. Checking a patch is deterministic and is not
// recorded. A replay therefore walks the same steps to the same ending, with the same effects,
// without calling a provider.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/check"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/workflow"
)

// Workflow is a model, checked and resolved against its vocabulary, run as a
// workflow.Registered. It holds its own copy of the model, so changing the model it was made
// from changes nothing it runs.
type Workflow struct {
	model    model.Workflow
	manifest workflow.Manifest
	plan     plan
}

var _ workflow.Registered = (*Workflow)(nil)

// New makes w runnable against v. It refuses a model package check refuses, with the same
// diag.Problems, and a model whose own reads would share a name with what Cx records itself.
func New(v *vocab.Vocabulary, w model.Workflow) (*Workflow, error) {
	w = w.Clone()
	if err := check.Check(v, w); err != nil {
		return nil, err
	}
	manifest, err := check.Manifest(v, w)
	if err != nil {
		return nil, err
	}
	resolved, err := resolve(v, w, manifest)
	if err != nil {
		return nil, err
	}
	return &Workflow{model: w, manifest: manifest, plan: resolved}, nil
}

// Model is a copy of the model the workflow runs.
func (e *Workflow) Model() model.Workflow { return e.model.Clone() }

// Name implements workflow.Registered.
func (e *Workflow) Name() string { return e.model.Name }

// Intent implements workflow.Registered.
func (e *Workflow) Intent() string { return e.model.Intent }

// Kind implements workflow.Registered: the first kind of delivery the model's event is parsed
// from.
func (e *Workflow) Kind() string { return e.plan.event.Kinds[0] }

// Handles implements workflow.Registered: every kind of delivery the model's event is parsed
// from.
func (e *Workflow) Handles(kind string) bool { return slices.Contains(e.plan.event.Kinds, kind) }

// RunsPerHour implements workflow.Registered.
func (e *Workflow) RunsPerHour() int { return e.model.Limits.RunsPerHour }

// May implements workflow.Registered.
func (e *Workflow) May() []effect.Kind { return slices.Clone(e.model.May) }

// Agents implements workflow.Registered: the agent of every agent step, as the manifest shows
// them.
func (e *Workflow) Agents() []agent.Info { return slices.Clone(e.manifest.Agents) }

// Requirements implements workflow.Registered: every provider the parts the model uses need.
func (e *Workflow) Requirements() []providers.Requirement {
	return slices.Clone(e.manifest.Requires)
}

// Manifest implements workflow.Registered. Its digest names the version of the model.
func (e *Workflow) Manifest() workflow.Manifest {
	m := e.manifest
	m.May = slices.Clone(m.May)
	m.Agents = slices.Clone(m.Agents)
	m.Requires = slices.Clone(m.Requires)
	return m
}

// Identify implements workflow.Registered. It parses the payload as the model's event, then
// works out the key, the subjects and whether the event is wanted from the event alone.
func (e *Workflow) Identify(payload json.RawMessage) (workflow.Identity, error) {
	event, err := e.plan.event.Parse(payload)
	if err != nil {
		return workflow.Identity{}, err
	}
	known := facts{model.EventFact: event}
	key, err := e.key(known)
	if err != nil {
		return workflow.Identity{}, err
	}
	identity := workflow.Identity{Key: key, Wanted: true}
	before := &walk{e: e, facts: known}
	if identity.Subjects, err = before.subjects(e.model.Subjects, false); err != nil {
		return workflow.Identity{}, e.at("subjects", err)
	}
	if e.plan.when != nil {
		if identity.Wanted, err = before.holds(context.Background(), *e.plan.when); err != nil {
			return workflow.Identity{}, e.at("when", err)
		}
	}
	return identity, nil
}

// Scan implements workflow.Registered with the model's scan, which finds events of the
// workflow's kind. A model with no scan finds nothing.
func (e *Workflow) Scan(ctx context.Context, scanner *workflow.Scanner) ([]workflow.Found, error) {
	if e.plan.scan == nil {
		return nil, nil
	}
	found, err := e.plan.scan.Scan(ctx, scanner)
	if err != nil {
		return nil, err
	}
	for i := range found {
		found[i].Kind = e.Kind()
	}
	return found, nil
}

// at names where in the workflow err happened.
func (e *Workflow) at(where string, err error) error {
	return fmt.Errorf("%s, %s: %w", e.model.Name, where, err)
}
