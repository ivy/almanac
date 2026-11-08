package renovate

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/registry"
)

// PullText is the parts of a pull request that say what it updates.
type PullText struct {
	Body *string `json:"body"`
}

// UpdatedPackage is one package a pull request updates, as its text claims.
type UpdatedPackage struct {
	Datasource string `json:"datasource"`
	Name       string `json:"name"`
	// Current is the version being replaced. Empty when it cannot be read.
	Current string `json:"current"`
	Version string `json:"version"`
}

// Update is everything a pull request updates. A group updates several packages at once.
type Update struct {
	Packages []UpdatedPackage `json:"packages"`
}

// UpdateOf reads the table Renovate puts at the top of a pull request. Anything it cannot read
// is left out, and an update it cannot fully read is not cooled, so a parsing gap only ever
// makes Almanac wait. It reads hostile text, so call it through hostile.Map.
func UpdateOf(text PullText) Update {
	var update Update
	if text.Body == nil {
		return update
	}
	for _, line := range strings.Split(*text.Body, "\n") {
		row := strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(row, "|") || !strings.Contains(row, "→") {
			continue
		}
		if found, ok := packageOf(row); ok {
			update.Packages = append(update.Packages, found)
		}
	}
	return update
}

// between is the text after the first open and before the next close.
func between(text, open, close string) (string, bool) {
	start := strings.Index(text, open)
	if start < 0 {
		return "", false
	}
	rest := text[start+len(open):]
	end := strings.Index(rest, close)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// fromURL is the datasource and version from a URL like
// `.../badges/age/go/example.com/mod/v1.104.4?slim=true`.
func fromURL(row, marker string) (datasource, version string, ok bool) {
	start := strings.Index(row, marker)
	if start < 0 {
		return "", "", false
	}
	rest := row[start+len(marker):]
	if end := strings.IndexAny(rest, ")? "); end >= 0 {
		rest = rest[:end]
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		return "", "", false
	}
	return parts[0], parts[len(parts)-1], true
}

// bare is a version without a range's operator.
func bare(version string) string { return strings.TrimLeft(version, "^~=><") }

// fromChange is the new version in a "`old` → `new`" cell.
func fromChange(row string) (string, bool) {
	sides := strings.Split(row, "→")
	if len(sides) < 2 {
		return "", false
	}
	version, ok := between(sides[1], "`", "`")
	return bare(version), ok
}

// oldVersion is the version being replaced, preferring a diff link's exact version to a cell's
// range.
func oldVersion(row string) string {
	const marker = "renovatebot.com/diffs/"
	if start := strings.Index(row, marker); start >= 0 {
		rest := row[start+len(marker):]
		if end := strings.IndexAny(rest, ") "); end >= 0 {
			rest = rest[:end]
		}
		if parts := strings.Split(rest, "/"); len(parts) >= 4 {
			return parts[len(parts)-2]
		}
	}
	before, _, _ := strings.Cut(row, "→")
	quoted := strings.Split(before, "`")
	if len(quoted) < 2 {
		return ""
	}
	return bare(quoted[len(quoted)-2])
}

func packageOf(row string) (UpdatedPackage, bool) {
	name, ok := between(row, "[", "]")
	if !ok {
		return UpdatedPackage{}, false
	}
	datasource, version, exact := fromURL(row, "/badges/age/")
	if !exact {
		datasource, version, exact = fromURL(row, "renovatebot.com/diffs/")
	}
	if !exact {
		datasource = ""
		if columns := strings.Split(row, "|"); len(columns) > 2 && strings.TrimSpace(columns[2]) == "action" {
			datasource = "github-tags"
		}
		if version, ok = fromChange(row); !ok {
			return UpdatedPackage{}, false
		}
	}
	return UpdatedPackage{Datasource: datasource, Name: name, Current: oldVersion(row), Version: version}, true
}

// datasource is where Renovate says a package comes from. Renovate names many; these are the
// ones a registry or an advisory feed is read for, and any other is unsupported.
type datasource struct {
	hostile.Closed
	name string
	// ecosystem is the registry the package is published in.
	ecosystem registry.Ecosystem
	// feed is how advisory feeds name the ecosystem, or empty when none is read.
	feed string
}

var (
	fromNpm         = datasource{name: "npm", ecosystem: registry.Npm, feed: "npm"}
	fromCrates      = datasource{name: "crate", ecosystem: registry.Crates, feed: "crates.io"}
	fromPyPI        = datasource{name: "pypi", ecosystem: registry.PyPI, feed: "PyPI"}
	fromGo          = datasource{name: "go", ecosystem: registry.Go, feed: "Go"}
	fromDocker      = datasource{name: "docker", ecosystem: registry.Docker}
	fromGitHubTags  = datasource{name: "github-tags", ecosystem: registry.GitHubRelease}
	fromGitHubRel   = datasource{name: "github-releases", ecosystem: registry.GitHubRelease}
	fromUnsupported = datasource{name: "", ecosystem: registry.Unsupported}

	datasources = []datasource{fromNpm, fromCrates, fromPyPI, fromGo, fromDocker, fromGitHubTags, fromGitHubRel}
)

// UnmarshalJSON reads a datasource Renovate names, and any other as unsupported.
func (d *datasource) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	*d = fromUnsupported
	for _, known := range datasources {
		if known.name == name {
			*d = known
		}
	}
	return nil
}

// opened is an updated package whose name and versions are validated slugs: the form in which
// what a pull request claims may reach a registry or a feed.
type opened struct {
	Datasource datasource   `json:"datasource"`
	Name       hostile.Slug `json:"name"`
	// Current is nil when the version being replaced could not be read.
	Current *hostile.Slug `json:"current"`
	Version hostile.Slug  `json:"version"`
}

// release is the package at version, or false if the registry could not look it up safely.
func (p opened) release(version *hostile.Slug) (registry.Package, bool) {
	if version == nil {
		return registry.Package{}, false
	}
	found, err := registry.NewPackage(p.Datasource.ecosystem, p.Name, *version)
	return found, err == nil
}

// open validates every package an update claims. It is false if any name or version is not a
// slug, and then nothing in the update is read: one package that cannot be read safely leaves
// the whole update unproven.
func open(update hostile.Of[Update]) ([]opened, bool) {
	var packages []opened
	if err := hostile.DecodeClean(hostile.Map(update, openable), &packages); err != nil {
		return nil, false
	}
	return packages, true
}

// openable is update in the shape opened decodes from, with a version that could not be read
// as null.
func openable(update Update) json.RawMessage {
	type claimed struct {
		Datasource string  `json:"datasource"`
		Name       string  `json:"name"`
		Current    *string `json:"current"`
		Version    string  `json:"version"`
	}
	all := make([]claimed, 0, len(update.Packages))
	for _, p := range update.Packages {
		c := claimed{Datasource: p.Datasource, Name: p.Name, Version: p.Version}
		if p.Current != "" {
			c.Current = &p.Current
		}
		all = append(all, c)
	}
	out, err := json.Marshal(all)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return out
}

// Newest is when the newest release an update installs was published. Anything unknown makes
// the whole unknown: an update with no packages, a package that is not a slug or not in a
// registry Almanac reads, or a release its registry gives no time for.
func Newest(ctx context.Context, reg registry.Registry, update hostile.Of[Update]) (cooled.Published, error) {
	packages, ok := open(update)
	if !ok {
		return cooled.Published{}, nil
	}
	if len(packages) == 0 {
		return cooled.Published{}, nil
	}
	var newest time.Time
	for _, p := range packages {
		release, ok := p.release(&p.Version)
		if !ok {
			return cooled.Published{}, nil
		}
		at, known, err := reg.Published(ctx, release)
		if err != nil {
			return cooled.Published{}, err
		}
		if !known {
			return cooled.Published{}, nil
		}
		if at.After(newest) {
			newest = at
		}
	}
	return cooled.PublishedAt(newest), nil
}
