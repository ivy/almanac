package compare

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/buildfailures"
	"github.com/ivy/almanac/almanacs/renovate/report"
	"github.com/ivy/almanac/almanacs/renovate/securityfixes"
	"github.com/ivy/almanac/almanacs/renovate/wiring"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/workflow"
)

// Verbs are compare and compare-effects, reading their settings from env. almanac-renovate
// mounts them beside report.
func Verbs(env wiring.Env) []app.Verb {
	return []app.Verb{StatesVerb(env), EffectsVerb(env)}
}

// StatesVerb is compare: two reports side by side. Without --go, the Go report is made now,
// as the report verb makes it.
func StatesVerb(env wiring.Env) app.Verb {
	return app.Verb{
		Name:  "compare",
		Args:  "--rust FILE [--go FILE]",
		Short: "Print the Rust and Go reports' states side by side, marking every difference.",
		Flags: func(flags *pflag.FlagSet) {
			flags.String("rust", "", "what the Rust almanac-renovate's `report --format json` printed")
			flags.String("go", "", "what the Go one's printed; without it, the Go report is made now")
		},
		Run: func(ctx context.Context, s app.Setting, _ []string) (app.Said, error) {
			rustPath, _ := s.Flags.GetString("rust")
			goPath, _ := s.Flags.GetString("go")
			if rustPath == "" {
				return app.Said{}, diag.MissingFlag("compare", "--rust FILE", "the Rust report, as `report --format json` printed it")
			}
			rust, err := readReportFile(rustPath)
			if err != nil {
				return app.Said{}, err
			}
			var golang Report
			if goPath != "" {
				golang, err = readReportFile(goPath)
			} else {
				golang, err = goReportNow(ctx, env)
			}
			if err != nil {
				return app.Said{}, err
			}
			compared := CompareStates(rust, golang)
			return app.Said{Human: strings.TrimSuffix(RenderStates(compared), "\n"), JSON: compared}, nil
		},
	}
}

func readReportFile(path string) (Report, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return Report{}, diag.UnreadableInput(path, err)
	}
	return ReadReport(path, text)
}

func goReportNow(ctx context.Context, env wiring.Env) (Report, error) {
	found, err := report.FromEnv(ctx, env)
	if err != nil {
		return Report{}, err
	}
	return fromFleet(found)
}

// opener opens the Go almanac's store in its dry run's scope, and says how to close it.
type opener func(ctx context.Context, s app.Setting) (store.Store, func(), error)

// EffectsVerb is compare-effects: for each open Renovate pull request, what the Go version's
// dry run would do beside what the Rust version did. It reads the dry run's store in the
// database DATABASE_URL names.
func EffectsVerb(env wiring.Env) app.Verb {
	return effectsVerb(env, func(ctx context.Context, s app.Setting) (store.Store, func(), error) {
		return s.OpenStore(ctx, effect.DryRun)
	})
}

func effectsVerb(env wiring.Env, open opener) app.Verb {
	return app.Verb{
		Name:  "compare-effects",
		Args:  "[--since DURATION]",
		Short: "Print what the Go dry run would do to each open Renovate pull request beside what the Rust version did.",
		Flags: func(flags *pflag.FlagSet) {
			flags.Duration("since", 0, "compare only Go runs started this long ago or later, such as 72h; without it, every run")
		},
		Run: func(ctx context.Context, s app.Setting, _ []string) (app.Said, error) {
			window, _ := s.Flags.GetDuration("since")
			if window < 0 {
				return app.Said{}, diag.MissingFlag("compare-effects", "--since DURATION", fmt.Sprintf("a duration to look back, which %s is not", window))
			}
			from, closeStore, err := effectSources(ctx, env, open, s)
			if err != nil {
				return app.Said{}, err
			}
			defer closeStore()
			if window > 0 {
				from.Since = time.Now().Add(-window)
			}
			compared, err := CompareEffects(ctx, from)
			if err != nil {
				return app.Said{}, err
			}
			return app.Said{Human: strings.TrimSuffix(RenderEffects(compared), "\n"), JSON: compared}, nil
		},
	}
}

// effectSources sets up what compare-effects reads: GitHub through a client that refuses every
// write, the Go version's workflows, and its dry-run store.
func effectSources(ctx context.Context, env wiring.Env, open opener, s app.Setting) (EffectSources, func(), error) {
	owner, ok := env("ALMANAC_OWNER")
	if !ok || owner == "" {
		return EffectSources{}, nil, diag.MissingEnv("ALMANAC_OWNER", "the account whose pull requests to compare")
	}
	author, ok := env("ALMANAC_RENOVATE_AUTHOR")
	if !ok || author == "" {
		author = renovate.DefaultAuthor
	}
	client, err := wiring.ReadOnlyClient(env)
	if err != nil {
		return EffectSources{}, nil, err
	}
	if client == nil {
		return EffectSources{}, nil, diag.MissingEnv("GITHUB_TOKEN", "a token that can read the repositories, or the GITHUB_APP_ settings")
	}
	workflows, err := Workflows()
	if err != nil {
		return EffectSources{}, nil, err
	}
	st, closeStore, err := open(ctx, s)
	if err != nil {
		return EffectSources{}, nil, err
	}
	return EffectSources{GitHub: client, Store: st, Workflows: workflows, Owner: owner, Author: author}, closeStore, nil
}

// Workflows are the Renovate almanac's workflows, whose dry runs compare-effects reads.
func Workflows() ([]workflow.Registered, error) {
	var out []workflow.Registered
	for _, build := range []func() (workflow.Registered, error){
		func() (workflow.Registered, error) { return buildfailures.New() },
		func() (workflow.Registered, error) { return securityfixes.New() },
	} {
		w, err := build()
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}
