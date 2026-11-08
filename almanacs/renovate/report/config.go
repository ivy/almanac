package report

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/titanous/json5"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
)

// configFiles are where Renovate looks for a repository's config, in its order.
var configFiles = []string{
	"renovate.json",
	"renovate.json5",
	".renovaterc.json",
	".github/renovate.json",
	".github/renovate.json5",
	".renovaterc",
}

// presetFile is where a github>owner/repo preset keeps its settings.
const presetFile = "default.json"

// renovateConfig is the part of a Renovate config a report reads. Each field takes whatever
// JSON it is given and keeps only what it understands, so one odd setting never hides the
// others.
type renovateConfig struct {
	Automerge         flag           `json:"automerge"`
	PlatformAutomerge flag           `json:"platformAutomerge"`
	GitIgnoredAuthors ignoredAuthors `json:"gitIgnoredAuthors"`
	Extends           presets        `json:"extends"`
}

// flag is a setting that is On or Off when it is a bool, and Unset when it is anything else.
type flag Flag

func (f *flag) UnmarshalJSON(data []byte) error {
	var set bool
	*f = flag(Unset)
	if json.Unmarshal(data, &set) == nil && string(data) != "null" {
		*f = flag(Off)
		if set {
			*f = flag(On)
		}
	}
	return nil
}

// ignoredAuthors is whether a list of authors names anyone, and Unset when it is not a list.
type ignoredAuthors Flag

func (a *ignoredAuthors) UnmarshalJSON(data []byte) error {
	var authors []json.RawMessage
	*a = ignoredAuthors(Unset)
	if json.Unmarshal(data, &authors) == nil && string(data) != "null" {
		*a = ignoredAuthors(Off)
		if len(authors) > 0 {
			*a = ignoredAuthors(On)
		}
	}
	return nil
}

// presets are the repositories a config extends with github>owner/repo, the form whose settings
// live in that repository's default.json. A preset in any other form, or one whose repository
// is not a slug, is left out.
type presets []hostile.Slug

func (p *presets) UnmarshalJSON(data []byte) error {
	var names []json.RawMessage
	*p = nil
	if json.Unmarshal(data, &names) != nil {
		return nil
	}
	for _, raw := range names {
		var name string
		if json.Unmarshal(raw, &name) != nil {
			continue
		}
		if repo, ok := presetRepo(name); ok {
			*p = append(*p, repo)
		}
	}
	return nil
}

// presetRepo is the repository a github> preset names. A path inside it, such as
// github>owner/repo//path, is a file other than default.json, so it names none.
func presetRepo(name string) (hostile.Slug, bool) {
	repo, ok := strings.CutPrefix(name, "github>")
	if !ok || (strings.ContainsAny(repo, "/:") && strings.Count(repo, "/") != 1) {
		return hostile.Slug{}, false
	}
	if at := strings.IndexAny(repo, "#:"); at >= 0 {
		repo = repo[:at]
	}
	slug, err := hostile.ParseSlug(repo)
	return slug, err == nil
}

// asJSON is a Renovate config, written in JSON5, as plain JSON. A config that does not parse is
// null, which reads as a config that sets nothing.
func asJSON(text string) json.RawMessage {
	var value any
	if json5.Unmarshal([]byte(text), &value) != nil {
		return json.RawMessage("null")
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return plain
}

// readable is text read as a Renovate config, or a config that sets nothing if it is not one.
func readable(text hostile.Of[string]) renovateConfig {
	var c renovateConfig
	if hostile.DecodeClean(hostile.Map(text, asJSON), &c) != nil {
		return renovateConfig{}
	}
	return c
}

// ConfigOf is what a Renovate config says, in a repository whose default branch does or does
// not require Almanac's status. A config that cannot be read sets nothing.
func ConfigOf(text hostile.Of[string], trustRequired bool) Config {
	c := readable(text)
	return Config{
		Automerge:         Flag(c.Automerge),
		PlatformAutomerge: Flag(c.PlatformAutomerge),
		GitIgnoredAuthors: Flag(c.GitIgnoredAuthors),
		TrustRequired:     trustRequired,
	}
}

// PresetsOf are the repositories a Renovate config extends with github>owner/repo.
func PresetsOf(text hostile.Of[string]) []hostile.Slug {
	return readable(text).Extends
}

// requiresTrust reports whether a branch's required checks include Almanac's status.
func requiresTrust(required []string) bool {
	return slices.Contains(required, renovate.TrustContext)
}

// readConfig is a repository's Renovate setup: its own config, with what that leaves unset
// taken from the presets it extends, earlier presets first. Settings in the runner's global
// config are not visible here.
func readConfig(ctx context.Context, client *github.Client, repo, branch hostile.Slug) (Config, error) {
	var text hostile.Of[string]
	for _, file := range configFiles {
		found, ok, err := client.File(ctx, repo, file)
		if err != nil {
			return Config{}, err
		}
		if ok {
			text = found
			break
		}
	}
	required, err := client.RequiredChecks(ctx, repo, branch)
	if err != nil {
		return Config{}, err
	}
	trustRequired := hostile.Narrow(required, requiresTrust)
	var shared Config
	for _, preset := range PresetsOf(text) {
		found, _, err := client.File(ctx, preset, presetFile)
		if err != nil {
			return Config{}, err
		}
		shared = shared.or(ConfigOf(found, trustRequired))
	}
	return ConfigOf(text, trustRequired).or(shared), nil
}
