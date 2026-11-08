package check_test

// The models the specs check, written with the DSL. They are in a file of their own because the
// DSL's When, Not, Measure and Pending share names with Ginkgo's and Gomega's, and a dot import
// reaches only its own file.

import (
	"context"

	"github.com/ivy/almanac/agent"
	. "github.com/ivy/almanac/dsl"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/workflow"
)

var (
	build   = EventOf(renovate.BuildChanged)
	repo    = Field[hostile.Slug](build, "repo")
	branch  = Field[hostile.Slug](build, "branch")
	pull    = Fact[renovate.Pull]("pull")
	number  = Field[uint64](pull, "number")
	head    = Field[hostile.Sha](pull, "head")
	text    = HostileField[renovate.PullText](pull, "text")
	logs    = Hostile[string]("logs")
	repair  = Hostile[patch.Patch]("patch")
	review  = Hostile[renovate.Review]("review")
	trusted = renovate.TrustContext
)

// repairs is a workflow with the declarations most specs share, and flow as its steps.
func repairs(flow ...Part) model.Workflow {
	return Define("repairs", append([]Part{
		Intent("Repair a failed build."),
		On(renovate.BuildChanged),
		Key(repo, branch),
		May(Attests, Awaits, Asks, Pushes),
		Limits(PerHour(5), MaxAttempts(2)),
	}, flow...)...)
}

var (
	readPull = Observe("pull", "Read the pull request", pull, renovate.PullForBranch, build)
	measure  = Measure("cooldown", "How old is the release?", renovate.NewestRelease, text, renovate.MinReleaseAge)
	cooled   = Decide("cooled", "Has it cooled?", Rule("young", "Too young", Not(Age.Is(Cooled)), Await("the cooldown")))
	readLogs = Observe("logs", "Read the logs", logs, renovate.FailureLogs, pull)
	fix      = Repair("fixer", "Repair it", repair, renovate.Fixer, Input("logs", logs), Var("repo", repo), CheckoutAt(repo, head))
	checkIt  = CheckPatch("check", "Check the patch", repair, head, DefaultPolicy)
	pushIt   = Decide("decide", "Push it?",
		Rule("refused", "Refused", Checked.Is(Refused), Ask(repo, Title("Refused")).With(Showing(repair, renovate.PatchDiff))),
		Otherwise("push", "Push", Push(Checked, repo, number, branch, "fix: repair").Holding(trusted)))
	done = Decide("done", "Done", Otherwise("end", "Nothing to do"))
)

// whole is a workflow that breaks no rule.
func whole() model.Workflow {
	return repairs(readPull, measure, cooled, readLogs, fix, checkIt, pushIt)
}

func reviewed() model.Workflow {
	return repairs(readPull,
		Read("review", "Review", review, renovate.Reviewer, Input("text", text), Var("repo", repo)),
		Decide("verdict", "Verdict", Rule("concerns", "Concerns", review.Is(renovate.RaisedConcerns)), Otherwise("fine", "Fine")))
}

func readTooEarly() model.Workflow {
	return repairs(Decide("early", "Too early", Rule("nothing", "Nothing", pull.Is(renovate.NotActionable))), readPull, done)
}

func whenReadsPull() model.Workflow {
	return repairs(When(pull.Is(renovate.NotActionable)), readPull, done)
}

func whenReadsAttempts() model.Workflow { return repairs(When(Attempts.Is(Asked)), done) }

func holds() model.Workflow {
	return repairs(Hold("hold", "Hold it", trusted, repo, Field[hostile.Sha](build, "sha")), done)
}

func asksOnceThenAwaits() model.Workflow {
	return repairs(readPull, Decide("ask", "Ask", Otherwise("asked", "Ask", AskOnce(repo, Title("Look")).ThenAwait("a person"))))
}

func cleanPredicateOfHostile() model.Workflow {
	return repairs(readPull, Decide("d", "D",
		Rule("r", "R", model.Cond{Pred: renovate.NotActionable.Name(), Args: []model.Ref{"pull.text"}}),
		Otherwise("o", "O")))
}

func titleOfPull() model.Workflow {
	return repairs(readPull, Decide("d", "D", Otherwise("o", "O", Ask(repo, Title("Pull %v failed"), pull))))
}

// counts is a workflow with a worker, in a vocabulary of its own, giving it input.
func counts(input AgentOption) (*vocab.Vocabulary, model.Workflow) {
	v := vocab.New()
	event := vocab.Event[renovate.BuildEvent](v, "demo.build", "a build")
	pullOf := vocab.Observer(v, "demo.pull", "the pull request", readNothing)
	worker := vocab.Worker[Count](v, "counts things", agent.NewWorker("counter", agent.MustPrompt(agent.ParsePrompt("count.md", "Count."))))
	e := EventOf(event)
	return v, Define("counts", Intent("Count."), On(event), Key(Field[hostile.Slug](e, "repo")), May(Attests), Limits(PerHour(1)),
		Observe("pull", "Read", pull, pullOf, e),
		Act("count", "Count", Fact[Count]("count"), worker, input),
		done)
}

func workerReadsNumber() (*vocab.Vocabulary, model.Workflow) { return counts(Input("number", number)) }
func workerReadsPull() (*vocab.Vocabulary, model.Workflow)   { return counts(Input("pull", pull)) }

func promptWrongVars() model.Workflow {
	return repairs(readPull, measure, cooled, readLogs,
		Repair("fixer", "Repair it", repair, renovate.Fixer, Input("logs", logs), Var("owner", repo)), done)
}

func promptFromPull() model.Workflow {
	return repairs(readPull, measure, cooled, readLogs, Repair("fixer", "Repair it", repair, renovate.Fixer, Var("repo", pull)), done)
}

func pushWithoutCheck() model.Workflow {
	return repairs(readPull, measure, cooled, readLogs, fix, pushIt)
}

func fixerFirst() model.Workflow {
	return repairs(readPull, readLogs, fix, measure, cooled, checkIt, pushIt)
}

func endsWithObserve() model.Workflow { return repairs(readPull) }

func endsWithoutOtherwise() model.Workflow {
	return repairs(readPull, Decide("d", "D", Rule("r", "R", pull.Is(renovate.Failing))))
}

func emptyDecide() model.Workflow { return repairs(readPull, Decide("empty", "Nothing here"), done) }

func ruleAfterOtherwise() model.Workflow {
	return repairs(readPull, Decide("d", "D", Otherwise("o", "O"), Rule("r", "R", pull.Is(renovate.Failing))))
}

func stepAfterOtherwise() model.Workflow {
	return repairs(readPull, done, Decide("later", "Later", Otherwise("o", "O")))
}

func stepIDTwice() model.Workflow {
	return repairs(readPull, Decide("pull", "Again", Otherwise("o", "O")))
}

func ruleIDTwice() model.Workflow {
	return repairs(readPull, Decide("d", "D", Rule("o", "R", pull.Is(renovate.Failing)), Otherwise("o", "O")))
}

func pullTwice() model.Workflow {
	return repairs(readPull, Observe("again", "Read it again", pull, renovate.PullForBranch, build), done)
}

func measureTwice() model.Workflow {
	return repairs(readPull, measure, Measure("again", "Again", renovate.NewestRelease, text, renovate.MinReleaseAge), done)
}

func unlabelled() model.Workflow { return repairs(readPull, Decide("d", "", Otherwise("", "O"))) }

func bare() model.Workflow { return Define("bare", On(renovate.BuildChanged), May(Attests), done) }

func testsSpent() model.Workflow {
	return repairs(Decide("d", "D", Rule("spent", "Spent", Attempts.Is(Spent)), Otherwise("o", "O")))
}

func factNamedAge() model.Workflow {
	return repairs(Observe("pull", "Read", Fact[renovate.Pull]("age"), renovate.PullForBranch, build), done)
}

// Count is a clean answer.
type Count struct {
	N int `json:"n"`
}

func readNothing(context.Context, workflow.Lookup, renovate.BuildEvent) (renovate.Pull, error) {
	return renovate.Pull{}, nil
}
