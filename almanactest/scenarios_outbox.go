package almanactest

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/workflow"
)

// describeOutbox is recording effects and applying each once (R-34, principle 6).
func describeOutbox(w *world) {
	// checked is a runtime whose one run fails trust checks, so it decides Attest and Ask.
	checked := func(options ...runtime.Option) *runtime.Runtime {
		w.registry.checksFail = true
		return w.runtime([]workflow.Registered{dependencyUpdates}, options...)
	}
	calls := func() int { return len(w.forge.Calls()) + len(w.tracker.Calls()) }

	ginkgo.It("a dry run records what would apply and calls no provider", func() {
		rt := checked()
		w.ingest(rt, pr("d1", "owner/repo"))
		outcome := ran(only(w.drain(rt)))
		gomega.Expect(w.deliver(rt)).To(gomega.BeEmpty())
		gomega.Expect(calls()).To(gomega.BeZero())
		gomega.Expect(statuses(w.effectsOf(rt, outcome.RunID))).To(gomega.Equal([]store.Status{store.WouldApply, store.WouldApply}))
	})

	ginkgo.It("a dry run applies nothing, even from a store holding pending effects", func() {
		live := checked(runtime.InMode(effect.Live))
		w.ingest(live, pr("d1", "owner/repo"))
		w.drain(live)

		gomega.Expect(w.deliver(checked())).To(gomega.BeEmpty())
		gomega.Expect(calls()).To(gomega.BeZero())
		gomega.Expect(w.deliver(live)).To(gomega.HaveLen(2), "they were left for a live runtime")
	})

	ginkgo.It("a crash before applying delivers each effect once after restart", func() {
		before := checked(runtime.InMode(effect.Live))
		w.ingest(before, pr("d1", "owner/repo"))
		w.drain(before)
		gomega.Expect(calls()).To(gomega.BeZero(), "recording applies nothing")

		after := checked(runtime.InMode(effect.Live))
		gomega.Expect(w.deliver(after)).To(gomega.HaveLen(2))
		gomega.Expect(w.deliver(after)).To(gomega.BeEmpty())
		gomega.Expect(calls()).To(gomega.Equal(2))
		gomega.Expect(w.forge.Statuses()).To(gomega.HaveLen(1))
		gomega.Expect(w.tracker.Filed()).To(gomega.HaveLen(1))
	})

	ginkgo.It("a failed delivery is retried with the same key", func() {
		w.forge.FailNext(1)
		rt := checked(runtime.InMode(effect.Live))
		w.ingest(rt, pr("d1", "owner/repo"))
		w.drain(rt)

		first := w.deliver(rt)
		gomega.Expect(first[0].Err).To(gomega.HaveOccurred())
		second := w.deliver(rt)
		gomega.Expect(second).To(gomega.HaveLen(1))
		gomega.Expect(second[0].Err).NotTo(gomega.HaveOccurred())
		keys := w.forge.Calls()
		gomega.Expect(keys).To(gomega.HaveLen(2))
		gomega.Expect(keys[0]).To(gomega.Equal(keys[1]))
	})

	ginkgo.It("repeated failures stop being retried", func() {
		w.forge.FailNext(runtime.MaxDeliveryAttempts + 1)
		rt := checked(runtime.InMode(effect.Live))
		w.ingest(rt, pr("d1", "owner/repo"))
		outcome := ran(only(w.drain(rt)))

		for range runtime.MaxDeliveryAttempts {
			w.deliver(rt)
		}
		gomega.Expect(w.forge.Calls()).To(gomega.HaveLen(runtime.MaxDeliveryAttempts))
		gomega.Expect(w.deliver(rt)).To(gomega.BeEmpty())
		gomega.Expect(statuses(w.effectsOf(rt, outcome.RunID))).To(gomega.Equal([]store.Status{store.Failed, store.Applied}))
	})

	ginkgo.It("hostile text stays tagged in the stored effect", func() {
		rt := checked()
		w.ingest(rt, pr("d1", "owner/repo"))
		outcome := ran(only(w.drain(rt)))
		recorded := w.effectsOf(rt, outcome.RunID)
		gomega.Expect(field(recorded[1].Effect, "Ask", "body")).To(gomega.Equal(tagged(injection)))
	})

	ginkgo.It("settles: a rerun held back for its key's effects starts once they land", func() {
		w.harness.Gated()
		rt := checked(runtime.InMode(effect.Live))
		w.ingest(rt, pr("d1", "owner/repo"))
		settled := make(chan []runtime.Outcome, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			outcomes, err := rt.Settle(w.ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			settled <- outcomes
		}()

		gomega.Eventually(w.harness.Calls).WithTimeout(within).Should(gomega.Equal(1))
		gomega.Expect(w.ingest(rt, pr("d2", "owner/repo"))).To(gomega.Equal(store.MarkedDirty))
		w.harness.Release()
		gomega.Eventually(w.harness.Calls).WithTimeout(within).Should(gomega.Equal(2))
		gomega.Expect(calls()).To(gomega.Equal(2), "the first run's effects landed before the rerun began")
		w.harness.Release()

		gomega.Eventually(settled).WithTimeout(within).Should(gomega.Receive(gomega.HaveLen(2)))
		gomega.Expect(calls()).To(gomega.Equal(4))
	})
}

// describeModes is live, dry-run and read-only (R-34, ADR 2).
func describeModes(w *world) {
	updates := []workflow.Registered{dependencyUpdates}

	ginkgo.DescribeTable("--dry-run wins over ALMANAC_MODE=live",
		func(dryRun bool, env map[string]string, want runtime.Switches) {
			lookup := func(name string) (string, bool) {
				value, ok := env[name]
				return value, ok
			}
			switches := runtime.SwitchesFrom(dryRun, lookup)
			gomega.Expect(switches).To(gomega.Equal(want))
			gomega.Expect(w.runtime(updates, runtime.Switched(switches)).Mode()).To(gomega.Equal(want.Mode))
		},
		ginkgo.Entry("nothing set is a dry run", false, map[string]string{}, runtime.Switches{Mode: effect.DryRun}),
		ginkgo.Entry("ALMANAC_MODE=live is live", false, map[string]string{"ALMANAC_MODE": "live"}, runtime.Switches{Mode: effect.Live}),
		ginkgo.Entry("--dry-run wins over ALMANAC_MODE=live", true, map[string]string{"ALMANAC_MODE": "live"}, runtime.Switches{Mode: effect.DryRun}),
		ginkgo.Entry("any other mode is a dry run", false, map[string]string{"ALMANAC_MODE": "Live"}, runtime.Switches{Mode: effect.DryRun}),
		ginkgo.Entry("ALMANAC_READ_ONLY=1 wins over ALMANAC_MODE=live", false, map[string]string{"ALMANAC_MODE": "live", "ALMANAC_READ_ONLY": "1"}, runtime.Switches{Mode: effect.DryRun, ReadOnly: true}),
		ginkgo.Entry("any ALMANAC_READ_ONLY but 0 is read-only", false, map[string]string{"ALMANAC_MODE": "live", "ALMANAC_READ_ONLY": "true"}, runtime.Switches{Mode: effect.DryRun, ReadOnly: true}),
		ginkgo.Entry("ALMANAC_READ_ONLY=0 is not read-only", false, map[string]string{"ALMANAC_MODE": "live", "ALMANAC_READ_ONLY": "0"}, runtime.Switches{Mode: effect.Live}),
	)

	ginkgo.It("a read-only runtime is a dry run whatever mode it is given", func() {
		rt := w.runtime(updates, runtime.ReadOnly(), runtime.InMode(effect.Live))
		gomega.Expect(rt.Mode()).To(gomega.Equal(effect.DryRun))
		gomega.Expect(rt.IsReadOnly()).To(gomega.BeTrue())
	})

	ginkgo.It("a dry run leaves a live run everything still to do", func() {
		w.registry.checksFail = true
		dry := w.runtime(updates, runtime.WithStore(w.open(effect.DryRun.Scope(scope))))
		live := w.runtime(updates, runtime.WithStore(w.open(effect.Live.Scope(scope))), runtime.InMode(effect.Live))
		for i := range 3 {
			gomega.Expect(w.ingest(dry, pr(fmt.Sprintf("d%d", i), fmt.Sprintf("owner/repo-%d", i)))).To(gomega.Equal(store.Queued))
		}
		dryRuns, err := dry.Settle(w.ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(dryRuns).To(gomega.HaveLen(3))
		gomega.Expect(w.forge.Calls()).To(gomega.BeEmpty())
		gomega.Expect(w.ingest(dry, pr("d0", "owner/repo-0"))).To(gomega.Equal(store.Duplicate), "the dry run has seen them")

		for i := range 3 {
			gomega.Expect(w.ingest(live, pr(fmt.Sprintf("d%d", i), fmt.Sprintf("owner/repo-%d", i)))).To(gomega.Equal(store.Queued), "live has seen nothing")
		}
		liveRuns, err := live.Settle(w.ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(liveRuns).To(gomega.HaveLen(3))
		gomega.Expect(w.forge.Statuses()).To(gomega.HaveLen(3))
		gomega.Expect(w.tracker.Filed()).To(gomega.HaveLen(3))
	})

	ginkgo.It("a read-only pass writes nothing and calls no model", func() {
		sweep := &FakeSweep{}
		rt := w.runtimeWith(withSweep(w.providers(), sweep),
			[]workflow.Registered{holdsThenDecides, readsWithAnAgent, errorTriage, writesDirectly},
			runtime.InMode(effect.Live), runtime.ReadOnly())
		gomega.Expect(rt.Validate()).To(gomega.Succeed())

		w.ingest(rt, pr("d1", "owner/repo"))
		w.ingest(rt, suite("d2", 1))
		w.ingest(rt, report("d3"))
		w.ingest(rt, status("d4", "github-actions[bot]"))
		_, ticked, err := rt.Tick(w.ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ticked).To(gomega.BeTrue())
		outcomes, err := rt.Settle(w.ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Expect(w.forge.Calls()).To(gomega.BeEmpty(), "nothing reached the forge")
		gomega.Expect(w.tracker.Calls()).To(gomega.BeEmpty(), "nothing reached the tracker")
		gomega.Expect(sweep.Calls()).To(gomega.BeZero(), "no redelivery was asked for")
		gomega.Expect(w.harness.Calls()).To(gomega.BeZero(), "no agent ran")
		gomega.Expect(w.classifier.Calls()).To(gomega.BeZero(), "no classifier was asked")

		byWorkflow := map[string]runtime.Outcome{}
		for _, o := range outcomes {
			byWorkflow[o.Workflow] = o
		}
		held := ran(byWorkflow["holds-then-decides"])
		gomega.Expect(statuses(w.effectsOf(rt, held.RunID))).To(gomega.Equal([]store.Status{store.WouldApply, store.WouldApply}))
		gomega.Expect(byWorkflow["reads-with-an-agent"]).To(gomega.HaveField("Reason", gomega.ContainSubstring("ALMANAC_READ_ONLY")))
		gomega.Expect(byWorkflow["error-triage"]).To(gomega.HaveField("Reason", gomega.ContainSubstring("ALMANAC_READ_ONLY")))
		gomega.Expect(byWorkflow["writes-directly"]).To(gomega.HaveField("Reason", gomega.ContainSubstring("read-only")))
	})

	ginkgo.It("a read-only runtime needs no harness, and a refused write is a diagnostic", func() {
		set := providers.Provide[registry](providers.New(), w.registry)
		providers.Provide[effect.Forge](set, w.forge)
		rt := w.runtimeWith(set, []workflow.Registered{dependencyUpdates, writesDirectly}, runtime.ReadOnly())
		gomega.Expect(rt.Validate()).To(gomega.Succeed(), "every agent has a harness that refuses")

		forge, ok := providers.Lookup[effect.Forge](set)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(forge).To(gomega.BeIdenticalTo(w.forge), "the binary's own set is left as it was")
		w.ingest(rt, status("d1", "github-actions[bot]"))
		outcome := only(w.drain(rt))
		gomega.Expect(outcome.Result).To(gomega.Equal(runtime.Failed))
		gomega.Expect(outcome.Reason).To(gomega.ContainSubstring("posting a status is a write"))
		gomega.Expect(w.forge.Calls()).To(gomega.BeEmpty())
	})
}

func withSweep(set *providers.Set, sweep effect.Sweep) *providers.Set {
	return providers.Provide(set, sweep)
}
