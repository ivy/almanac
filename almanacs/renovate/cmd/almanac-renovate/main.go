// Command almanac-renovate is the Renovate almanac: build_failures and security_fixes, run as a
// daemon or a verb at a time, with the fleet report, the comparison with the Rust version and the
// workflow views beside them.
//
// Its name is the scope its rows are kept under, which compare-effects reads as
// almanac-renovate.dry-run, so it stays almanac-renovate.
//
// Every provider comes from the environment that docs/configuration.md lists, through package
// wiring. A provider that is not configured is left out, so check names what is missing.
package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/buildfailures"
	"github.com/ivy/almanac/almanacs/renovate/compare"
	"github.com/ivy/almanac/almanacs/renovate/report"
	"github.com/ivy/almanac/almanacs/renovate/securityfixes"
	"github.com/ivy/almanac/almanacs/renovate/wiring"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/views"
)

// models are the almanac's workflows. Their kinds do not overlap: build_failures takes check
// suites and statuses, and security_fixes takes pull requests opened and labelled.
var models = []model.Workflow{buildfailures.Model, securityfixes.Model}

func main() {
	app.Almanac{
		Name:       "almanac-renovate",
		Vocabulary: renovate.Vocabulary,
		Models:     models,
		Setup:      setup,
		Verbs:      verbs(),
		Commands:   commands,
	}.Main()
}

// setup makes every provider from the environment. wiring reads ALMANAC_READ_ONLY as the
// runtime does, so a read-only pass gets a GitHub client that refuses writes and no harness.
func setup(context.Context, app.Setting) (*providers.Set, error) {
	return wiring.Providers(os.LookupEnv)
}

// verbs are the almanac's own verbs, listed after the ones every almanac has: the report, and
// compare and compare-effects. Each reads its settings from the environment.
func verbs() []app.Verb {
	return append([]app.Verb{report.Verb(os.LookupEnv)}, compare.Verbs(os.LookupEnv)...)
}

// commands are the verbs with flags of their own: outcomes and views, which draw the models.
func commands() []*cobra.Command {
	return views.Commands(renovate.Vocabulary, models...)
}
