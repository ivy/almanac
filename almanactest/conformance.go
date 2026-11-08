package almanactest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/storetest"
	"github.com/ivy/almanac/workflow"
)

// Conformance registers the runtime's scenarios, each run against stores from fresh, so the
// runtime is shown to behave the same on every store.Store:
//
//	var _ = Describe("The runtime on the memory store", func() {
//		almanactest.Conformance(func(context.Context) storetest.Open { ... })
//	})
//
// They port the runtime scenarios of the Rust tests in crates/definitions/tests and
// almanacs/renovate/tests, named as those tests are, with workflows written by hand as
// workflow.Coded values. A spec needing PostgreSQL carries the db label, so wrap the call for
// PostgreSQL in a container labelled db.
func Conformance(fresh storetest.Fresh) {
	w := &world{}
	ginkgo.BeforeEach(func(sc ginkgo.SpecContext) {
		ctx := context.WithoutCancel(sc)
		*w = world{
			ctx:        ctx,
			open:       fresh(ctx),
			clock:      clock.NewFake(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)),
			harness:    Replying(noConcerns()),
			classifier: Answering(0.9),
			forge:      &FakeForge{},
			tracker:    &FakeTracker{},
			registry:   &fakeRegistry{},
		}
	})

	ginkgo.Describe("admission", func() { describeAdmission(w) })
	ginkgo.Describe("limits", func() { describeLimits(w) })
	ginkgo.Describe("runs", func() { describeRuns(w) })
	ginkgo.Describe("start-up", func() { describeStartup(w) })
	ginkgo.Describe("the outbox", func() { describeOutbox(w) })
	ginkgo.Describe("modes", func() { describeModes(w) })
	ginkgo.Describe("scans", func() { describeScans(w) })
}

// scope is the almanac every scenario runs as, unless it says otherwise.
const scope = "almanac"

// world is one scenario's fakes and stores.
type world struct {
	ctx        context.Context
	open       storetest.Open
	clock      *clock.Fake
	harness    *ScriptedHarness
	classifier *FixedClassifier
	forge      *FakeForge
	tracker    *FakeTracker
	registry   *fakeRegistry
}

// providers is every provider the scenarios use. Almanac acts as me.
func (w *world) providers() *providers.Set {
	set := providers.New()
	providers.Provide[registry](set, w.registry)
	providers.Provide[effect.Forge](set, w.forge)
	providers.Provide[effect.Tracker](set, w.tracker)
	providers.Provide[agent.Classifier](set, w.classifier)
	return set.UseHarness(w.harness).ActAs(me)
}

// runtime runs workflows with every provider, on the scope's store and the world's clock.
// Opening the store anew for each runtime models a restart.
func (w *world) runtime(workflows []workflow.Registered, options ...runtime.Option) *runtime.Runtime {
	return w.runtimeWith(w.providers(), workflows, options...)
}

func (w *world) runtimeWith(set *providers.Set, workflows []workflow.Registered, options ...runtime.Option) *runtime.Runtime {
	base := []runtime.Option{
		runtime.WithStore(w.open(scope)),
		runtime.WithClock(w.clock),
		runtime.WithLogger(slog.New(slog.NewTextHandler(ginkgo.GinkgoWriter, nil))),
	}
	return runtime.New(workflows, set, append(base, options...)...)
}

func (w *world) ingest(rt *runtime.Runtime, e runtime.Envelope) store.Ingest {
	ginkgo.GinkgoHelper()
	ingest, err := rt.Ingest(w.ctx, e)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return ingest
}

func (w *world) drain(rt *runtime.Runtime) []runtime.Outcome {
	ginkgo.GinkgoHelper()
	outcomes, err := rt.Drain(w.ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return outcomes
}

func (w *world) deliver(rt *runtime.Runtime) []runtime.Delivery {
	ginkgo.GinkgoHelper()
	delivered, err := rt.Deliver(w.ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return delivered
}

func (w *world) effectsOf(rt *runtime.Runtime, runID string) []store.Entry {
	ginkgo.GinkgoHelper()
	entries, err := rt.EffectsOf(w.ctx, runID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return entries
}

func (w *world) replay(rt *runtime.Runtime, runID string) runtime.Outcome {
	ginkgo.GinkgoHelper()
	outcome, err := rt.Replay(w.ctx, runID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return outcome
}

// pr is a pull request opened in repo, delivered by webhook.
func pr(delivery, repo string) runtime.Envelope {
	return runtime.Envelope{Source: "github", DeliveryID: delivery, Kind: "github.pull_request.opened", Payload: pullPayload(repo, renovateBot)}
}

// at is e at a stated chain depth.
func at(depth int, e runtime.Envelope) runtime.Envelope {
	e.Depth = depth
	return e
}

// scanned is e as a scan found it.
func scanned(e runtime.Envelope) runtime.Envelope {
	e.Origin = store.Scan
	return e
}

// report is an error reported in owner/repo.
func report(delivery string) runtime.Envelope {
	return runtime.Envelope{Source: "sentry", DeliveryID: delivery, Kind: "sentry.issue", Payload: reportPayload("owner/repo")}
}

func status(delivery, sender string) runtime.Envelope {
	return runtime.Envelope{Source: "github", DeliveryID: delivery, Kind: "github.status", Payload: statusPayload("owner/repo", sender)}
}

func suite(delivery string, id int) runtime.Envelope {
	return runtime.Envelope{Source: "github", DeliveryID: delivery, Kind: "github.check_suite.completed", Payload: suitePayload("owner/repo", id, "github-actions[bot]")}
}

// only is the outcome when there is exactly one.
func only(outcomes []runtime.Outcome) runtime.Outcome {
	ginkgo.GinkgoHelper()
	gomega.Expect(outcomes).To(gomega.HaveLen(1), "expected one outcome, got %+v", outcomes)
	return outcomes[0]
}

// ran is the run's outcome, which must have ended Ran.
func ran(outcome runtime.Outcome) runtime.Outcome {
	ginkgo.GinkgoHelper()
	gomega.Expect(outcome.Result).To(gomega.Equal(runtime.Ran), "expected a run that decided, got %+v", outcome)
	return outcome
}

func kinds(outcome runtime.Outcome) []effect.Kind {
	out := make([]effect.Kind, len(outcome.Effects))
	for i, e := range outcome.Effects {
		out[i] = e.Kind()
	}
	return out
}

func tripped(outcomes []runtime.Outcome) []runtime.Outcome {
	var out []runtime.Outcome
	for _, o := range outcomes {
		if o.Result == runtime.BudgetTripped {
			out = append(out, o)
		}
	}
	return out
}

func statuses(entries []store.Entry) []store.Status {
	out := make([]store.Status, len(entries))
	for i, e := range entries {
		out[i] = e.Status
	}
	return out
}

// codeOf is an error's diagnostic code, or empty.
func codeOf(err error) diag.Code {
	var d *diag.Error
	if errors.As(err, &d) {
		return d.Code
	}
	return ""
}

// field reads a value out of JSON by a path of keys.
func field(raw json.RawMessage, path ...string) any {
	ginkgo.GinkgoHelper()
	var value any
	gomega.Expect(json.Unmarshal(raw, &value)).To(gomega.Succeed())
	for _, key := range path {
		object, ok := value.(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue(), "no %q in %s", key, raw)
		value = object[key]
	}
	return value
}

// tagged is text as a hostile value is written in JSON, decoded as field decodes it, so a
// scenario can check text is still tagged hostile where it leaves Almanac.
func tagged(text string) any {
	ginkgo.GinkgoHelper()
	return field(mustJSON(hostile.New(text)))
}

// within is long enough for a goroutine in a scenario to reach where it waits.
const within = 10 * time.Second
