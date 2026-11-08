package workflows

import (
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/fixture/renovate"

	. "github.com/ivy/almanac/dsl"
)

// The facts of SecurityFixes.
var (
	opened       = EventOf(renovate.SecurityPull)
	openedRepo   = Field[hostile.Slug](opened, "repo")
	openedBranch = Field[hostile.Slug](opened, "branch")
	openedSha    = Field[hostile.Sha](opened, "sha")
	openedNumber = Field[uint64](opened, "number")

	view     = Fact[renovate.SecurityView]("pull")
	viewHead = Field[hostile.Sha](view, "head")
	viewText = HostileField[renovate.PullText](view, "text")

	advisory    = Fact[renovate.Advisory]("advisory")
	diff        = Fact[renovate.PackageDiff]("diff")
	diffUnshown = HostileField[string](diff, "unshown")
	diffText    = HostileField[string](diff, "diff")
	review      = Hostile[renovate.Review]("review")
)

// SecurityFixes validates a Renovate security pull request. These skip the cooldown, so they
// get the most scrutiny. The model can veto, never approve: it is asked only once the
// deterministic checks pass, and a review with no concerns cannot rescue a failed check.
var SecurityFixes = Define("security_fixes",
	Intent("Validate a Renovate security update, and hold it until it is proved."),
	On(renovate.SecurityPull),
	When(opened.Is(renovate.IsSecurity)),
	Key(openedRepo, openedBranch),
	Subjects(RepoSubject(openedRepo)),
	May(Attests, Asks),
	Limits(PerHour(20)),
	Scan(renovate.ScanSecurity),

	Hold("hold", "Hold the update before anything else", renovate.TrustContext, openedRepo, openedSha),
	Observe("pull", "Read the pull request", view, renovate.SecurityPR, opened),
	Decide("found", "Is it an open Renovate pull request?",
		Rule("gone", "Not an open Renovate pull request", Not(view.Is(renovate.Open))),
	),
	Observe("advisory", "Find the advisory this update fixes", advisory, renovate.NamesTheFix, viewText),
	Measure("cooldown", "How old is the release?", renovate.NewestRelease, viewText, renovate.MinReleaseAge),
	Observe("diff", "Diff the package's releases", diff, renovate.ReleaseDiff, viewText),
	Decide("checks", "Do the deterministic checks pass?",
		Rule("not_the_fix", "Not the fix an advisory names", Not(advisory.Is(renovate.NamesFix)),
			refuse(Title("This update is not the fix an advisory names"))...),
		Rule("unreadable", "The package could not be read", Not(diff.Is(renovate.Readable)),
			refuse(Title("This security update's package could not be read"))...),
		Rule("hidden", "Files no reviewer can read", diffUnshown.Is(renovate.HidesFiles),
			refuse(Title("This security update changes files no reviewer can read"), ShowingText(diffUnshown))...),
		Rule("truncated", "Too large to review in full", diff.Is(renovate.Truncated),
			refuse(Title("This security update is too large for its reviewer to read in full"))...),
		Rule("install_script", "Adds an install script", diff.Is(renovate.AddsInstall),
			refuse(Title("This security update adds an install script"))...),
		Rule("too_young", "Younger than the cooldown", Not(Age.Is(Cooled)),
			AskOnce(openedRepo, Title("This fix is younger than the cooldown")).OnPull(openedNumber)),
	),
	Read("review", "Have a reviewer read the diff", review, renovate.Reviewer,
		Input("diff", diffText), Var("repo", openedRepo)),
	Decide("verdict", "What did the reviewer say?",
		Rule("concerns", "The reviewer raised concerns", review.Is(renovate.RaisedConcerns),
			refuse(Title("A reviewer raised concerns about this security update"), Showing(review, renovate.ReviewNotes))...),
		Rule("tested", "No concerns, and tested", view.Is(renovate.Tested),
			Attest(renovate.TrustContext, Success, openedRepo, viewHead)),
		Rule("untestable", "No concerns, and nothing tests it", view.Is(renovate.Untestable),
			AskOnce(openedRepo, Title("Nothing tests this repository, so this security update cannot merge")).OnPull(openedNumber)),
		Otherwise("waiting", "No concerns, waiting for tests"),
	),
)

// refuse fails the trust status on the head and asks a person, once, about the pull request.
func refuse(question hostile.Format, body ...Body) []Effect {
	ask := AskOnce(openedRepo, question).OnPull(openedNumber)
	for _, b := range body {
		ask = ask.With(b)
	}
	return []Effect{Attest(renovate.TrustContext, Failure, openedRepo, viewHead), ask}
}
