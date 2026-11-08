package almanactest

import (
	"encoding/json"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/workflow"
)

// describeScans is scans, backfill and catch-up, and one path for every origin (R-30 to R-33,
// R-35, R-36).
func describeScans(w *world) {
	const repo = "owner/repo"
	scanning := func(f *fleet) *runtime.Runtime {
		return w.runtime([]workflow.Registered{watcher(f)})
	}
	backfill := func(rt *runtime.Runtime, only *hostile.Slug) runtime.Backfill {
		ginkgo.GinkgoHelper()
		report, err := rt.Backfill(w.ctx, only)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		return report
	}
	tick := func(rt *runtime.Runtime) (runtime.Backfill, bool) {
		ginkgo.GinkgoHelper()
		report, ran, err := rt.Tick(w.ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		return report, ran
	}

	ginkgo.It("backfilling existing pull requests records a decision for each and writes nothing", func() {
		rt := scanning(newFleet(28))
		report := backfill(rt, nil)
		gomega.Expect(report).To(gomega.Equal(runtime.Backfill{Found: 28, Queued: 28}))
		outcomes := w.drain(rt)
		gomega.Expect(outcomes).To(gomega.HaveLen(28))
		for _, outcome := range outcomes {
			gomega.Expect(statuses(w.effectsOf(rt, ran(outcome).RunID))).To(gomega.Equal([]store.Status{store.WouldApply}))
		}
		gomega.Expect(w.deliver(rt)).To(gomega.BeEmpty())
		gomega.Expect(w.forge.Calls()).To(gomega.BeEmpty())
	})

	ginkgo.It("rerunning a backfill starts no new runs", func() {
		rt := scanning(newFleet(3))
		backfill(rt, nil)
		w.drain(rt)
		gomega.Expect(backfill(rt, nil)).To(gomega.Equal(runtime.Backfill{Found: 3, Unchanged: 3}))
		gomega.Expect(w.drain(rt)).To(gomega.BeEmpty())
	})

	ginkgo.It("a new commit is scanned again", func() {
		f := newFleet(1)
		rt := scanning(f)
		backfill(rt, nil)
		w.drain(rt)
		f.moveBranch(repo, 1, "cccccccccccccccccccccccccccccccccccccccc")
		gomega.Expect(backfill(rt, nil).Queued).To(gomega.Equal(1))
	})

	ginkgo.It("a backfill can look at one repository", func() {
		f := newFleet(2)
		f.add("owner/other", listed{Branch: "renovate/x", Head: head})
		other := hostile.MustParseSlug("owner/other")
		gomega.Expect(backfill(scanning(f), &other).Found).To(gomega.Equal(1))
	})

	ginkgo.It("a backfill refuses a repository it cannot see", func() {
		rt := scanning(newFleet(2))
		unseen := hostile.MustParseSlug("owner/unseen")
		_, err := rt.Backfill(w.ctx, &unseen)
		gomega.Expect(codeOf(err)).To(gomega.Equal(diag.ScanFailed))
		gomega.Expect(err).To(gomega.MatchError(gomega.And(gomega.ContainSubstring("owner/unseen"), gomega.ContainSubstring("the 1 repositories"))))
		gomega.Expect(w.drain(rt)).To(gomega.BeEmpty())
	})

	ginkgo.It("a webhook arriving after a scan coalesces on the same key", func() {
		f := newFleet(1)
		rt := scanning(f)
		backfill(rt, nil)
		webhook := runtime.Envelope{Source: "github", DeliveryID: "w1", Kind: "github.pull_request.opened", Payload: f.webhook(repo, 1)}
		gomega.Expect(w.ingest(rt, webhook)).To(gomega.Equal(store.Coalesced))
		gomega.Expect(w.drain(rt)).To(gomega.HaveLen(1))
	})

	ginkgo.It("all three origins produce identical decisions for the same state", func() {
		f := newFleet(1)
		found, err := f.scan(w.ctx, workflow.NewScanner(providers.New()))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		decision := func(origin store.Origin, payload json.RawMessage) []string {
			ginkgo.GinkgoHelper()
			rt := w.runtime([]workflow.Registered{watcher(f)}, runtime.WithStore(w.open("origin-"+origin.String())))
			w.ingest(rt, runtime.Envelope{Source: "t", DeliveryID: "d", Kind: "github.pull_request.opened", Origin: origin, Payload: payload})
			var decided []string
			for _, entry := range w.effectsOf(rt, ran(only(w.drain(rt))).RunID) {
				decided = append(decided, string(entry.Effect))
			}
			return decided
		}

		fromWebhook := decision(store.Webhook, f.webhook(repo, 1))
		gomega.Expect(decision(store.Scan, found[0].Payload)).To(gomega.Equal(fromWebhook))
		gomega.Expect(decision(store.Simulated, f.webhook(repo, 1))).To(gomega.Equal(fromWebhook))
		gomega.Expect(fromWebhook).To(gomega.ConsistOf(gomega.MatchJSON(`{"Await": {"until": "labeled"}}`)))
	})

	ginkgo.It("two scans cannot overlap", func() {
		f := newFleet(1)
		f.gated()
		rt := scanning(f)
		first := make(chan runtime.Backfill, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			first <- backfill(rt, nil)
		}()
		gomega.Eventually(f.listings).WithTimeout(within).Should(gomega.Equal(1))

		gomega.Expect(backfill(rt, nil)).To(gomega.Equal(runtime.Backfill{Skipped: true}))
		f.release()
		gomega.Eventually(first).WithTimeout(within).Should(gomega.Receive(gomega.Equal(runtime.Backfill{Found: 1, Queued: 1})))
	})

	ginkgo.It("a scheduled scan runs when due and only then", func() {
		rt := scanning(newFleet(1))
		_, ran := tick(rt)
		gomega.Expect(ran).To(gomega.BeTrue(), "due at start")
		_, ran = tick(rt)
		gomega.Expect(ran).To(gomega.BeFalse())
		w.clock.Advance(rt.Limits().ScanInterval)
		_, ran = tick(rt)
		gomega.Expect(ran).To(gomega.BeTrue())
	})

	ginkgo.It("a lost webhook is found by the next scheduled scan", func() {
		f := newFleet(1)
		rt := scanning(f)
		tick(rt)
		w.drain(rt)

		f.add(repo, listed{Branch: "renovate/lost", Head: head, Labels: []string{"dependencies"}})
		w.clock.Advance(rt.Limits().ScanInterval)
		report, _ := tick(rt)
		gomega.Expect(report).To(gomega.Equal(runtime.Backfill{Found: 2, Queued: 1, Unchanged: 1}))
		ran(only(w.drain(rt)))
	})

	ginkgo.It("a scheduled scan sweeps first, and a sweep that fails does not stop it", func() {
		sweep := Failing()
		set := providers.Provide[effect.Sweep](w.providers(), sweep)
		rt := w.runtimeWith(set, []workflow.Registered{watcher(newFleet(1))})
		report, ran := tick(rt)
		gomega.Expect(ran).To(gomega.BeTrue())
		gomega.Expect(sweep.Calls()).To(gomega.Equal(1))
		gomega.Expect(report.Found).To(gomega.Equal(1))
	})
}
