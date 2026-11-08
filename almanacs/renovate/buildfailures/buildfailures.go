// Package buildfailures repairs a Renovate pull request whose CI failed, once the update has
// cooled.
//
// A run reads the pull request as GitHub shows it now, and stops if there is nothing to do or
// the repairs are spent. It waits out the update's cooldown, because no agent reads a release
// younger than that. Then it holds the pull request, has the fixer repair the build in a
// sandbox, and pushes the repair if the patch policy accepts it and it keeps the update. Every
// other outcome fails Almanac's status and asks a person. When CI passes on a repair Almanac
// pushed, the hold comes off.
package buildfailures

import (
	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/engine"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"

	. "github.com/ivy/almanac/dsl"
)

// Model is the workflow, as data the engine runs and the checker proves.
var Model = Define("build_failures",
	Intent("Repair a Renovate pull request whose build failed, after its cooldown."),
	On(renovate.BuildChanged),
	When(build.Is(renovate.NeedsAttention)),
	Key(repo, branch),
	Subjects(RepoSubject(repo)),
	May(Attests, Awaits, Asks, Pushes),
	Limits(PerHour(20), MaxAttempts(renovate.AttemptCap)),
	Scan(renovate.ScanFailing),

	Observe("pull", "Read the pull request", pull, renovate.PullForBranch, build),
	Decide("actionable", "Is there anything to do?",
		Rule("nothing", "Nothing to do", pull.Is(renovate.NotActionable)),
		Rule("passed", "Green after our repair", All(Not(pull.Is(renovate.Failing)), pull.Is(renovate.RepairPassed)),
			Attest(renovate.TrustContext, Success, repo, head)),
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
			AskOnce(repo, Title("This update's build failed, and nothing says when it was published")).
				OnPull(number).
				With(Saying("No agent reads an update before its cooldown passes, and this one has no publish time to measure from. It needs a person.")).
				ThenAwait("a person to look at this update")),
		Rule("cooling", "Still cooling", Age.Is(Young), Await("the update's cooldown to pass")),
	),

	Hold("hold", "Hold the pull request while the fixer works", renovate.TrustContext, repo, head),
	Observe("logs", "Read the failure logs", logs, renovate.FailureLogs, pull),
	Observe("updated", "Read what the update added", updated, renovate.UpdatedLines, pull),
	Repair("fixer", "Repair the build in a sandbox", repair, renovate.Fixer,
		Input("logs", logs), Var("repo", repo), CheckoutAt(repo, head)),
	CheckPatch("check", "Check the patch by policy", repair, head, DefaultPolicy),

	Decide("decide", "What did the fixer produce?",
		Rule("empty", "A repair with no change", Checked.Is(Empty),
			failAndAsk(Title("A repair ended with no change to push"), Showing(repair, renovate.ClosingWords))...),
		Rule("refused", "Refused by the patch policy", Checked.Is(Refused),
			failAndAsk(Title("A repair was refused by the patch policy"), Showing(repair, renovate.PatchDiff))...),
		Rule("undid", "The repair took back the update", Both(repair, updated, renovate.UndoesUpdate),
			failAndAsk(Title("A repair took back the update"), Showing(repair, renovate.PatchDiff))...),
		Otherwise("push", "Push the repair",
			Push(Checked, repo, number, branch, "fix(deps): repair the build after this update").
				Holding(renovate.TrustContext)),
	),
)

// The facts a run reads. The event and the pull request are clean; the pull request's text,
// the logs, the lines the update added and the fixer's patch are hostile.
var (
	build  = EventOf(renovate.BuildChanged)
	repo   = Field[hostile.Slug](build, "repo")
	branch = Field[hostile.Slug](build, "branch")

	pull     = Fact[renovate.Pull]("pull")
	number   = Field[uint64](pull, "number")
	head     = Field[hostile.Sha](pull, "head")
	pullText = HostileField[renovate.PullText](pull, "text")

	logs    = Hostile[string]("logs")
	updated = Hostile[[]renovate.Line]("updated")
	repair  = Hostile[patch.Patch]("patch")
)

// failAndAsk fails Almanac's status on the head, and asks a person about the pull request.
func failAndAsk(question hostile.Format, body ...Body) []Effect {
	ask := Ask(repo, question).OnPull(number)
	for _, b := range body {
		ask = ask.With(b)
	}
	return []Effect{Attest(renovate.TrustContext, Failure, repo, head), ask}
}

// New is the workflow, ready for a runtime to run.
func New() (*engine.Workflow, error) {
	return engine.New(renovate.Vocabulary, Model)
}
