// Package workflows writes the Rust almanac's workflows in the Go DSL, against the fixture
// vocabularies, to prove the model can say everything they do. Specs check each one.
package workflows

import (
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/patch"

	. "github.com/ivy/almanac/dsl"
)

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

// BuildFailures repairs a Renovate pull request whose CI failed, once the update has cooled.
var BuildFailures = Define("build_failures",
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
