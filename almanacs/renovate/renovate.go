// Package renovate is the Renovate almanac's vocabulary: the events, observers, predicates,
// agents and scans its workflows are written with. Every part is registered in Vocabulary with
// a label a person can read, and reads GitHub, package registries and advisory feeds through
// the providers it names.
//
// A Renovate pull request's text is hostile, and so are the package names and versions in it.
// They leave taint only by decoding into validated slugs (see [Newest]), so a name that is not
// a slug never reaches a registry or a feed.
package renovate

import (
	"time"

	"github.com/ivy/almanac/advisory"
	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/registry"
	"github.com/ivy/almanac/vocab"
)

// Vocabulary holds every part of the Renovate almanac.
var Vocabulary = vocab.New()

const (
	// TrustContext is the status Almanac holds a pull request with, and gives its verdict in.
	TrustContext = "almanac/trust"
	// MinReleaseAge is how old a release must be before anything reads it.
	MinReleaseAge = 72 * time.Hour
	// AttemptCap is how many repairs are tried on one pull request before a person is asked.
	AttemptCap = 3
	// PatchTrailer is set on every commit Almanac makes.
	PatchTrailer = "Almanac-Key:"
	// CIPatience is how long CI gets to report on a pull request before a person is told
	// nothing has.
	CIPatience = time.Hour
	// DefaultAuthor is Renovate's login in the operator's repositories, unless the
	// environment names another.
	DefaultAuthor = "ivy-s-renovate-bot[bot]"
)

const (
	renovateBranch       = "renovate/"
	vulnerabilitySuffix  = "-vulnerability"
	securityLabel        = "security"
	logLimit             = 256 * 1024
	workflowsDirectory   = ".github/workflows"
	scanIDPrefix         = "scan:"
	securityScanIDPrefix = "scan:security:"
)

// Target is whose pull requests the almanac looks after: Renovate's, in Owner's repositories.
type Target struct {
	// Owner is the account whose repositories are scanned.
	Owner string
	// Author is the login that opens Renovate's pull requests. A branch name proves nothing,
	// so only pull requests by Author are touched.
	Author string
}

var (
	needsGitHub     = providers.Require[*github.Client]()
	needsTarget     = providers.Require[Target]()
	needsClock      = providers.Require[clock.Clock]()
	needsRegistry   = providers.Require[registry.Registry]()
	needsAdvisories = providers.Require[advisory.Advisories]()
)
