// Command almanac-demo is an almanac binary over the sample vocabulary and its two sample
// workflow models, with fakes for everything outside. The app package's specs drive it as a
// process, to see what an operator sees: exit codes, signals and systemd readiness.
//
// DEMO_REGISTRY sets the CI state the fake registry reports, green, red or pending. Unset, no
// registry is provided, so check and run name it as missing.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/fixture/sample"
	"github.com/ivy/almanac/internal/fixture/workflows"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
)

func main() {
	app.Almanac{
		Name:       "almanac-demo",
		Vocabulary: sample.Vocabulary,
		Models:     []model.Workflow{workflows.DependencyUpdates, workflows.ErrorTriage},
		Setup:      setup,
		Verbs: []app.Verb{{
			Name:  "hello",
			Args:  "[NAME]",
			Short: "Greet someone, to show an almanac's own verb.",
			Run: func(_ context.Context, s app.Setting, args []string) (app.Said, error) {
				who := "world"
				if len(args) > 0 {
					who = args[0]
				}
				return app.Said{
					Human: fmt.Sprintf("hello, %s, from %s", who, s.Name),
					JSON:  map[string]string{"hello": who, "from": s.Name},
				}, nil
			},
		}},
	}.Main()
}

// setup provides fakes: a forge and tracker that keep what they are given, agents that always
// answer the same, and a registry when DEMO_REGISTRY names a CI state.
func setup(context.Context, app.Setting) (*providers.Set, error) {
	set := providers.New()
	providers.Provide[effect.Forge](set, &almanactest.FakeForge{})
	providers.Provide[effect.Tracker](set, &almanactest.FakeTracker{})
	providers.Provide[agent.Classifier](set, almanactest.Answering(0.9))
	set.UseHarnessFor("reviewer", almanactest.Replying(`{"verdict": "no_concerns", "notes": "Looks fine."}`))
	set.UseHarnessFor("investigator", almanactest.Replying(`{"confidence": "high", "summary": "A nil map."}`))
	states := map[string]sample.CIState{"green": sample.CIGreen, "red": sample.CIRed, "pending": sample.CIPending}
	if state, ok := states[os.Getenv("DEMO_REGISTRY")]; ok {
		providers.Provide[sample.Registry](set, registry{ci: state})
	}
	return set, nil
}

// registry reports a trusted update to lodash, with CI in one state.
type registry struct{ ci sample.CIState }

func (r registry) Evidence(_ context.Context, opened sample.OpenedEvent) (sample.UpdateEvidence, error) {
	return sample.UpdateEvidence{
		Repo:               opened.Repo,
		Sha:                opened.Sha,
		Package:            hostile.MustParseSlug("lodash"),
		ProvenanceVerified: true,
		CI:                 r.ci,
		Diff:               hostile.New("- 4.17.20\n+ 4.17.21"),
	}, nil
}
