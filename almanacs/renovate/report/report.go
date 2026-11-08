// Package report says where every open Renovate pull request stands: exactly one state, and
// always a reason, as docs/report-states.md defines them. A state is derived from what GitHub
// says now and is never stored, so it cannot go stale. A pull request's title is hostile text,
// so a report names a pull request by number and validated branch only.
package report

import (
	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/cooled"
)

// State is where a pull request stands. The states are ordered as a report totals them.
type State int

// The states, as docs/report-states.md defines them.
const (
	Mergeable State = iota
	InProgress
	Waiting
	NeedsPerson
	BlockedByConfig
)

var stateNames = [...]string{"mergeable", "in-progress", "waiting", "needs-person", "blocked-by-config"}

func (s State) String() string { return stateNames[s] }

// MarshalText is the state's name, so a State is a JSON string and a JSON object key.
func (s State) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Verdict is one pull request's standing: exactly one state, and always a reason.
type Verdict struct {
	State  State  `json:"state"`
	Reason string `json:"reason"`
	// Note is something worth knowing that does not change the state.
	Note string `json:"note,omitempty"`
}

// Conflict is whether a pull request can merge into its base.
type Conflict int

const (
	// NoConflict is a pull request GitHub says can merge.
	NoConflict Conflict = iota
	// Conflicted is a pull request GitHub says cannot merge.
	Conflicted
	// ConflictUnknown is a pull request GitHub has not worked out yet, which it does lazily.
	ConflictUnknown
)

// Flag is a yes-or-no Renovate setting, as far as Almanac can tell.
type Flag int

const (
	// Unset is a setting the config does not make, or a config that could not be read.
	Unset Flag = iota
	Off
	On
)

// or is f, or shared where f is unset.
func (f Flag) or(shared Flag) Flag {
	if f == Unset {
		return shared
	}
	return f
}

// Config is what can be learned about a repository's Renovate setup.
type Config struct {
	Automerge         Flag
	PlatformAutomerge Flag
	// GitIgnoredAuthors is whether gitIgnoredAuthors names anyone.
	GitIgnoredAuthors Flag
	// TrustRequired is the default branch requiring Almanac's status to merge.
	TrustRequired bool
}

// or is c, with what it leaves unset taken from shared.
func (c Config) or(shared Config) Config {
	return Config{
		Automerge:         c.Automerge.or(shared.Automerge),
		PlatformAutomerge: c.PlatformAutomerge.or(shared.PlatformAutomerge),
		GitIgnoredAuthors: c.GitIgnoredAuthors.or(shared.GitIgnoredAuthors),
		TrustRequired:     c.TrustRequired,
	}
}

// Facts are everything the policy reads about one pull request.
type Facts struct {
	Conflict Conflict
	Checks   renovate.Checks
	Trust    renovate.Trust
	Security bool
	// Attempts are the patches Almanac has already pushed to the branch.
	Attempts int
	Cooldown cooled.Age
	Config   Config
}

// The reasons a setting is in the way. Each names the setting to change.
const (
	// ignoreAlmanac is needed because Renovate treats a branch someone else pushed to as edited,
	// and neither merges nor rebases it.
	ignoreAlmanac = "add Almanac's author to gitIgnoredAuthors, or Renovate will not merge a patched branch"
	// holdTrust is needed because platform automerge ignores any check that is not required.
	holdTrust = "set platformAutomerge to false, or require the almanac/trust check"
)

func verdict(state State, reason string) Verdict { return Verdict{State: state, Reason: reason} }

// trustWouldHold reports whether Almanac's status can hold a merge in a repository set up as c.
func trustWouldHold(c Config) bool {
	return c.TrustRequired || c.PlatformAutomerge == Off
}

// Derive is the whole policy for where a pull request stands. Earlier rules win.
func Derive(f Facts) Verdict {
	patched := f.Attempts > 0
	switch {
	case f.Conflict == ConflictUnknown:
		return verdict(Waiting, "GitHub to compute mergeability")
	case f.Conflict == Conflicted && patched && f.Config.GitIgnoredAuthors == Off:
		return verdict(BlockedByConfig, ignoreAlmanac)
	case f.Conflict == Conflicted && patched:
		// The rebase starts the branch over without the patch, and the next red build is
		// repaired again.
		return verdict(Waiting, "Renovate to rebase it, which drops Almanac's patch")
	case f.Conflict == Conflicted:
		return verdict(Waiting, "Renovate to rebase it")
	}
	if f.Trust == renovate.TrustFailure {
		return verdict(NeedsPerson, "it failed trust checks")
	}
	if f.Security && f.Trust != renovate.TrustSuccess && !trustWouldHold(f.Config) {
		return verdict(BlockedByConfig, holdTrust)
	}
	switch f.Checks {
	case renovate.ChecksRed:
		if f.Attempts >= renovate.AttemptCap {
			return verdict(NeedsPerson, "repairs did not fix the build")
		}
		switch f.Cooldown {
		case cooled.Passed:
			return verdict(InProgress, "a repair is due")
		case cooled.Young:
			return verdict(Waiting, "the update's cooldown to pass")
		}
		return verdict(NeedsPerson, "nothing says when this update was published")
	case renovate.ChecksPending:
		return verdict(Waiting, "CI to finish")
	}
	if f.Trust == renovate.TrustPending && f.Checks == renovate.ChecksNone {
		return verdict(NeedsPerson, "nothing tests this repository")
	}
	if f.Trust == renovate.TrustPending {
		return verdict(InProgress, "Almanac is validating it")
	}
	if patched && f.Config.GitIgnoredAuthors == Off {
		return verdict(BlockedByConfig, ignoreAlmanac)
	}
	v := verdict(Mergeable, "nothing is in its way")
	switch {
	case f.Checks == renovate.ChecksNone:
		v.Note = "no checks ran on this pull request"
	case f.Config.Automerge == Off:
		v.Note = "automerge is off, so this will not merge itself"
	}
	return v
}
