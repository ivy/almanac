package almanactest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/workflow"
)

// countingGuard keeps what it was shown, and refuses filing work when told to.
type countingGuard struct {
	mu         sync.Mutex
	seen       []string
	denyFiling bool
}

func (g *countingGuard) Review(_ context.Context, intent string, actions []effect.Summary) ([]bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seen = append(g.seen, fmt.Sprintf("%s %+v", intent, actions))
	verdicts := make([]bool, len(actions))
	for i, a := range actions {
		verdicts[i] = !g.denyFiling || a.Kind != effect.KindFile
	}
	return verdicts, nil
}

// describeRuns is what a run reaches through its Cx, what it may decide, and replay (R-9,
// R-12, R-19, R-22, principle 7).
func describeRuns(w *world) {
	updates := []workflow.Registered{dependencyUpdates}
	triage := []workflow.Registered{errorTriage}

	ginkgo.It("replay reuses recorded results and calls nothing again", func() {
		rt := w.runtime(updates)
		w.ingest(rt, pr("d1", "owner/repo"))
		first := ran(only(w.drain(rt)))
		w.harness.Reply(`{"verdict": "Concerns", "notes": "a different answer"}`)

		again := ran(w.replay(rt, first.RunID))
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(1))
		gomega.Expect(w.registry.reads()).To(gomega.Equal(1))
		gomega.Expect(kinds(again)).To(gomega.Equal(kinds(first)))
		gomega.Expect(again.Effects).To(gomega.Equal(first.Effects))
	})

	ginkgo.It("replay after a restart reuses the recorded model call", func() {
		before := w.runtime(updates)
		w.ingest(before, pr("d1", "owner/repo"))
		first := ran(only(w.drain(before)))

		after := w.runtime(updates)
		ran(w.replay(after, first.RunID))
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(1), "a replay must not call the model again")
	})

	ginkgo.It("replay of a stopped run does not ask the classifier again", func() {
		w.classifier = Answering(0.2)
		rt := w.runtime(triage)
		w.ingest(rt, report("s1"))
		first := only(w.drain(rt))
		gomega.Expect(first.Result).To(gomega.Equal(runtime.Stopped))

		gomega.Expect(w.replay(rt, first.RunID).Result).To(gomega.Equal(runtime.Stopped))
		gomega.Expect(w.classifier.Calls()).To(gomega.Equal(1))
		gomega.Expect(w.harness.Calls()).To(gomega.BeZero())
	})

	ginkgo.It("the screen stops a run before any agent starts", func() {
		w.classifier = Answering(0.2)
		rt := w.runtime(triage)
		w.ingest(rt, report("s1"))
		gomega.Expect(only(w.drain(rt))).To(gomega.HaveField("Reason", "below screen threshold"))
		gomega.Expect(w.classifier.Calls()).To(gomega.Equal(1))
		gomega.Expect(w.harness.Calls()).To(gomega.BeZero())
	})

	ginkgo.It("a filed bug targets the repo from the event, not the model", func() {
		w.harness.Reply(`{"repo": "attacker/elsewhere"}`)
		rt := w.runtime(triage)
		w.ingest(rt, report("s1"))
		filed := ran(only(w.drain(rt))).Effects
		gomega.Expect(filed).To(gomega.ConsistOf(gomega.HaveField("Target", hostile.MustParseSlug("owner/repo"))))
	})

	ginkgo.It("the guard runs once per run, sees no event text, and is recorded", func() {
		w.registry.ciRed = true
		guard := &countingGuard{denyFiling: true}
		rt := w.runtime(updates, runtime.Guarded(guard))
		w.ingest(rt, pr("d1", "owner/repo"))
		outcome := ran(only(w.drain(rt)))
		gomega.Expect(kinds(outcome)).To(gomega.Equal([]effect.Kind{effect.KindAttest}))
		gomega.Expect(outcome.Denied).To(gomega.ConsistOf(effect.Summary{Kind: effect.KindFile}))

		w.replay(rt, outcome.RunID)
		gomega.Expect(guard.seen).To(gomega.HaveLen(1), "one call per run, and none on replay")
		gomega.Expect(guard.seen[0]).To(gomega.HavePrefix(dependencyUpdates.Intent()))
		gomega.Expect(guard.seen[0]).NotTo(gomega.ContainSubstring("IGNORE PREVIOUS"))
	})

	ginkgo.It("an effect the workflow did not declare is denied", func() {
		undeclared := workflow.Code[pullRequest](workflow.Coded[pullRequest, pullRequest]{
			Name: "overreaches", Intent: "Wait, then try to attest.", RunsPerHour: 1,
			May:    []effect.Kind{effect.KindAwait},
			Key:    func(pr pullRequest) string { return pr.Repo.String() },
			Gather: func(_ context.Context, _ workflow.Cx, pr pullRequest) (pullRequest, error) { return pr, nil },
			Decide: func(pr pullRequest) []effect.Effect {
				return []effect.Effect{
					effect.Attest{Context: "almanac/trust", State: effect.Success, Repo: pr.Repo, Sha: pr.Head},
					effect.Await{Until: "a person"},
				}
			},
		})
		rt := w.runtime([]workflow.Registered{undeclared}, runtime.InMode(effect.Live))
		w.ingest(rt, pr("d1", "owner/repo"))
		outcome := ran(only(w.drain(rt)))
		gomega.Expect(kinds(outcome)).To(gomega.Equal([]effect.Kind{effect.KindAwait}))
		gomega.Expect(outcome.Denied).To(gomega.ConsistOf(effect.Summary{Kind: effect.KindAttest, Attests: effect.Success}))
		gomega.Expect(w.effectsOf(rt, outcome.RunID)).To(gomega.HaveLen(1))
		w.deliver(rt)
		gomega.Expect(w.forge.Calls()).To(gomega.BeEmpty())
	})

	ginkgo.It("a push whose patch no policy checked fails the run and records nothing", func() {
		unchecked := workflow.Code[pullRequest](workflow.Coded[pullRequest, pullRequest]{
			Name: "pushes-unchecked", Intent: "Push without a check.", RunsPerHour: 1,
			May:    []effect.Kind{effect.KindPush},
			Key:    func(pr pullRequest) string { return pr.Repo.String() },
			Gather: func(_ context.Context, _ workflow.Cx, pr pullRequest) (pullRequest, error) { return pr, nil },
			Decide: func(pr pullRequest) []effect.Effect {
				return []effect.Effect{effect.Push{Repo: pr.Repo, Pull: pr.Number, Branch: pr.Branch, Message: "fix"}}
			},
		})
		rt := w.runtime([]workflow.Registered{unchecked}, runtime.InMode(effect.Live))
		w.ingest(rt, pr("d1", "owner/repo"))
		outcome := only(w.drain(rt))
		gomega.Expect(outcome.Result).To(gomega.Equal(runtime.Failed))
		gomega.Expect(outcome.Reason).To(gomega.ContainSubstring("patch.Policy.Check"))
		gomega.Expect(w.effectsOf(rt, outcome.RunID)).To(gomega.BeEmpty())
	})

	ginkgo.It("an agent hidden in a helper fails the run instead of escaping the manifest", func() {
		rt := w.runtime([]workflow.Registered{hidesItsAgent})
		w.ingest(rt, pr("d1", "owner/repo"))
		outcome := only(w.drain(rt))
		gomega.Expect(outcome.Result).To(gomega.Equal(runtime.Failed))
		gomega.Expect(outcome.Reason).To(gomega.ContainSubstring("not declared"))
		gomega.Expect(w.harness.Calls()).To(gomega.BeZero())
	})

	ginkgo.It("the reader is quarantined and taint survives to the provider", func() {
		rt := w.runtime(updates)
		w.ingest(rt, pr("d1", "owner/repo"))
		w.drain(rt)
		gomega.Expect(w.harness.Grants()).To(gomega.Equal([]agent.Grant{{}}))
		gomega.Expect(field(w.harness.Inputs()[0], "diff")).To(gomega.Equal(tagged(injection)))
	})

	ginkgo.It("hostile text is redacted from debug output", func() {
		w.registry.checksFail = true
		rt := w.runtime(updates)
		w.ingest(rt, pr("d1", "owner/repo"))
		outcomes := w.drain(rt)
		gomega.Expect(fmt.Sprintf("%v %+v %#v", outcomes, outcomes, outcomes)).NotTo(gomega.ContainSubstring("IGNORE PREVIOUS"))
	})

	ginkgo.It("workflows connect through shared subjects", func() {
		rt := w.runtime([]workflow.Registered{dependencyUpdates, errorTriage})
		w.ingest(rt, pr("d1", "owner/repo"))
		w.ingest(rt, report("s1"))
		w.drain(rt)
		repo := hostile.MustParseSlug("owner/repo")
		gomega.Expect(rt.RunsFor(w.ctx, workflow.RepoSubject(repo))).To(gomega.HaveLen(2))
		gomega.Expect(rt.RunsFor(w.ctx, workflow.PackageSubject(repo, lodash))).To(gomega.HaveLen(1))
	})

	ginkgo.Describe("a cooldown", func() {
		effectsAt := func(published cooled.Published) []effect.Kind {
			ginkgo.GinkgoHelper()
			set := providers.Provide(w.providers(), published)
			rt := w.runtimeWith(set, []workflow.Registered{cooldownGate})
			w.ingest(rt, pr("d1", "owner/repo"))
			return kinds(ran(only(w.drain(rt))))
		}
		day := 24 * time.Hour

		ginkgo.It("a release newer than the declared age yields await", func() {
			gomega.Expect(effectsAt(cooled.PublishedAt(w.clock.Now().Add(-day)))).To(gomega.Equal([]effect.Kind{effect.KindAwait}))
		})

		ginkgo.It("a missing timestamp yields await", func() {
			gomega.Expect(effectsAt(cooled.Published{})).To(gomega.Equal([]effect.Kind{effect.KindAwait}))
		})

		ginkgo.It("an old enough release yields no wait", func() {
			gomega.Expect(effectsAt(cooled.PublishedAt(w.clock.Now().Add(-4 * day)))).To(gomega.BeEmpty())
		})

		ginkgo.It("is measured again on replay from the recorded times, however late the replay", func() {
			set := providers.Provide(w.providers(), cooled.PublishedAt(w.clock.Now().Add(-day)))
			rt := w.runtimeWith(set, []workflow.Registered{cooldownGate})
			w.ingest(rt, pr("d1", "owner/repo"))
			first := ran(only(w.drain(rt)))
			w.clock.Advance(30 * day)
			gomega.Expect(kinds(w.replay(rt, first.RunID))).To(gomega.Equal([]effect.Kind{effect.KindAwait}))
		})
	})

	ginkgo.Describe("a repair", func() {
		repairWith := func(changes string) []effect.Kind {
			ginkgo.GinkgoHelper()
			w.harness.Reply(fmt.Sprintf(`{"changes": %s, "diff": "the diff"}`, changes))
			rt := w.runtime([]workflow.Registered{repairBuild}, runtime.InMode(effect.Live))
			w.ingest(rt, pr("d1", "owner/repo"))
			decided := kinds(ran(only(w.drain(rt))))
			w.deliver(rt)
			return decided
		}
		writeTo := func(path string) string {
			return fmt.Sprintf(`{"Write": {"path": %q, "content": "eA==", "executable": false}}`, path)
		}

		ginkgo.It("an injected agent editing CI is rejected and nothing is pushed", func() {
			gomega.Expect(repairWith("[" + writeTo(".github/workflows/ci.yml") + "]")).To(gomega.Equal([]effect.Kind{effect.KindAsk}))
			gomega.Expect(w.forge.Pushes()).To(gomega.BeEmpty())
		})

		ginkgo.It("a clean repair is pushed to the pull request's own branch", func() {
			gomega.Expect(repairWith("[" + writeTo("src/main.rs") + "," + writeTo("Cargo.lock") + "]")).To(gomega.Equal([]effect.Kind{effect.KindPush}))
			pushed := w.forge.Pushes()
			gomega.Expect(pushed).To(gomega.HaveLen(1))
			gomega.Expect(pushed[0].Commit.Branch.String()).To(gomega.Equal("renovate/ts"))
			gomega.Expect(pushed[0].Commit.Repo.String()).To(gomega.Equal("owner/repo"))
			gomega.Expect(pushed[0].Commit.Base.String()).To(gomega.Equal(head))
		})

		ginkgo.It("the fixer is given write and nothing else, only once the update has cooled", func() {
			repairWith("[" + writeTo("src/main.rs") + "]")
			gomega.Expect(w.harness.Grants()).To(gomega.Equal([]agent.Grant{{Write: true}}))
		})
	})

	ginkgo.Describe("a hold", func() {
		ginkgo.It("pending is posted before any other work, and the final status replaces it", func() {
			rt := w.runtime([]workflow.Registered{holdsThenDecides}, runtime.InMode(effect.Live))
			w.ingest(rt, pr("d1", "owner/repo"))
			outcome := ran(only(w.drain(rt)))

			on := "owner/repo@" + head + ":almanac/review"
			gomega.Expect(w.forge.Statuses()).To(gomega.Equal([]Status{{On: on, State: effect.Pending}}))
			gomega.Expect(w.forge.Calls()).To(gomega.Equal([]string{outcome.RunID + ":hold:almanac/review"}))
			w.deliver(rt)
			gomega.Expect(w.forge.Statuses()).To(gomega.Equal([]Status{{On: on, State: effect.Success}}))
		})

		ginkgo.It("is recorded, so a replay posts nothing", func() {
			rt := w.runtime([]workflow.Registered{holdsThenDecides}, runtime.InMode(effect.Live))
			w.ingest(rt, pr("d1", "owner/repo"))
			outcome := ran(only(w.drain(rt)))
			w.replay(rt, outcome.RunID)
			gomega.Expect(w.forge.Calls()).To(gomega.HaveLen(1))
		})

		ginkgo.It("posts nothing in a dry run", func() {
			rt := w.runtime([]workflow.Registered{holdsThenDecides})
			w.ingest(rt, pr("d1", "owner/repo"))
			ran(only(w.drain(rt)))
			gomega.Expect(w.forge.Calls()).To(gomega.BeEmpty())
		})

		ginkgo.It("needs the workflow to declare it may Attest", func() {
			holdsUndeclared := workflow.Code[pullRequest](workflow.Coded[pullRequest, struct{}]{
				Name: "holds-undeclared", Intent: "Hold without saying so.", RunsPerHour: 1,
				May: []effect.Kind{effect.KindAwait},
				Key: func(pr pullRequest) string { return pr.Repo.String() },
				Gather: func(ctx context.Context, cx workflow.Cx, pr pullRequest) (struct{}, error) {
					return struct{}{}, cx.Hold(ctx, "almanac/review", pr.Repo, pr.Head)
				},
				Decide: func(struct{}) []effect.Effect { return nil },
			})
			rt := w.runtime([]workflow.Registered{holdsUndeclared}, runtime.InMode(effect.Live))
			w.ingest(rt, pr("d1", "owner/repo"))
			gomega.Expect(only(w.drain(rt))).To(gomega.HaveField("Reason", gomega.ContainSubstring("may not Attest")))
			gomega.Expect(w.forge.Calls()).To(gomega.BeEmpty())
		})
	})

	ginkgo.It("a run interrupted by a crash runs again after a restart", func(sc ginkgo.SpecContext) {
		w.harness.Gated()
		before := w.runtime(updates)
		w.ingest(before, pr("d1", "owner/repo"))
		crash, cancel := context.WithCancel(w.ctx)
		stopped := make(chan error, 1)
		go func() {
			_, err := before.Drain(crash)
			stopped <- err
		}()
		gomega.Eventually(w.harness.Calls).WithTimeout(within).Should(gomega.Equal(1))
		cancel()
		gomega.Eventually(stopped).WithTimeout(within).Should(gomega.Receive(gomega.MatchError(context.Canceled)))

		w.harness = Replying(noConcerns())
		after := w.runtime(updates)
		gomega.Expect(after.Recover(w.ctx)).To(gomega.Equal(1))
		ran(only(w.drain(after)))
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(1))
	})
}

// describeStartup is the check a binary makes before anything connects (R-40), and the manifest
// it describes itself with (R-18, R-19, R-43).
func describeStartup(w *world) {
	ginkgo.It("a missing provider fails at startup and names what is missing", func() {
		rt := w.runtimeWith(providers.New(), []workflow.Registered{dependencyUpdates, errorTriage})
		err := rt.Validate()
		gomega.Expect(codeOf(err)).To(gomega.Equal(diag.ProviderMissing))
		gomega.Expect(err).To(gomega.MatchError(gomega.And(
			gomega.ContainSubstring("dependency-updates needs a provider for almanactest.registry"),
			gomega.ContainSubstring("dependency-updates needs a harness for agent reviewer"),
			gomega.ContainSubstring("error-triage needs a provider for agent.Classifier"),
		)))
	})

	ginkgo.It("a fully provided almanac passes the startup check", func() {
		rt := w.runtime([]workflow.Registered{dependencyUpdates, errorTriage})
		gomega.Expect(rt.Validate()).To(gomega.Succeed())
		gomega.Expect(rt.Ping(w.ctx)).To(gomega.Succeed(), "and its store answers")
	})

	ginkgo.It("going live requires a forge and a tracker", func() {
		set := providers.Provide[registry](providers.New(), w.registry).UseHarness(w.harness)
		rt := w.runtimeWith(set, []workflow.Registered{dependencyUpdates}, runtime.InMode(effect.Live))
		gomega.Expect(rt.Validate()).To(gomega.MatchError(gomega.And(
			gomega.ContainSubstring("may Attest or Push but no Forge"),
			gomega.ContainSubstring("may File or Ask but no Tracker"),
		)))
		gomega.Expect(w.runtimeWith(set, []workflow.Registered{dependencyUpdates}).Validate()).To(gomega.Succeed(), "a dry run applies nothing, so needs neither")
	})

	ginkgo.It("the manifest shows each workflow's declaration", func() {
		manifest := w.runtime([]workflow.Registered{repairBuild, watcher(newFleet(0))}).Manifest()
		gomega.Expect(manifest).To(gomega.HaveLen(2))
		gomega.Expect(manifest[0].Agents).To(gomega.ConsistOf(gomega.And(
			gomega.HaveField("Profile", "fixer"),
			gomega.HaveField("Grant", agent.Grant{Write: true}),
		)))
		gomega.Expect(manifest[0].Scans).To(gomega.BeFalse())
		gomega.Expect(manifest[1].Scans).To(gomega.BeTrue(), "the manifest says a workflow scans")
	})
}
