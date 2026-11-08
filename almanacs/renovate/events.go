package renovate

import (
	"encoding/json"
	"strings"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
)

// BuildEvent is a build finishing. It arrives as a check suite or a commit status, and they
// mean the same thing here, so both parse to one event.
type BuildEvent struct {
	Repo     hostile.Slug `json:"repo"`
	Branch   hostile.Slug `json:"branch"`
	Sha      hostile.Sha  `json:"sha"`
	Failed   bool         `json:"failed"`
	Finished bool         `json:"finished"`
	// Patched is a head commit that carries an Almanac patch.
	Patched bool `json:"patched"`
	// Bookkeeping is a status that is not about the build, so it says nothing here.
	Bookkeeping bool `json:"bookkeeping"`
}

// Kind is a completed check suite.
func (BuildEvent) Kind() string { return "github.check_suite.completed" }

// AlsoKinds is a commit status, which reports a build as a check suite does.
func (BuildEvent) AlsoKinds() []string { return []string{"github.status"} }

// Parse reads a check suite, or else a commit status.
func (e *BuildEvent) Parse(p *wire.Payload) {
	e.Repo = wire.Field[hostile.Slug](p, "/repository/full_name")
	if sha := wire.OptionalField[hostile.Sha](p, "/check_suite/head_sha"); sha != nil {
		e.Sha = *sha
		e.Branch = wire.Field[hostile.Slug](p, "/check_suite/head_branch")
		e.Failed = hostile.Narrow(p.OptionalText("/check_suite/conclusion"), isFailedConclusion)
		e.Finished = true
		e.Patched = hostile.Narrow(p.OptionalText("/check_suite/head_commit/message"), carriesPatch)
		return
	}
	state := p.Text("/state")
	e.Branch = wire.Field[hostile.Slug](p, "/branches/0/name")
	e.Sha = wire.Field[hostile.Sha](p, "/sha")
	e.Failed = hostile.Narrow(state, isFailedState)
	e.Finished = !hostile.Narrow(state, isPendingState)
	e.Patched = hostile.Narrow(p.OptionalText("/commit/commit/message"), carriesPatch)
	e.Bookkeeping = hostile.Narrow(p.Text("/context"), isBookkeeping)
}

// failedConclusions are the check suite conclusions that mean the build failed.
var failedConclusions = []string{"failure", "timed_out", "action_required", "startup_failure", "cancelled"}

func isFailedConclusion(conclusion *string) bool {
	if conclusion == nil {
		return false
	}
	for _, failed := range failedConclusions {
		if *conclusion == failed {
			return true
		}
	}
	return false
}

func isFailedState(state string) bool  { return state == "failure" || state == "error" }
func isPendingState(state string) bool { return state == "pending" }

func carriesPatch(message *string) bool {
	return message != nil && strings.Contains(*message, PatchTrailer)
}

// isBookkeeping is Almanac's own status or Renovate's, which say nothing about whether the
// build works.
func isBookkeeping(context string) bool {
	return context == TrustContext || strings.HasPrefix(context, renovateBranch)
}

// SecurityEvent is a pull request appearing or gaining a label.
type SecurityEvent struct {
	Repo    hostile.Slug `json:"repo"`
	Branch  hostile.Slug `json:"branch"`
	Sha     hostile.Sha  `json:"sha"`
	Number  uint64       `json:"number"`
	Labeled bool         `json:"labeled"`
}

// Kind is a pull request opened.
func (SecurityEvent) Kind() string { return "github.pull_request.opened" }

// AlsoKinds is a pull request labelled, because a security label can come after it opens.
func (SecurityEvent) AlsoKinds() []string { return []string{"github.pull_request.labeled"} }

// Parse reads the pull request, and whether it is labelled as a security update.
func (e *SecurityEvent) Parse(p *wire.Payload) {
	labels := p.TextList("/pull_request/labels", "/name")
	e.Repo = wire.Field[hostile.Slug](p, "/repository/full_name")
	e.Branch = wire.Field[hostile.Slug](p, "/pull_request/head/ref")
	e.Sha = wire.Field[hostile.Sha](p, "/pull_request/head/sha")
	e.Number = wire.Field[uint64](p, "/number")
	e.Labeled = hostile.Narrow(labels, hasSecurityLabel)
}

func hasSecurityLabel(labels []string) bool {
	for _, label := range labels {
		if label == securityLabel {
			return true
		}
	}
	return false
}

// The events the Renovate almanac's workflows trigger on.
var (
	BuildChanged = vocab.Event[BuildEvent](Vocabulary, "renovate.build_changed",
		"a build finished, or its status changed, on a commit")
	SecurityPull = vocab.Event[SecurityEvent](Vocabulary, "renovate.security_pull",
		"a pull request was opened or labelled")
)

// SuitePayload is a check suite payload, for a scan: a scanned event takes the same path as a
// webhook.
func SuitePayload(repo, branch hostile.Slug, sha hostile.Sha, failed, patched bool) json.RawMessage {
	message, conclusion := "", "success"
	if patched {
		message = PatchTrailer
	}
	if failed {
		conclusion = "failure"
	}
	return mustJSON(map[string]any{
		"repository": map[string]any{"full_name": repo},
		"check_suite": map[string]any{
			"head_sha":    sha,
			"head_branch": branch,
			"conclusion":  conclusion,
			"head_commit": map[string]any{"message": message},
		},
	})
}

// openedPayload is a pull request opened, for a scan. Its labels say only whether it is
// labelled as a security update, which is all the event reads of them.
func openedPayload(repo, branch hostile.Slug, sha hostile.Sha, number uint64, labeled bool) json.RawMessage {
	labels := []any{}
	if labeled {
		labels = append(labels, map[string]any{"name": securityLabel})
	}
	return mustJSON(map[string]any{
		"number":     number,
		"repository": map[string]any{"full_name": repo},
		"pull_request": map[string]any{
			"head":   map[string]any{"ref": branch, "sha": sha},
			"labels": labels,
		},
	})
}

// mustJSON encodes a payload built from clean values, which always encodes.
func mustJSON(v any) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}
