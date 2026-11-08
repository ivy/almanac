package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/engine"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/internal/fixture/sample"
	"github.com/ivy/almanac/internal/fixture/workflows"
	"github.com/ivy/almanac/internal/pgtest"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/memory"
	"github.com/ivy/almanac/store/postgres"
	"github.com/ivy/almanac/store/storetest"
	"github.com/ivy/almanac/workflow"
)

var _ = Describe("Models run by the runtime, on the memory store", func() {
	throughTheRuntime(func(context.Context) storetest.Open {
		scopes := map[string]*memory.Store{}
		return func(scope string) store.Store {
			if scopes[scope] == nil {
				scopes[scope] = memory.New()
			}
			return scopes[scope]
		}
	})
})

var _ = Describe("Models run by the runtime, on PostgreSQL", Label("db"), func() {
	throughTheRuntime(func(ctx context.Context) storetest.Open {
		pool, err := pgxpool.New(ctx, pgtest.Database())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(pool.Close)
		Expect(postgres.Migrate(ctx, pool)).To(Succeed())
		return func(scope string) store.Store { return postgres.New(pool, scope) }
	})
})

// almanac is the scope every spec's runtime stores its rows under.
var almanac = effect.Live.Scope("engine")

// world is what one spec runs models in: a store, a clock, and fakes for everything outside.
type world struct {
	ctx        context.Context
	open       storetest.Open
	clock      *clock.Fake
	github     *renovate.Fake
	forge      *almanactest.FakeForge
	tracker    *almanactest.FakeTracker
	classifier *almanactest.FixedClassifier
	fixer      *almanactest.ScriptedHarness
	reader     *almanactest.ScriptedHarness
}

// workflows are the models every runtime here runs, made runnable.
func (w *world) workflows() []workflow.Registered {
	GinkgoHelper()
	bf, err := engine.New(renovate.Vocabulary, workflows.BuildFailures)
	Expect(err).NotTo(HaveOccurred())
	et, err := engine.New(sample.Vocabulary, workflows.ErrorTriage)
	Expect(err).NotTo(HaveOccurred())
	return []workflow.Registered{bf, et}
}

// providers are the world's fakes.
func (w *world) providers() *providers.Set {
	set := providers.New()
	providers.Provide[renovate.GitHub](set, w.github)
	providers.Provide[effect.Forge](set, w.forge)
	providers.Provide[effect.Tracker](set, w.tracker)
	providers.Provide[agent.Classifier](set, w.classifier)
	return set.UseHarnessFor("fixer", w.fixer).UseHarnessFor("investigator", w.reader)
}

// runtime is a live runtime with set, on the world's store and clock. Opening the store again
// for each runtime is a restart.
func (w *world) runtime(set *providers.Set) *runtime.Runtime {
	return runtime.New(w.workflows(), set,
		runtime.WithStore(w.open(almanac)),
		runtime.WithClock(w.clock),
		runtime.InMode(effect.Live),
		runtime.WithLogger(slog.New(slog.NewTextHandler(GinkgoWriter, nil))),
	)
}

// settle ingests e and settles, and returns the outcome of the one run it made.
func (w *world) settle(rt *runtime.Runtime, e runtime.Envelope) runtime.Outcome {
	GinkgoHelper()
	_, err := rt.Ingest(w.ctx, e)
	Expect(err).NotTo(HaveOccurred())
	outcomes, err := rt.Settle(w.ctx)
	Expect(err).NotTo(HaveOccurred())
	Expect(outcomes).To(HaveLen(1))
	return outcomes[0]
}

// replayDown replays a run after a restart in which every provider fails every call.
func (w *world) replayDown(runID string) (runtime.Outcome, *down, *renovate.Fake) {
	GinkgoHelper()
	failing, github := &down{}, renovate.Down()
	set := providers.New()
	providers.Provide[renovate.GitHub](set, github)
	providers.Provide[effect.Forge](set, failing)
	providers.Provide[effect.Tracker](set, failing)
	providers.Provide[agent.Classifier](set, failing)
	set.UseHarness(failing)
	outcome, err := w.runtime(set).Replay(w.ctx, runID)
	Expect(err).NotTo(HaveOccurred())
	return outcome, failing, github
}

// built is CI failing on the Renovate branch, delivered by webhook as delivery.
func built(delivery string) runtime.Envelope {
	return runtime.Envelope{Source: "github", DeliveryID: delivery, Kind: "github.check_suite.completed", Payload: buildFailed}
}

// reported is the error report, delivered by the error tracker.
func reported(delivery string) runtime.Envelope {
	return runtime.Envelope{Source: "sentry", DeliveryID: delivery, Kind: "sentry.issue", Payload: errorReported}
}

func throughTheRuntime(fresh storetest.Fresh) {
	w := &world{}
	BeforeEach(func(sc SpecContext) {
		ctx := context.WithoutCancel(sc)
		*w = world{
			ctx:   ctx,
			open:  fresh(ctx),
			clock: clock.NewFake(now),
			github: renovate.Answering(renovate.Reads{
				Pull:      failingPull(),
				Logs:      hostile.New("npm ERR! " + injection),
				Lines:     hostile.New([]renovate.Line{{Path: "package.json", Text: `"tar": "7.0.0"`}}),
				Published: cooled.PublishedAt(now.Add(-30 * 24 * time.Hour)),
			}),
			forge:      &almanactest.FakeForge{},
			tracker:    &almanactest.FakeTracker{},
			classifier: almanactest.Answering(0.9),
			fixer:      almanactest.Replying(fixed),
			reader:     almanactest.Replying(`{"confidence": "high", "summary": "A nil map in the handler. ` + injection + `"}`),
		}
	})

	It("repairs a failed build: holds the pull request, has the fixer repair it, and pushes the repair", func() {
		outcome := w.settle(w.runtime(w.providers()), built("d-1"))
		Expect(outcome.Result).To(Equal(runtime.Ran), outcome.Reason)
		Expect(outcome.Workflow).To(Equal("build_failures"))
		Expect(planned(outcome.Effects)).To(MatchJSON(`[{"Push": {"repo": "` + repo + `", "pull": 7, "branch": "` + branch + `",
			"message": "fix(deps): repair the build after this update", "hold": "almanac/trust", "base": "` + sha + `",
			"patch": {"$untrusted": {"changes": [{"Write": {"path": "src/main.go", "content": "eA==", "executable": false}}], "diff": "the fix"}}}}]`))

		Expect(w.forge.Statuses()).To(Equal([]almanactest.Status{{On: held, State: effect.Pending}}))
		Expect(w.forge.Pushes()).To(HaveLen(1))
		Expect(w.forge.Pushes()[0].Commit.Branch.String()).To(Equal(branch))
		Expect(w.fixer.Calls()).To(Equal(1))
		Expect(w.fixer.Grants()[0].Write).To(BeTrue())
		Expect(w.fixer.Inputs()[0]).To(MatchJSON(`{"logs": {"$untrusted": "npm ERR! ` + injection + `"}, "$checkout": {"repo": "` + repo + `", "sha": "` + sha + `"}}`))
		Expect(w.github.Calls()).To(Equal(4), "the pull request, the publish time, the logs and the updated lines")

		digest, ok, err := w.open(almanac).Recorded(w.ctx, outcome.RunID, "model", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(digest).To(MatchJSON(`"` + w.workflows()[0].Manifest().Digest + `"`))
	})

	It("replays a repair with providers that fail every call, and reaches the same push", func() {
		first := w.settle(w.runtime(w.providers()), built("d-1"))
		Expect(first.Result).To(Equal(runtime.Ran), first.Reason)

		again, failing, github := w.replayDown(first.RunID)
		Expect(again.Result).To(Equal(runtime.Ran), again.Reason)
		Expect(again.RunID).To(Equal(first.RunID))
		Expect(planned(again.Effects)).To(MatchJSON(planned(first.Effects)))
		Expect(failing.called()).To(BeEmpty(), "a replay calls no forge, tracker, classifier or agent")
		Expect(github.Calls()).To(BeZero(), "a replay reads nothing from GitHub")
		Expect(w.forge.Pushes()).To(HaveLen(1), "a replay applies nothing")
	})

	It("stops repairing once a key's attempts are spent, and asks a person once", func() {
		rt := w.runtime(w.providers())
		for i := range 3 {
			outcome := w.settle(rt, built(fmt.Sprintf("d-%d", i)))
			Expect(outcome.Result).To(Equal(runtime.Ran), outcome.Reason)
			Expect(outcome.Effects).To(ConsistOf(BeAssignableToTypeOf(effect.Push{})))
			w.clock.Advance(3 * time.Hour)
		}
		exhausted := w.settle(rt, built("d-3"))
		Expect(planned(exhausted.Effects)).To(MatchJSON(`[` + failTrust + `, ` + ask("Repairs did not fix this build", "") + `]`))
		Expect(w.tracker.Filed()).To(ConsistOf(HaveField("Title", "Repairs did not fix this build")))

		w.clock.Advance(3 * time.Hour)
		asked := w.settle(rt, built("d-4"))
		Expect(asked.Result).To(Equal(runtime.Ran))
		Expect(asked.Effects).To(BeEmpty(), "a person was asked already")
		Expect(w.fixer.Calls()).To(Equal(3))

		again, failing, _ := w.replayDown(exhausted.RunID)
		Expect(planned(again.Effects)).To(MatchJSON(planned(exhausted.Effects)))
		Expect(failing.called()).To(BeEmpty())
	})

	It("files an error the investigator is sure of, and replays it with no model to call", func() {
		outcome := w.settle(w.runtime(w.providers()), reported("e-1"))
		Expect(outcome.Result).To(Equal(runtime.Ran), outcome.Reason)
		Expect(outcome.Workflow).To(Equal("error_triage"))
		Expect(planned(outcome.Effects)).To(MatchJSON(filed("File", "Fix this application error")))
		Expect(w.tracker.Filed()).To(ConsistOf(almanactest.Filed{Key: w.tracker.Filed()[0].Key, Target: repo, Title: "Fix this application error"}))
		Expect(w.classifier.Calls()).To(Equal(1))
		Expect(w.reader.Inputs()[0]).To(MatchJSON(`{"message": {"$untrusted": "TypeError: ` + injection + `"}}`))

		again, failing, _ := w.replayDown(outcome.RunID)
		Expect(again.Result).To(Equal(runtime.Ran), again.Reason)
		Expect(planned(again.Effects)).To(MatchJSON(planned(outcome.Effects)))
		Expect(failing.called()).To(BeEmpty())
	})

	It("stops at the screen, before the investigator, and replays the stop", func() {
		w.classifier = almanactest.Answering(0.2)
		outcome := w.settle(w.runtime(w.providers()), reported("e-1"))
		Expect(outcome.Result).To(Equal(runtime.Stopped))
		Expect(outcome.Reason).To(Equal("below screen threshold"))
		Expect(w.reader.Calls()).To(BeZero())
		Expect(w.tracker.Filed()).To(BeEmpty())

		again, failing, _ := w.replayDown(outcome.RunID)
		Expect(again.Result).To(Equal(runtime.Stopped))
		Expect(failing.called()).To(BeEmpty())
	})

	It("fails a run whose read fails, and records nothing it did not read", func() {
		failing := &down{}
		set := w.providers()
		providers.Provide[renovate.GitHub](set, renovate.Down())
		set.UseHarnessFor("fixer", failing)
		outcome := w.settle(w.runtime(set), built("d-1"))
		Expect(outcome.Result).To(Equal(runtime.Failed))
		Expect(outcome.Reason).To(ContainSubstring(`build_failures, step "pull"`))
		Expect(outcome.Reason).NotTo(ContainSubstring("IGNORE"))
		Expect(failing.called()).To(BeEmpty())
	})

	It("checks at start-up that every provider and harness the models need is set up", func() {
		err := runtime.New(w.workflows(), providers.New(), runtime.InMode(effect.Live)).Validate()
		Expect(err).To(MatchError(And(
			ContainSubstring("build_failures needs a provider for renovate.GitHub"),
			ContainSubstring("build_failures needs a harness for agent fixer"),
			ContainSubstring("error_triage needs a provider for agent.Classifier"),
			ContainSubstring("error_triage needs a harness for agent investigator"),
		)))
		Expect(w.runtime(w.providers()).Validate()).To(Succeed())
	})
}

// down is a forge, tracker, classifier and harness that fails every call, and keeps what was
// called.
type down struct {
	mu    sync.Mutex
	calls []string
}

var errDown = errors.New("down")

func (d *down) call(name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, name)
	return errDown
}

func (d *down) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *down) Attest(context.Context, string, effect.Attest) error { return d.call("Attest") }
func (d *down) Push(context.Context, string, effect.Commit) error   { return d.call("Push") }
func (d *down) File(context.Context, string, effect.Issue) error    { return d.call("File") }
func (d *down) Ask(context.Context, string, effect.Question) error  { return d.call("Ask") }

func (d *down) YesProbability(context.Context, agent.Pass, string, json.RawMessage) (float64, error) {
	return 0, d.call("YesProbability")
}

func (d *down) Run(context.Context, agent.Pass, string, agent.Grant, json.RawMessage) (json.RawMessage, error) {
	return nil, d.call("Run")
}
