// Package registry reads what package registries say about a release: when it was published,
// for the cooldown proof, and its archive, for diffing. An archive is read as data, in memory.
// Nothing in one is written to disk or run, so a hostile install script is only text.
//
// Everything a registry or an archive says in words comes back hostile. Only numbers, bools
// and times come back clean.
package registry

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ivy/almanac/hostile"
)

// Ecosystem is where a package is published. It is a closed set, so hostile text naming one,
// such as a Renovate datasource, can be sorted into it with [hostile.Classify].
type Ecosystem struct {
	hostile.Closed
	name string
}

// The ecosystems the registry knows.
var (
	Npm    = Ecosystem{name: "npm"}
	Crates = Ecosystem{name: "crates.io"}
	PyPI   = Ecosystem{name: "PyPI"}
	Go     = Ecosystem{name: "Go"}
	// GitHubRelease is an action, or anything else versioned by GitHub releases. The name is
	// owner/repo.
	GitHubRelease = Ecosystem{name: "GitHub releases"}
	Docker        = Ecosystem{name: "Docker Hub"}
	// Unsupported is a registry with no timestamps, such as ghcr.io. Its releases are never
	// cooled.
	Unsupported = Ecosystem{name: "an unsupported registry"}
)

func (e Ecosystem) String() string {
	if e.name == "" {
		return Unsupported.name
	}
	return e.name
}

// Package is a release, by validated name and version, because both arrive in hostile text.
// Build one with [NewPackage].
type Package struct {
	ecosystem Ecosystem
	name      hostile.Slug
	version   hostile.Slug
}

// NewPackage checks that neither name nor version has a "." or ".." path segment, which would
// walk out of the registry path the package is looked up under.
func NewPackage(ecosystem Ecosystem, name, version hostile.Slug) (Package, error) {
	if traverses(name) || traverses(version) {
		return Package{}, &NotAPackage{Name: name, Version: version}
	}
	return Package{ecosystem: ecosystem, name: name, version: version}, nil
}

func traverses(s hostile.Slug) bool {
	for part := range strings.SplitSeq(s.String(), "/") {
		if part == ".." || part == "." {
			return true
		}
	}
	return false
}

// Ecosystem is where the package is published.
func (p Package) Ecosystem() Ecosystem { return p.ecosystem }

// Name is the package's name in its ecosystem.
func (p Package) Name() hostile.Slug { return p.name }

// Version is the release's version.
func (p Package) Version() hostile.Slug { return p.version }

// String is name@version.
func (p Package) String() string { return p.name.String() + "@" + p.version.String() }

// valid reports whether p was built by NewPackage, rather than being a zero Package.
func (p Package) valid() bool { return p.name != hostile.Slug{} && p.version != hostile.Slug{} }

// NotAPackage is a name or version that would walk out of a registry's path.
type NotAPackage struct {
	Name, Version hostile.Slug
}

func (e *NotAPackage) Error() string {
	return e.Name.String() + "@" + e.Version.String() + " is not a package name: a . or .. segment would walk out of the registry's path, so nothing looks it up; check what named this release"
}

// Registry reads registries.
type Registry interface {
	// Published is when the release was published. known is false when the registry does not
	// say, which an implementation must report rather than guess: an unknown time never cools.
	Published(ctx context.Context, p Package) (at time.Time, known bool, err error)

	// Artifact is the release's source archive, for diffing.
	Artifact(ctx context.Context, p Package) ([]byte, error)
}

// ErrNoArtifact is a release with no archive to diff: the registry does not know it, or its
// ecosystem has none that this package reads.
var ErrNoArtifact = errors.New("the registry has no archive of this release")

// Error is a registry that could not answer. Its message names the release and never quotes
// what the registry sent.
type Error struct {
	Package Package
	// Doing is what was being done, such as "read the publish time".
	Doing string
	// Reason is why it failed, in words this package chose.
	Reason string
	// Help says what to do next.
	Help string
	// Err is what failed underneath, kept for errors.Is and errors.As. It is nil when its
	// message could quote text the registry sent.
	Err error
}

func (e *Error) Error() string {
	return "cannot " + e.Doing + " of " + e.Package.ecosystem.String() + " package " + e.Package.String() + ": " + e.Reason + "; " + e.Help
}

func (e *Error) Unwrap() error { return e.Err }
