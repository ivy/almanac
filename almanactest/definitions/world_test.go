package definitions_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/almanactest/definitions"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/internal/pgtest"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/memory"
	"github.com/ivy/almanac/store/postgres"
	"github.com/ivy/almanac/store/storetest"
	"github.com/ivy/almanac/workflow"
)

// The Rust specification is crates/definitions/tests: workflows.rs, policy.rs and
// workflows_on_postgres.rs for the samples themselves, origins.rs for a scan's key and what a
// scan needs, and broker_flow.rs for the samples behind the broker and the deliveries log. The
// runtime scenarios those files hold run against these samples in almanactest.Conformance.

const (
	repo = "owner/repo"
	head = "0123456789abcdef0123456789abcdef01234567"
	// fixture is a real pull_request.opened delivery, kept by the Rust implementation.
	fixture = "../../crates/definitions/fixtures/renovate-pr-opened.json"
	// injection is text an attacker controls.
	injection = definitions.Injection
	opened    = "github.pull_request.opened"
)

// onMemory and onPostgres are fresh stores of each kind, for specs that run on both.
func onMemory(context.Context) storetest.Open {
	scopes := map[string]*memory.Store{}
	return func(scope string) store.Store {
		if scopes[scope] == nil {
			scopes[scope] = memory.New()
		}
		return scopes[scope]
	}
}

func onPostgres(ctx context.Context) storetest.Open {
	pool, err := pgxpool.New(ctx, pgtest.Database())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(pool.Close)
	Expect(postgres.Migrate(ctx, pool)).To(Succeed())
	return func(scope string) store.Store { return postgres.New(pool, scope) }
}

// world is what a spec runs the samples in: a store, and fakes for everything outside.
type world struct {
	ctx          context.Context
	open         storetest.Open
	registry     *definitions.FakeRegistry
	reviewer     *almanactest.ScriptedHarness
	investigator *almanactest.ScriptedHarness
	classifier   *almanactest.FixedClassifier
	forge        *almanactest.FakeForge
	tracker      *almanactest.FakeTracker
}

// samples registers specs that run the samples on the stores fresh makes.
func samples(fresh storetest.Fresh, specs ...func(*world)) {
	w := &world{}
	BeforeEach(func(sc SpecContext) {
		ctx := context.WithoutCancel(sc)
		*w = world{
			ctx:          ctx,
			open:         fresh(ctx),
			registry:     &definitions.FakeRegistry{},
			reviewer:     almanactest.Replying(review("NoConcerns")),
			investigator: almanactest.Replying(findings("High")),
			classifier:   almanactest.Answering(0.9),
			forge:        &almanactest.FakeForge{},
			tracker:      &almanactest.FakeTracker{},
		}
	})
	for _, describe := range specs {
		describe(w)
	}
}

// review is a reviewer's answer, with notes an attacker shaped.
func review(verdict string) string {
	return string(mustJSON(map[string]any{"verdict": verdict, "notes": injection}))
}

// findings is an investigator's answer, with a summary an attacker shaped.
func findings(confidence string) string {
	return string(mustJSON(map[string]any{"confidence": confidence, "summary": "A nil map in the handler. " + injection}))
}

// providers are the world's fakes.
func (w *world) providers() *providers.Set {
	set := providers.New()
	providers.Provide[definitions.Registry](set, w.registry)
	providers.Provide[effect.Forge](set, w.forge)
	providers.Provide[effect.Tracker](set, w.tracker)
	providers.Provide[agent.Classifier](set, w.classifier)
	return set.UseHarnessFor("reviewer", w.reviewer).UseHarnessFor("investigator", w.investigator)
}

// runtime runs models with the world's fakes, on the world's store.
func (w *world) runtime(models ...model.Workflow) *runtime.Runtime {
	GinkgoHelper()
	return runtime.New(runnable(models...), w.providers(),
		runtime.WithStore(w.open("definitions")),
		runtime.WithLogger(logger()),
	)
}

// runnable is each model, run by the engine.
func runnable(models ...model.Workflow) []workflow.Registered {
	GinkgoHelper()
	registered := make([]workflow.Registered, len(models))
	for i, m := range models {
		w, err := definitions.Runnable(m)
		Expect(err).NotTo(HaveOccurred())
		registered[i] = w
	}
	return registered
}

func logger() *slog.Logger { return slog.New(slog.NewTextHandler(GinkgoWriter, nil)) }

func (w *world) ingest(rt *runtime.Runtime, e runtime.Envelope) store.Ingest {
	GinkgoHelper()
	ingest, err := rt.Ingest(w.ctx, e)
	Expect(err).NotTo(HaveOccurred())
	return ingest
}

func (w *world) drain(rt *runtime.Runtime) []runtime.Outcome {
	GinkgoHelper()
	outcomes, err := rt.Drain(w.ctx)
	Expect(err).NotTo(HaveOccurred())
	return outcomes
}

// decided ingests e alone and returns what its one run decided.
func (w *world) decided(rt *runtime.Runtime, e runtime.Envelope) runtime.Outcome {
	GinkgoHelper()
	Expect(w.ingest(rt, e)).To(Equal(store.Queued))
	outcomes := w.drain(rt)
	Expect(outcomes).To(HaveLen(1))
	Expect(outcomes[0].Result).To(Equal(runtime.Ran), outcomes[0].Reason)
	return outcomes[0]
}

// pr is a Renovate pull request opened in repo, delivered by webhook.
func pr(delivery, repo string) runtime.Envelope {
	return opening(delivery, mustJSON(map[string]any{
		"number":     1,
		"sender":     map[string]any{"login": "renovate[bot]", "type": "Bot"},
		"repository": map[string]any{"full_name": repo},
		"pull_request": map[string]any{
			"state": "open", "draft": false, "title": "chore(deps): bump", "body": injection,
			"user": map[string]any{"login": "renovate[bot]"},
			"head": map[string]any{"ref": "renovate/ts", "sha": head},
		},
	}))
}

// opening is a pull request opened, with payload as its body.
func opening(delivery string, payload json.RawMessage) runtime.Envelope {
	return runtime.Envelope{Source: "github", DeliveryID: delivery, Kind: opened, Payload: payload}
}

// issue is an application error in owner/repo, with a message an attacker wrote.
func issue(delivery string) runtime.Envelope {
	return runtime.Envelope{Source: "sentry", DeliveryID: delivery, Kind: "sentry.issue",
		Payload: mustJSON(map[string]any{"issue_id": 7, "repo": repo, "message": injection})}
}

// edited is the real delivery with the value at path replaced.
func edited(path []string, value any) json.RawMessage {
	GinkgoHelper()
	var payload map[string]any
	Expect(json.Unmarshal(renovateDelivery(), &payload)).To(Succeed())
	at := payload
	for _, step := range path[:len(path)-1] {
		at = at[step].(map[string]any)
	}
	at[path[len(path)-1]] = value
	return mustJSON(payload)
}

// renovateDelivery is the real pull_request.opened delivery.
func renovateDelivery() json.RawMessage {
	GinkgoHelper()
	raw, err := os.ReadFile(fixture)
	Expect(err).NotTo(HaveOccurred())
	return raw
}

// summaries are the effects of a run, as a guard sees them.
func summaries(outcome runtime.Outcome) []effect.Summary {
	out := make([]effect.Summary, len(outcome.Effects))
	for i, e := range outcome.Effects {
		out[i] = effect.Summarize(e)
	}
	return out
}

// endingsOf is every ending of w, as step/rule.
func endingsOf(w model.Workflow) []string {
	var all []string
	for _, e := range w.Endings() {
		all = append(all, e.Step+"/"+e.Rule)
	}
	return all
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
