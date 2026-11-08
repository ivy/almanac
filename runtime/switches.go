package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/workflow"
)

// Switches are how far an almanac may reach: whether its effects apply, and whether it may
// write or call a model at all.
type Switches struct {
	Mode     effect.Mode
	ReadOnly bool
}

// SwitchesFrom reads the switches for one invocation. Going live is deliberate: it needs
// ALMANAC_MODE=live in the environment, and has no config key, because config only tightens.
// dryRun, the --dry-run flag, always wins over it. ALMANAC_READ_ONLY=1 makes a read-only dry
// run, whatever else is set. A typo can only make an almanac do less: any mode but "live" is a
// dry run, and any ALMANAC_READ_ONLY but unset, empty or "0" is read-only.
func SwitchesFrom(dryRun bool, lookupEnv func(string) (string, bool)) Switches {
	mode, _ := lookupEnv("ALMANAC_MODE")
	readOnly, _ := lookupEnv("ALMANAC_READ_ONLY")
	s := Switches{Mode: effect.DryRun, ReadOnly: readOnly != "" && readOnly != "0"}
	if !dryRun && !s.ReadOnly && mode == "live" {
		s.Mode = effect.Live
	}
	return s
}

// refusingWrites is set with every write and model call it holds refused: a Forge, Tracker or
// Sweep, a Classifier, and the harness of every agent the workflows declare, whether or not
// set had one. Everything else is as set has it.
func refusingWrites(set *providers.Set, workflows []workflow.Registered) *providers.Set {
	refused := set.Clone()
	if _, ok := providers.Lookup[effect.Forge](set); ok {
		providers.Provide[effect.Forge](refused, refusedForge{})
	}
	if _, ok := providers.Lookup[effect.Tracker](set); ok {
		providers.Provide[effect.Tracker](refused, refusedTracker{})
	}
	if _, ok := providers.Lookup[effect.Sweep](set); ok {
		providers.Provide[effect.Sweep](refused, refusedSweep{})
	}
	if _, ok := providers.Lookup[agent.Classifier](set); ok {
		providers.Provide[agent.Classifier](refused, refusedModel{})
	}
	refused.UseHarness(refusedModel{})
	for _, w := range workflows {
		for _, a := range w.Agents() {
			refused.UseHarnessFor(a.Name, refusedModel{})
		}
	}
	return refused
}

type refusedForge struct{}

func (refusedForge) Attest(context.Context, string, effect.Attest) error {
	return diag.ReadOnly("posting a status")
}

func (refusedForge) Push(context.Context, string, effect.Commit) error {
	return diag.ReadOnly("pushing a commit")
}

type refusedTracker struct{}

func (refusedTracker) File(context.Context, string, effect.Issue) error {
	return diag.ReadOnly("filing an issue")
}

func (refusedTracker) Ask(context.Context, string, effect.Question) error {
	return diag.ReadOnly("asking a person")
}

type refusedSweep struct{}

func (refusedSweep) Sweep(context.Context) (int, error) {
	return 0, diag.ReadOnly("asking for failed deliveries to be sent again")
}

// refusedModel stands in for every model when nothing may be spent, so a run records how far
// it got.
type refusedModel struct{}

var errNoModel = errors.New("read-only: a model would be called here, and ALMANAC_READ_ONLY=1 allows no model call, so the run stops here, recording how far it got; unset ALMANAC_READ_ONLY to let it go further")

func (refusedModel) Run(context.Context, agent.Pass, string, agent.Grant, json.RawMessage) (json.RawMessage, error) {
	return nil, errNoModel
}

func (refusedModel) YesProbability(context.Context, agent.Pass, string, json.RawMessage) (float64, error) {
	return 0, errNoModel
}
