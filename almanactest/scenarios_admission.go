package almanactest

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/ivy/almanac/config"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/workflow"
)

// describeAdmission is dedupe, one run per key, and chains (R-24, R-25, R-26).
func describeAdmission(w *world) {
	updates := []workflow.Registered{dependencyUpdates}

	ginkgo.It("a redelivered webhook is dropped", func() {
		rt := w.runtime(updates)
		gomega.Expect(w.ingest(rt, pr("d1", "owner/repo"))).To(gomega.Equal(store.Queued))
		gomega.Expect(w.ingest(rt, pr("d1", "owner/repo"))).To(gomega.Equal(store.Duplicate))
		w.drain(rt)
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(1))
	})

	ginkgo.It("a crash between persisting and acknowledging starts no second run", func() {
		before := w.runtime(updates)
		gomega.Expect(w.ingest(before, pr("d1", "owner/repo"))).To(gomega.Equal(store.Queued))

		after := w.runtime(updates)
		gomega.Expect(w.ingest(after, pr("d1", "owner/repo"))).To(gomega.Equal(store.Duplicate))
		gomega.Expect(w.drain(after)).To(gomega.HaveLen(1))
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(1))
	})

	ginkgo.It("a delivery of a kind no workflow handles is unrouted, and one that does not parse is rejected", func() {
		rt := w.runtime(updates)
		gomega.Expect(w.ingest(rt, report("s1"))).To(gomega.Equal(store.Unrouted))
		bad := pr("d1", "owner/repo")
		bad.Payload = []byte(`{"number": 1, "repository": {"full_name": "owner/repo"}, "pull_request": {"head": {"sha": "not a sha"}}}`)
		gomega.Expect(w.ingest(rt, bad)).To(gomega.Equal(store.Rejected))
		gomega.Expect(w.drain(rt)).To(gomega.BeEmpty())
	})

	ginkgo.It("an event a workflow does not want starts nothing", func() {
		unwanted := workflow.Code[pullRequest](workflow.Coded[pullRequest, struct{}]{
			Name: "wants-nothing", Intent: "Want nothing.", RunsPerHour: 1,
			Wanted: func(pullRequest) bool { return false },
			Key:    func(pr pullRequest) string { return pr.Repo.String() },
			Gather: func(context.Context, workflow.Cx, pullRequest) (struct{}, error) { return struct{}{}, nil },
			Decide: func(struct{}) []effect.Effect { return nil },
		})
		rt := w.runtime([]workflow.Registered{unwanted})
		gomega.Expect(w.ingest(rt, pr("d1", "owner/repo"))).To(gomega.Equal(store.Ignored))
		gomega.Expect(w.drain(rt)).To(gomega.BeEmpty())
	})

	ginkgo.It("a hundred events about one thing start one agent", func() {
		rt := w.runtime(updates)
		for i := range 100 {
			w.ingest(rt, pr(fmt.Sprintf("d%d", i), "owner/repo"))
		}
		gomega.Expect(w.drain(rt)).To(gomega.HaveLen(1))
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(1))
	})

	ginkgo.It("events during a run cause exactly one rerun", func() {
		w.harness.Gated()
		rt := w.runtime(updates)
		w.ingest(rt, pr("first", "owner/repo"))
		drained := drainInBackground(w, rt)

		gomega.Eventually(w.harness.Calls).WithTimeout(within).Should(gomega.Equal(1))
		for i := range 50 {
			gomega.Expect(w.ingest(rt, pr(fmt.Sprintf("m%d", i), "owner/repo"))).To(gomega.Equal(store.MarkedDirty))
		}
		w.harness.Release()
		gomega.Eventually(w.harness.Calls).WithTimeout(within).Should(gomega.Equal(2))
		w.harness.Release()

		gomega.Expect(<-drained).To(gomega.HaveLen(2))
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(2))
	})

	ginkgo.It("independent keys run concurrently but one key never does", func() {
		w.harness.Gated()
		rt := w.runtime(updates)
		for i := range 3 {
			w.ingest(rt, pr(fmt.Sprintf("d%d", i), fmt.Sprintf("owner/repo-%d", i)))
		}
		w.ingest(rt, pr("again", "owner/repo-0"))
		drained := drainInBackground(w, rt)

		gomega.Eventually(w.harness.Calls).WithTimeout(within).Should(gomega.Equal(3), "three keys, three sessions at once")
		gomega.Consistently(w.harness.Calls).Should(gomega.Equal(3), "the fourth event waits for its key")
		for range 3 {
			w.harness.Release()
		}
		gomega.Expect(<-drained).To(gomega.HaveLen(3))
	})

	ginkgo.It("a direct echo does not start a run", func() {
		rt := w.runtime([]workflow.Registered{notesStatus})
		gomega.Expect(w.ingest(rt, status("d1", me))).To(gomega.Equal(store.Echo))
		gomega.Expect(w.drain(rt)).To(gomega.BeEmpty())
		gomega.Expect(w.ingest(rt, status("d2", "github-actions[bot]"))).To(gomega.Equal(store.Queued))
	})

	ginkgo.It("a chain reaction stops at the depth limit", func() {
		rt := w.runtime(updates)
		depth := 0
		for w.ingest(rt, at(depth, pr(fmt.Sprintf("echo%d", depth), "owner/repo"))) != store.ChainStopped {
			w.drain(rt)
			depth++
		}
		gomega.Expect(depth).To(gomega.Equal(4))
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(4))
	})

	ginkgo.Describe("a repair loop", func() {
		// ciGoesRed delivers red CI on the repository, from another actor than Almanac.
		var ciGoesRed func(rt *runtime.Runtime) store.Ingest
		ginkgo.BeforeEach(func() {
			w.harness.Reply(aRepair())
			next := 0
			ciGoesRed = func(rt *runtime.Runtime) store.Ingest {
				ginkgo.GinkgoHelper()
				next++
				return w.ingest(rt, suite(fmt.Sprintf("d%d", next), next))
			}
		})
		live := func() *runtime.Runtime {
			return w.runtime([]workflow.Registered{repairPerRepo}, runtime.InMode(effect.Live))
		}
		turn := func(rt *runtime.Runtime) store.Ingest {
			ginkgo.GinkgoHelper()
			ingest := ciGoesRed(rt)
			w.drain(rt)
			w.deliver(rt)
			return ingest
		}

		ginkgo.It("a repair that keeps failing makes exactly three attempts and asks once", func() {
			rt := live()
			for range 8 {
				turn(rt)
			}
			gomega.Expect(w.forge.Pushes()).To(gomega.HaveLen(attemptCap), "exactly the cap")
			gomega.Expect(w.tracker.Filed()).To(gomega.HaveLen(1), "a person is asked once")
		})

		ginkgo.It("CI after a push inherits depth though another actor sent it", func() {
			rt := live()
			stopped := 0
			for range 12 {
				if turn(rt) == store.ChainStopped {
					stopped++
				}
			}
			gomega.Expect(stopped).To(gomega.BeNumerically(">", 0), "the chain must stop on depth alone, whoever sent the events")
		})

		ginkgo.It("an unrelated event after the ledger window starts fresh", func() {
			rt := live()
			for range 6 {
				turn(rt)
			}
			gomega.Expect(ciGoesRed(rt)).To(gomega.Equal(store.ChainStopped))
			w.clock.Advance(runtime.LedgerWindow + 1)
			gomega.Expect(ciGoesRed(rt)).To(gomega.Equal(store.Queued))
		})

		ginkgo.It("a dry run leaves no trace and so causes no depth", func() {
			rt := w.runtime([]workflow.Registered{repairPerRepo})
			for range 8 {
				gomega.Expect(ciGoesRed(rt)).To(gomega.Equal(store.Queued))
				ran(only(w.drain(rt)))
			}
			gomega.Expect(w.forge.Calls()).To(gomega.BeEmpty())
		})
	})
}

// describeLimits is budgets, lanes and concurrency (R-27, R-28, R-44).
func describeLimits(w *world) {
	updates := []workflow.Registered{dependencyUpdates}

	ginkgo.It("one root cause across many keys trips the budget and holds the rest", func() {
		rt := w.runtime(updates)
		for i := range 25 {
			w.ingest(rt, pr(fmt.Sprintf("d%d", i), fmt.Sprintf("owner/repo-%d", i)))
		}
		outcomes := w.drain(rt)
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(dependencyUpdates.RunsPerHour()))
		gomega.Expect(tripped(outcomes)).To(gomega.ConsistOf(runtime.Outcome{Result: runtime.BudgetTripped, Workflow: "dependency-updates", Held: 15}))
	})

	ginkgo.It("a tripped budget asks once and held work runs after an hour", func() {
		set := w.providers().AlertTo(hostile.MustParseSlug("owner/ops"))
		capped := config.DefaultLimits()
		capped.RunsPerHour = config.CapAt(5)
		rt := w.runtimeWith(set, updates, runtime.InMode(effect.Live), runtime.Tightened(capped))
		for i := range 20 {
			w.ingest(rt, pr(fmt.Sprintf("d%d", i), fmt.Sprintf("owner/repo-%d", i)))
		}

		first := w.drain(rt)
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(5))
		gomega.Expect(tripped(first)).To(gomega.HaveLen(1))
		again := w.drain(rt)
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(5), "held")
		gomega.Expect(tripped(again)).To(gomega.BeEmpty(), "and no second alert")

		w.deliver(rt)
		gomega.Expect(w.tracker.Filed()).To(gomega.ConsistOf(gomega.HaveField("Target", "owner/ops")))

		w.clock.Advance(runtime.BudgetWindow + 1)
		refilled := w.drain(rt)
		gomega.Expect(w.harness.Calls()).To(gomega.Equal(10), "the window opened")
		gomega.Expect(tripped(refilled)).To(gomega.HaveLen(1), "a new window may alert again")
	})

	ginkgo.It("an overall budget holds every workflow", func() {
		capped := config.DefaultLimits()
		capped.OverallRunsPerHour = config.CapAt(1)
		rt := w.runtime([]workflow.Registered{dependencyUpdates, errorTriage}, runtime.Tightened(capped))
		w.ingest(rt, pr("d1", "owner/repo"))
		w.ingest(rt, report("s1"))

		outcomes := w.drain(rt)
		gomega.Expect(outcomes).To(gomega.HaveLen(2))
		gomega.Expect(tripped(outcomes)).To(gomega.ConsistOf(runtime.Outcome{Result: runtime.BudgetTripped, Held: 1}))
	})

	ginkgo.It("limits only ever tighten", func() {
		narrow := config.Limits{MaxChainDepth: 2, RunsPerHour: config.CapAt(5), Concurrency: 2}
		wide := config.Limits{MaxChainDepth: 99, RunsPerHour: config.CapAt(1000), Concurrency: 64}
		l := w.runtime(updates, runtime.Tightened(narrow), runtime.Tightened(wide)).Limits()
		gomega.Expect(l.MaxChainDepth).To(gomega.Equal(2))
		gomega.Expect(l.RunsPerHour).To(gomega.Equal(config.CapAt(5)))
		gomega.Expect(l.Concurrency).To(gomega.Equal(2))
	})

	ginkgo.It("config can lengthen the scan interval and never shorten it", func() {
		longer := config.DefaultLimits()
		longer.ScanInterval *= 2
		shorter := config.DefaultLimits()
		shorter.ScanInterval = 1
		rt := w.runtime(updates, runtime.Tightened(longer), runtime.Tightened(shorter))
		gomega.Expect(rt.Limits().ScanInterval).To(gomega.Equal(longer.ScanInterval))
	})

	ginkgo.It("a backfill does not starve a live event", func() {
		one := config.DefaultLimits()
		one.Concurrency = 1
		rt := w.runtime(updates, runtime.Tightened(one))
		for i := range 9 {
			w.ingest(rt, scanned(pr(fmt.Sprintf("s%d", i), fmt.Sprintf("owner/scan-%d", i))))
		}
		w.ingest(rt, pr("live", "owner/live"))
		w.drain(rt)
		gomega.Expect(w.harness.Prompts()[0]).To(gomega.ContainSubstring("owner/live"))
	})

	ginkgo.It("a stop during a run starts nothing new, and the rest stays queued for the next start", func() {
		w.harness.Gated()
		one := config.DefaultLimits()
		one.Concurrency = 1
		rt := w.runtime(updates, runtime.Tightened(one))
		w.ingest(rt, pr("d1", "owner/one"))
		w.ingest(rt, pr("d2", "owner/two"))
		drained := drainInBackground(w, rt)

		gomega.Eventually(w.harness.Calls).WithTimeout(within).Should(gomega.Equal(1))
		rt.StopClaiming()
		w.harness.Release()
		ran(only(<-drained))

		w.harness = Replying(noConcerns())
		restarted := w.runtime(updates)
		ran(only(w.drain(restarted)))
	})
}

// drainInBackground drains rt in a goroutine, for a scenario that acts while runs are under
// way, and sends what it drained.
func drainInBackground(w *world, rt *runtime.Runtime) <-chan []runtime.Outcome {
	drained := make(chan []runtime.Outcome, 1)
	go func() {
		defer ginkgo.GinkgoRecover()
		drained <- w.drain(rt)
	}()
	return drained
}
