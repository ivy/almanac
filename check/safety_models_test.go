package check_test

// The models the Rust compile-fail cases become, for check/safety_test.go. Each function is
// named for the Rust case it stands for. They are in a file of their own for the reason
// models_test.go is.

import (
	"context"

	"github.com/ivy/almanac/agent"
	cooledpkg "github.com/ivy/almanac/cooled"
	. "github.com/ivy/almanac/dsl"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// Opened is an event with a hostile title, which Rust's fixture event also has.
type Opened struct {
	Repo  hostile.Slug       `json:"repo"`
	Title hostile.Of[string] `json:"title"`
}

func (Opened) Kind() string        { return "demo.opened" }
func (Opened) AlsoKinds() []string { return nil }
func (o *Opened) Parse(p *wire.Payload) {
	o.Repo = wire.Field[hostile.Slug](p, "/repo")
	o.Title = p.Text("/title")
}

// hands is a vocabulary with an agent of each profile, so one model can hand what one agent
// answered to another.
var hands = vocab.New()

var (
	opened    = vocab.Event[Opened](hands, "demo.opened", "a pull request was opened")
	handsPull = vocab.Observer(hands, "demo.pull", "the pull request", pullOf)
	handsAge  = vocab.PublishTime(hands, "demo.published", "when the release was published", publishedOf)
	handsFix  = vocab.Fixer[patch.Patch](hands, "repairs it", agent.NewFixer("fixer", agent.MustPrompt(agent.ParsePrompt("fix.md", "Fix it."))))
	handsRead = vocab.Reader[renovate.Review](hands, "reviews it", agent.NewQuarantined("reader", agent.MustPrompt(agent.ParsePrompt("read.md", "Review it."))))
	handsAct  = vocab.Worker[Count](hands, "counts things", agent.NewWorker("counter", agent.MustPrompt(agent.ParsePrompt("count.md", "Count."))))
)

func pullOf(context.Context, workflow.Lookup, Opened) (renovate.Pull, error) {
	return renovate.Pull{}, nil
}

func publishedOf(context.Context, workflow.Lookup, renovate.Pull) (cooledpkg.Published, error) {
	return cooledpkg.Published{}, nil
}

var (
	event      = EventOf(opened)
	eventRepo  = Field[hostile.Slug](event, "repo")
	handsPatch = Hostile[patch.Patch]("patch")
	handsRev   = Hostile[renovate.Review]("review")
	counted    = Fact[Count]("count")
)

// handsOn is a workflow over hands, with flow as its steps.
func handsOn(flow ...Part) (*vocab.Vocabulary, model.Workflow) {
	return hands, Define("hands", append([]Part{
		Intent("Hand one agent's answer to another."),
		On(opened),
		Key(eventRepo),
		May(Attests),
		Limits(PerHour(1)),
		Observe("pull", "Read the pull request", pull, handsPull, event),
	}, flow...)...)
}

func fixerOutputIsNotClean() (*vocab.Vocabulary, model.Workflow) {
	return handsOn(
		Measure("cooldown", "How old is it?", handsAge, pull, renovate.MinReleaseAge),
		Decide("cooled", "Has it cooled?", Rule("young", "Too young", Not(Age.Is(Cooled)))),
		Repair("fixer", "Repair it", handsPatch, handsFix),
		Act("count", "Count it", counted, handsAct, Input("patch", handsPatch)),
		done)
}

func readerOutputToWorker() (*vocab.Vocabulary, model.Workflow) {
	return handsOn(
		Read("review", "Review it", handsRev, handsRead, Input("text", text)),
		Act("count", "Count it", counted, handsAct, Input("review", handsRev)),
		done)
}

func rawTextToWorker() (*vocab.Vocabulary, model.Workflow) {
	return handsOn(Act("count", "Count it", counted, handsAct, Input("text", text)), done)
}

func keyOnUntrustedText() (*vocab.Vocabulary, model.Workflow) {
	v, w := handsOn(done)
	w.Key = append(w.Key, "event.title")
	return v, w
}

func subjectOnUntrustedText() (*vocab.Vocabulary, model.Workflow) {
	v, w := handsOn(done)
	w.Subjects = []model.Subject{{Kind: model.RepoSubject, Of: []model.Ref{"event.title"}}}
	return v, w
}

func keyUnknownField() model.Workflow {
	w := whole()
	w.Key = append(w.Key, "event.nope")
	return w
}

// wrongEventType reads the pull request for a build from a workflow triggered by another event.
func wrongEventType() model.Workflow {
	return Define("wrong_event", Intent("Read a build's pull request."), On(renovate.SecurityPull),
		Key(Field[hostile.Slug](EventOf(renovate.SecurityPull), "repo")), May(Attests), Limits(PerHour(1)),
		Observe("pull", "Read the pull request", pull, renovate.PullForBranch, build), done)
}

func fixerUncooled() model.Workflow {
	return repairs(readPull, measure, readLogs, fix, checkIt, pushIt)
}

func fixerPastYoungOnly() model.Workflow {
	return repairs(readPull, measure,
		Decide("age", "Too young?", Rule("young", "Too young", Age.Is(Young), Await("the cooldown"))),
		readLogs, fix, checkIt, pushIt)
}

func fixerPastEachAge() model.Workflow {
	return repairs(readPull, measure,
		Decide("ages", "Has it cooled?",
			Rule("undated", "Nothing dates it", Age.Is(Unknown), Await("a publish time")),
			Rule("young", "Too young", Age.Is(Young), Await("the cooldown"))),
		readLogs, fix, checkIt, pushIt)
}

func fixerPastEitherAge() model.Workflow {
	return repairs(readPull, measure,
		Decide("ages", "Has it cooled?", Rule("not_yet", "Not yet", Any(Age.Is(Unknown), Age.Is(Young)), Await("the cooldown"))),
		readLogs, fix, checkIt, pushIt)
}

func missingPromptField() model.Workflow {
	return repairs(readPull, measure, cooled, readLogs,
		Repair("fixer", "Repair it", repair, renovate.Fixer, Input("logs", logs)), done)
}

func promptTypoInVariable() model.Workflow {
	return repairs(readPull, measure, cooled, readLogs,
		Repair("fixer", "Repair it", repair, renovate.Fixer, Input("logs", logs), Var("rpeo", repo)), done)
}

func promptRejectsHostileText() model.Workflow {
	w := whole()
	step(w, "fixer").Agent.Vars["repo"] = "pull.text"
	return w
}

func titleFromUntrusted() model.Workflow {
	w := titleOfPull()
	step(w, "d").Decide[0].Then[0].Ask.Title.Values = []model.Ref{"pull.text"}
	return w
}

// titleNamesAVariable has a verb in its title and no value to fill it.
func titleNamesAVariable() model.Workflow {
	return repairs(readPull, Decide("d", "D", Otherwise("o", "O", Ask(repo, Title("Build {hostile} failed %d")))))
}

func unknownEffect() model.Workflow {
	w := whole()
	w.May = append(w.May, effect.KindPush+1)
	return w
}

func scanWithoutAttribute() model.Workflow {
	w := whole()
	w.Scan = "renovate.unregistered"
	return w
}

func untrustedPatchToPush() model.Workflow {
	w := whole()
	step(w, "decide").Decide[1].Then[0].Push.Checked = "patch"
	return w
}
