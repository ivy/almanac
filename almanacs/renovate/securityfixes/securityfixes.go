// Package securityfixes validates a Renovate security pull request, and holds it until it is
// proved. Security updates skip the cooldown Renovate gives other updates, so they get the most
// scrutiny: deterministic checks first, and a reviewer last, which can veto and never approve.
package securityfixes

import (
	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/engine"
	"github.com/ivy/almanac/hostile"

	. "github.com/ivy/almanac/dsl"
)

// Model is the security_fixes workflow. Its steps run in order, and the first rule that matches
// ends the run with that rule's effects.
var Model = Define("security_fixes",
	Intent("Validate a Renovate security update, and hold it until it is proved."),
	On(renovate.SecurityPull),
	When(pr.Is(renovate.IsSecurity)),
	Key(repo, branch),
	Subjects(RepoSubject(repo)),
	May(Attests, Asks),
	Limits(PerHour(20)),
	Scan(renovate.ScanSecurity),

	// Before anything else, because Renovate merges a security update on green and an absent
	// status holds nothing.
	Hold("hold", "Hold the update before anything else", renovate.TrustContext, repo, sha),

	Observe("pull", "Read the pull request", pull, renovate.SecurityPR, pr),
	Decide("found", "Is it an open Renovate pull request?",
		// The hold stays pending: it is not Almanac's to clear.
		Rule("gone", "Not an open Renovate pull request", Not(pull.Is(renovate.Open))),
	),

	Observe("advisory", "Find the advisory this update fixes", advisory, renovate.NamesTheFix, text),
	Measure("cooldown", "How old is the release?", renovate.NewestRelease, text, renovate.MinReleaseAge),
	Observe("diff", "Diff the package's releases", diff, renovate.ReleaseDiff, text),
	Decide("checks", "Do the deterministic checks pass?",
		Rule("not_the_fix", "Not the fix an advisory names", Not(advisory.Is(renovate.NamesFix)),
			refuse(Title("This update is not the fix an advisory names"))...),
		Rule("unreadable", "The package could not be read", Not(diff.Is(renovate.Readable)),
			refuse(Title("This security update's package could not be read"))...),
		// A reviewer that saw part of an update has not reviewed it, and what is left out is
		// predictable, because files are diffed in name order.
		Rule("hidden", "Files no reviewer can read", unshown.Is(renovate.HidesFiles),
			refuse(Title("This security update changes files no reviewer can read"), ShowingText(unshown))...),
		Rule("truncated", "Too large to review in full", diff.Is(renovate.Truncated),
			refuse(Title("This security update is too large for its reviewer to read in full"))...),
		Rule("install_script", "Adds an install script", diff.Is(renovate.AddsInstall),
			refuse(Title("This security update adds an install script"))...),
		// Nothing is wrong yet, so the hold stays pending, and the scan looks again once the
		// release has aged.
		Rule("too_young", "Younger than the cooldown", Not(Age.Is(Cooled)),
			AskOnce(repo, Title("This fix is younger than the cooldown")).OnPull(number)),
	),

	// Only an update that passed every check reaches the reviewer, and its verdict can only
	// keep the update held or refuse it.
	Read("review", "Have a reviewer read the diff", review, renovate.Reviewer,
		Input("diff", diffText), Var("repo", repo)),
	Decide("verdict", "What did the reviewer say?",
		Rule("concerns", "The reviewer raised concerns", review.Is(renovate.RaisedConcerns),
			refuse(Title("A reviewer raised concerns about this security update"), Showing(review, renovate.ReviewNotes))...),
		// To Renovate a success is a passing check, so it is given only once something else
		// has tested the update, or Almanac's word alone would merge it.
		Rule("tested", "No concerns, and tested", pull.Is(renovate.Tested),
			Attest(renovate.TrustContext, Success, repo, head)),
		Rule("untestable", "No concerns, and nothing tests it", pull.Is(renovate.Untestable),
			AskOnce(repo, Title("Nothing tests this repository, so this security update cannot merge")).OnPull(number)),
		Otherwise("waiting", "No concerns, waiting for tests"),
	),
)

// New is the workflow, ready for a runtime.
func New() (*engine.Workflow, error) { return engine.New(renovate.Vocabulary, Model) }

// The facts the workflow reads.
var (
	// pr is the pull request event that started the run.
	pr     = EventOf(renovate.SecurityPull)
	repo   = Field[hostile.Slug](pr, "repo")
	branch = Field[hostile.Slug](pr, "branch")
	sha    = Field[hostile.Sha](pr, "sha")
	number = Field[uint64](pr, "number")

	// pull is the pull request as GitHub shows it now.
	pull = Fact[renovate.SecurityView]("pull")
	head = Field[hostile.Sha](pull, "head")
	text = HostileField[renovate.PullText](pull, "text")

	advisory = Fact[renovate.Advisory]("advisory")
	diff     = Fact[renovate.PackageDiff]("diff")
	unshown  = HostileField[string](diff, "unshown")
	diffText = HostileField[string](diff, "diff")
	review   = Hostile[renovate.Review]("review")
)

// refuse fails the trust status on the head and asks a person about the pull request, once.
func refuse(question hostile.Format, body ...Body) []Effect {
	ask := AskOnce(repo, question).OnPull(number)
	for _, b := range body {
		ask = ask.With(b)
	}
	return []Effect{Attest(renovate.TrustContext, Failure, repo, head), ask}
}
