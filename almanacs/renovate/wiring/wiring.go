// Package wiring sets up everything the Renovate almanac talks to, once, from the environment
// that docs/configuration.md lists. A provider that is not configured is left out, so that
// validating the providers names what is missing.
package wiring

import (
	"strconv"

	"github.com/ivy/almanac/advisory"
	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/harness/claude"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/registry"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/sandbox"
	"github.com/ivy/almanac/sandbox/podman"
)

const (
	defaultAPI          = "https://api.github.com"
	defaultAdvisories   = "https://cve.now"
	defaultSandboxImage = "localhost/almanac-fixer:latest"
	// model is the model repairs were evaluated on, so a subscription's default cannot change
	// it.
	model = "sonnet"
	// maxSessionUSD stops a runaway session. It is a ceiling, not a target.
	maxSessionUSD = 6.0
)

// Env reads one variable, as os.LookupEnv does.
type Env func(name string) (string, bool)

// get is the variable's value, or fallback when it is unset or empty.
func (env Env) get(name, fallback string) string {
	if value, ok := env(name); ok && value != "" {
		return value
	}
	return fallback
}

// Providers is everything the Renovate almanac's workflows and scans reach, set up from env.
//
// GitHub is read and written through one client, which refuses every write when
// ALMANAC_READ_ONLY is set, as the runtime reads it. A read-only pass gets no agent harness and
// needs no model credential: the runtime stands in for every harness then, so a run records how
// far it got. Without GitHub credentials there is no client, forge, tracker or sweep, and no
// harness, which checks out commits through the client.
func Providers(env Env) (*providers.Set, error) {
	readOnly := runtime.SwitchesFrom(false, env).ReadOnly
	set := providers.New()
	providers.Provide(set, Registry(env))
	providers.Provide[advisory.Advisories](set, advisory.NewCveNow(env.get("ALMANAC_ADVISORIES_URL", defaultAdvisories)))
	providers.Provide[clock.Clock](set, clock.System{})
	providers.Provide(set, renovate.Target{
		Owner:  env.get("ALMANAC_OWNER", ""),
		Author: env.get("ALMANAC_RENOVATE_AUTHOR", renovate.DefaultAuthor),
	})
	set.ActAs(env.get("ALMANAC_BOT_LOGIN", github.DefaultAuthor().Name))
	if alerts, err := hostile.ParseSlug(env.get("ALMANAC_ALERTS_REPO", "")); err == nil {
		set.AlertTo(alerts)
	}

	auth, err := gitHubAuth(env)
	if err != nil || auth == nil {
		return set, err
	}
	options := []github.Option{}
	if readOnly {
		options = append(options, github.ReadOnly())
	}
	client := github.New(env.get("GITHUB_API_URL", defaultAPI), auth, options...)
	providers.Provide(set, client)
	providers.Provide[effect.Sweep](set, client)
	providers.Provide[effect.Forge](set, github.NewForge(client, github.DefaultAuthor()))
	providers.Provide[effect.Tracker](set, github.NewIssues(client, person(env)))
	if readOnly {
		return set, nil
	}
	harness, err := fixerHarness(env, client)
	if err != nil || harness == nil {
		return set, err
	}
	set.UseHarness(harness)
	return set, nil
}

// ReadOnlyClient is a GitHub client that refuses every write, with the credentials in env, or
// nil when env holds none.
func ReadOnlyClient(env Env) (*github.Client, error) {
	auth, err := gitHubAuth(env)
	if err != nil || auth == nil {
		return nil, err
	}
	return github.New(env.get("GITHUB_API_URL", defaultAPI), auth, github.ReadOnly()), nil
}

// Registry reads the package registries env points at.
func Registry(env Env) registry.Registry {
	return registry.NewHTTP(registryEndpoints(env), nil)
}

// gitHubAuth is a GitHub App's installation if one is configured, or else a token, or nil.
func gitHubAuth(env Env) (github.Auth, error) {
	id, hasID := env("GITHUB_APP_ID")
	key, hasKey := env("GITHUB_APP_PRIVATE_KEY")
	installation, hasInstallation := env("GITHUB_APP_INSTALLATION_ID")
	if hasID && hasKey && hasInstallation {
		number, err := strconv.ParseUint(installation, 10, 64)
		if err != nil {
			return nil, diag.InvalidEnv("GITHUB_APP_INSTALLATION_ID")
		}
		return github.App{ID: id, PrivateKey: key, Installation: number}, nil
	}
	if token, ok := env("GITHUB_TOKEN"); ok && token != "" {
		return github.Token(token), nil
	}
	return nil, nil
}

// person is who is asked when a person is needed: ALMANAC_ASK, or else the account the almanac
// serves, or no one in particular.
func person(env Env) string {
	return env.get("ALMANAC_ASK", env.get("ALMANAC_OWNER", ""))
}

// registryEndpoints are where registries are. ALMANAC_REGISTRY_URL points them all at one
// mirror.
func registryEndpoints(env Env) registry.Endpoints {
	url := env.get("ALMANAC_REGISTRY_URL", "")
	if url == "" {
		return registry.DefaultEndpoints()
	}
	return registry.Endpoints{Npm: url, Crates: url, PyPI: url, Go: url, GitHub: url, Docker: url}
}

// fixerHarness runs agents on the Claude Code CLI, with an API key if there is one, because
// --bare is the tighter mode, or else an OAuth token. It is nil when neither is set.
func fixerHarness(env Env, checkouts claude.Checkouts) (*claude.Harness, error) {
	var credential claude.Credential
	if key, ok := env("ANTHROPIC_API_KEY"); ok && key != "" {
		credential = claude.APIKey(key)
	} else if token, ok := env("CLAUDE_CODE_OAUTH_TOKEN"); ok && token != "" {
		credential = claude.OAuthToken(token)
	} else {
		return nil, nil
	}
	config := claude.DefaultConfig(credential)
	config.Model = model
	config.BaseURL = env.get("ANTHROPIC_BASE_URL", "")
	config.MaxBudgetUSD = maxSessionUSD
	harness, err := claude.New(config)
	if err != nil {
		return nil, err
	}
	image := env.get("ALMANAC_SANDBOX_IMAGE", defaultSandboxImage)
	return harness.
		WithSandbox(podman.New(), sandbox.AgentSpec(image)).
		WithCheckouts(checkouts).
		WithFetching(sandbox.FetchSpec(image)), nil
}
