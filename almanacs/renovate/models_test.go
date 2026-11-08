package renovate_test

import (
	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"

	. "github.com/ivy/almanac/dsl"
)

// This file writes the two Renovate workflows as internal/fixture/workflows does, over the real
// vocabulary instead of the fixture's, to show that its parts fit them as they are. It holds no
// specs, because the DSL's names clash with Ginkgo's and Gomega's.

// The facts of BuildFailures.
var (
	build       = EventOf(renovate.BuildChanged)
	buildRepo   = Field[hostile.Slug](build, "repo")
	buildBranch = Field[hostile.Slug](build, "branch")

	pull       = Fact[renovate.Pull]("pull")
	pullNumber = Field[uint64](pull, "number")
	pullHead   = Field[hostile.Sha](pull, "head")
	pullText   = HostileField[renovate.PullText](pull, "text")

	logs    = Hostile[string]("logs")
	updated = Hostile[[]renovate.Line]("updated")
	repair  = Hostile[patch.Patch]("patch")
)

// buildFailures repairs a Renovate pull request whose CI failed, once the update has cooled.
var buildFailures = Define("build_failures",
	Intent("Repair a Renovate pull request whose build failed, after its cooldown."),
	On(renovate.BuildChanged),
	When(build.Is(renovate.NeedsAttention)),
	Key(buildRepo, buildBranch),
	Subjects(RepoSubject(buildRepo)),
	May(Attests, Awaits, Asks, Pushes),
	Limits(PerHour(20), MaxAttempts(3)),
	Scan(renovate.ScanFailing),

	Observe("pull", "Read the pull request", pull, renovate.PullForBranch, build),
	Decide("actionable", "Is there anything to do?",
		Rule("nothing", "Nothing to do", pull.Is(renovate.NotActionable)),
		Rule("passed", "Green after our repair", All(Not(pull.Is(renovate.Failing)), pull.Is(renovate.RepairPassed)),
			Attest(renovate.TrustContext, Success, buildRepo, pullHead)),
		Rule("green", "Green, and not ours to clear", Not(pull.Is(renovate.Failing))),
	),
	Decide("attempts", "Are the attempts spent?",
		Rule("asked", "Out of attempts, and a person was asked", All(Attempts.Is(Spent), Attempts.Is(Asked))),
		Rule("exhausted", "Out of attempts", Attempts.Is(Spent),
			failAndAsk(Title("Repairs did not fix this build"))...),
	),
	Measure("cooldown", "How old is the release?", renovate.NewestRelease, pullText, renovate.MinReleaseAge),
	Decide("cooled", "Has the release cooled?",
		Rule("undated", "Nothing dates the release", Age.Is(Unknown),
			AskOnce(buildRepo, Title("This update's build failed, and nothing says when it was published")).
				OnPull(pullNumber).
				With(Saying("No agent reads an update before its cooldown passes, and this one has no publish time to measure from. It needs a person.")).
				ThenAwait("a person to look at this update")),
		Rule("cooling", "Still cooling", Age.Is(Young), Await("the update's cooldown to pass")),
	),
	Hold("hold", "Hold the pull request while the fixer works", renovate.TrustContext, buildRepo, pullHead),
	Observe("logs", "Read the failure logs", logs, renovate.FailureLogs, pull),
	Observe("updated", "Read what the update added", updated, renovate.UpdatedLines, pull),
	Repair("fixer", "Repair the build in a sandbox", repair, renovate.Fixer,
		Input("logs", logs), Var("repo", buildRepo), CheckoutAt(buildRepo, pullHead)),
	CheckPatch("check", "Check the patch by policy", repair, pullHead, DefaultPolicy),
	Decide("decide", "What did the fixer produce?",
		Rule("empty", "A repair with no change", Checked.Is(Empty),
			failAndAsk(Title("A repair ended with no change to push"), Showing(repair, renovate.ClosingWords))...),
		Rule("refused", "Refused by the patch policy", Checked.Is(Refused),
			failAndAsk(Title("A repair was refused by the patch policy"), Showing(repair, renovate.PatchDiff))...),
		Rule("undid", "The repair took back the update", Both(repair, updated, renovate.UndoesUpdate),
			failAndAsk(Title("A repair took back the update"), Showing(repair, renovate.PatchDiff))...),
		Otherwise("push", "Push the repair",
			Push(Checked, buildRepo, pullNumber, buildBranch, "fix(deps): repair the build after this update").
				Holding(renovate.TrustContext)),
	),
)

// failAndAsk fails the trust status on the head and asks a person about the pull request.
func failAndAsk(question hostile.Format, body ...Body) []Effect {
	ask := Ask(buildRepo, question).OnPull(pullNumber)
	for _, b := range body {
		ask = ask.With(b)
	}
	return []Effect{Attest(renovate.TrustContext, Failure, buildRepo, pullHead), ask}
}

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

	advised     = Fact[renovate.Advisory]("advisory")
	diff        = Fact[renovate.PackageDiff]("diff")
	diffUnshown = HostileField[string](diff, "unshown")
	diffText    = HostileField[string](diff, "diff")
	review      = Hostile[renovate.Review]("review")
)

// securityFixes validates a Renovate security pull request. These skip the cooldown, so they
// get the most scrutiny. The model can veto, never approve: it is asked only once the
// deterministic checks pass, and a review with no concerns cannot rescue a failed check.
var securityFixes = Define("security_fixes",
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
	Observe("advisory", "Find the advisory this update fixes", advised, renovate.NamesTheFix, viewText),
	Measure("cooldown", "How old is the release?", renovate.NewestRelease, viewText, renovate.MinReleaseAge),
	Observe("diff", "Diff the package's releases", diff, renovate.ReleaseDiff, viewText),
	Decide("checks", "Do the deterministic checks pass?",
		Rule("not_the_fix", "Not the fix an advisory names", Not(advised.Is(renovate.NamesFix)),
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
