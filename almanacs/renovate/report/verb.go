package report

import (
	"context"
	"time"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/wiring"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/diag"
)

// Verb is the report verb, reading its settings from env. It always reads GitHub through a
// client that refuses every write, because a report has no reason to write, whatever
// ALMANAC_READ_ONLY says.
func Verb(env wiring.Env) app.Verb {
	return app.Verb{
		Name:  "report",
		Short: "Print where every open Renovate pull request stands, and why.",
		Run: func(ctx context.Context, _ app.Setting, _ []string) (app.Said, error) {
			return run(ctx, env)
		},
	}
}

func run(ctx context.Context, env wiring.Env) (app.Said, error) {
	owner, ok := env("ALMANAC_OWNER")
	if !ok || owner == "" {
		return app.Said{}, diag.MissingEnv("ALMANAC_OWNER", "the account whose repositories to report on")
	}
	author, ok := env("ALMANAC_RENOVATE_AUTHOR")
	if !ok || author == "" {
		author = renovate.DefaultAuthor
	}
	client, err := wiring.ReadOnlyClient(env)
	if err != nil {
		return app.Said{}, err
	}
	if client == nil {
		return app.Said{}, diag.MissingEnv("GITHUB_TOKEN", "a token that can read the repositories, or the GITHUB_APP_ settings")
	}
	found, err := Read(ctx, Sources{GitHub: client, Registry: wiring.Registry(env), Now: time.Now()}, owner, author)
	if err != nil {
		return app.Said{}, err
	}
	return app.Said{Human: Render(found), JSON: found}, nil
}
