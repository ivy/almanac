package buildfailures_test

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/wire"
)

// manifestDiff is an update to package.json that moves tar from 6.0.0 to 7.0.0.
const manifestDiff = "@@ -1,3 +1,3 @@\n {\n-  \"tar\": \"6.0.0\",\n+  \"tar\": \"7.0.0\",\n }"

var _ = Describe("build_failures, on a simulated forge", func() {
	It("patches a red pull request past its cooldown, and it goes pending, then success", func() {
		w := newWorld(fixedMain).released(cooledAge)
		broken := w.head()
		Expect(w.suiteFinishes("failure")).To(Equal(store.Queued))
		w.settle()
		Expect(w.fixer.Calls()).To(Equal(1))
		Expect(w.pushes()).To(Equal(1))
		patched := w.head()
		Expect(patched).NotTo(Equal(broken))
		statuses := w.statuses()
		Expect(statuses).To(HaveLen(2))
		Expect(statuses[0]).To(Equal(posted{broken, "pending"}), "held before the agent ran")
		Expect(statuses[1]).To(Equal(posted{patched, "pending"}), "held on the new commit")

		Expect(w.suiteFinishes("success")).To(Equal(store.Queued))
		w.settle()
		Expect(w.lastStatus()).To(Equal(posted{patched, "success"}))
		Expect(w.fixer.Calls()).To(Equal(1), "a green build runs no agent")
	})

	It("awaits a broken update inside its cooldown, and no agent reads it", func() {
		w := newWorld(fixedMain).released(youngAge)
		w.suiteFinishes("failure")
		outcomes := w.settle()
		Expect(outcomes).To(HaveLen(1))
		Expect(outcomes[0].Result).To(Equal(runtime.Ran))
		Expect(outcomes[0].Effects).To(ConsistOf(BeAssignableToTypeOf(effect.Await{})))
		Expect(w.fixer.Calls()).To(BeZero(), "no agent reads it")
		Expect(w.statuses()).To(BeEmpty(), "nothing is held for a run that does nothing")
	})

	It("asks a person about an update with no publish time, and no agent reads it", func() {
		w := newWorld(fixedMain)
		w.suiteFinishes("failure")
		w.settle()
		Expect(w.fixer.Calls()).To(BeZero())
		Expect(w.asked()).To(ConsistOf("This update's build failed, and nothing says when it was published"))
	})

	It("makes no run for a green pull request", func() {
		w := newWorld(fixedMain).released(cooledAge)
		Expect(w.suiteFinishes("success")).To(Equal(store.Ignored))
		Expect(w.settle()).To(BeEmpty())
	})

	It("makes no run for another branch", func() {
		w := newWorld(fixedMain).released(cooledAge)
		suite := w.sim.AddSuite(repoName, simforge.FinishedSuite(w.head(), "feature", "failure", "x"))
		w.sim.SuiteCompleted(w.ctx, repoName, suite)
		Expect(w.deliver("github.check_suite.completed")).To(Equal(store.Ignored))
	})

	It("asks a person about a patch the policy refuses, and pushes nothing", func() {
		w := newWorld(touchesCI).released(cooledAge)
		broken := w.head()
		w.suiteFinishes("failure")
		w.settle()
		Expect(w.pushes()).To(BeZero())
		Expect(w.asked()).To(ConsistOf("A repair was refused by the patch policy"))
		Expect(w.lastStatus()).To(Equal(posted{broken, "failure"}))
	})

	// Green checks on the old version are not a repair: the pull request no longer updates
	// anything.
	It("never pushes a repair that takes back the update", func() {
		w := newWorld(undoesUpdate, updating("package.json", manifestDiff)).released(cooledAge)
		broken := w.head()
		w.suiteFinishes("failure")
		w.settle()
		Expect(w.pushes()).To(BeZero())
		Expect(w.asked()).To(ConsistOf(ContainSubstring("took back the update")))
		Expect(w.lastStatus()).To(Equal(posted{broken, "failure"}))
	})

	It("pushes a repair of the same manifest that keeps the update", func() {
		reply := writing("package.json", "{\n    \"tar\": \"7.0.0\",\n    \"added\": \"1.0.0\"\n}\n")
		w := newWorld(reply, updating("package.json", manifestDiff)).released(cooledAge)
		w.suiteFinishes("failure")
		w.settle()
		Expect(w.pushes()).To(Equal(1))
	})

	It("lets a repair regenerate a lockfile", func() {
		w := newWorld(writing("pnpm-lock.yaml", "regenerated\n"), updating("pnpm-lock.yaml", "@@ -1 +1 @@\n-old\n+tar@7.0.0")).released(cooledAge)
		w.suiteFinishes("failure")
		w.settle()
		Expect(w.pushes()).To(Equal(1))
	})

	It("hands over what an agent that changed nothing found", func() {
		w := newWorld(changesNothing).released(cooledAge)
		broken := w.head()
		w.suiteFinishes("failure")
		outcomes := w.settle()
		Expect(w.pushes()).To(BeZero())
		Expect(w.asked()).To(ConsistOf(ContainSubstring("no change to push")))
		Expect(w.lastStatus()).To(Equal(posted{broken, "failure"}))

		By("showing the person what the agent said, still hostile")
		Expect(outcomes).To(HaveLen(1))
		Expect(planned(outcomes[0].Effects)).To(ContainSubstring(`"body":{"$untrusted":"This needs a CI change."}`))
	})

	It("starts one run for several check suites on one commit", func() {
		w := newWorld(fixedMain).released(cooledAge)
		Expect(w.suiteFinishes("failure")).To(Equal(store.Queued))
		Expect(w.suiteFinishes("failure")).To(Equal(store.Coalesced))
		w.settle()
		Expect(w.fixer.Calls()).To(Equal(1))
	})

	It("takes a failed status as it takes a failed check suite", func() {
		w := newWorld(fixedMain).released(cooledAge)
		w.sim.AddSuite(repoName, simforge.FinishedSuite(w.head(), branch, "failure", "boom"))
		w.sim.StatusChanged(w.ctx, repoName, w.head(), "ci/build", "failure")
		Expect(w.deliver("github.status")).To(Equal(store.Queued))
	})

	It("does not take Almanac's own status as a build result", func() {
		w := newWorld(fixedMain).released(cooledAge)
		w.sim.StatusChanged(w.ctx, repoName, w.head(), renovate.TrustContext, "failure")
		Expect(w.deliver("github.status")).To(Equal(store.Ignored))
	})

	It("stops a repair that keeps failing at the cap, and asks once", func() {
		w := newWorld(fixedMain).released(cooledAge)
		for range 8 {
			w.suiteFinishes("failure")
			w.settle()
		}
		Expect(w.pushes()).To(Equal(renovate.AttemptCap))
		Expect(w.asked()).To(ConsistOf("Repairs did not fix this build"))
	})

	It("finds a red pull request in a backfill, and repairs it", func() {
		w := newWorld(fixedMain).released(cooledAge)
		w.sim.AddSuite(repoName, simforge.FinishedSuite(w.head(), branch, "failure", "boom"))
		report := w.backfill()
		Expect(report.Found).To(Equal(1))
		Expect(report.Queued).To(Equal(1))
		w.settle()
		Expect(w.pushes()).To(Equal(1))
	})

	It("leaves green pull requests alone in a backfill", func() {
		w := newWorld(fixedMain).released(cooledAge)
		w.sim.AddSuite(repoName, simforge.FinishedSuite(w.head(), branch, "success", ""))
		Expect(w.backfill().Found).To(BeZero())
	})

	It("gives the agent the logs as hostile input, and the checkout by reference", func() {
		w := newWorld(fixedMain).released(cooledAge)
		w.suiteFinishes("failure")
		w.settle()
		Expect(w.fixer.Inputs()).To(HaveLen(1))
		var input struct {
			Logs struct {
				Untrusted string `json:"$untrusted"`
			} `json:"logs"`
			Checkout struct {
				Repo string `json:"repo"`
				Sha  string `json:"sha"`
			} `json:"$checkout"`
		}
		Expect(json.Unmarshal(w.fixer.Inputs()[0], &input)).To(Succeed())
		Expect(input.Logs.Untrusted).To(ContainSubstring("mismatched types"))
		Expect(input.Checkout.Repo).To(Equal(repoName))
		Expect(input.Checkout.Sha).To(Equal(w.statuses()[0].sha))
		Expect(w.fixer.Prompts()[0]).NotTo(ContainSubstring("mismatched"), "logs are never prompt text")
	})

	// What the fleet report makes of a patched branch in conflict is the report's to say. The
	// workflow's part is to leave a pull request in conflict to Renovate, which rebases it.
	It("leaves a pull request in conflict to Renovate, repairing nothing", func() {
		w := newWorld(fixedMain, inConflict).released(cooledAge)
		w.suiteFinishes("failure")
		outcomes := w.settle()
		Expect(outcomes).To(HaveLen(1))
		Expect(outcomes[0].Effects).To(BeEmpty())
		Expect(w.fixer.Calls()).To(BeZero())
		Expect(w.statuses()).To(BeEmpty())
	})

	It("parses a status and a check suite to the same event", func() {
		sha := strings.Repeat("a", 40)
		suite := `{"repository": {"full_name": "` + repoName + `"}, "check_suite": {"head_sha": "` + sha + `", "head_branch": "` + branch + `",
			"conclusion": "failure", "head_commit": {"message": "Update"}}}`
		status := `{"repository": {"full_name": "` + repoName + `"}, "sha": "` + sha + `", "state": "failure", "context": "ci",
			"branches": [{"name": "` + branch + `"}], "commit": {"commit": {"message": "Update"}}}`
		fromSuite, err := wire.Parse[renovate.BuildEvent]([]byte(suite))
		Expect(err).NotTo(HaveOccurred())
		fromStatus, err := wire.Parse[renovate.BuildEvent]([]byte(status))
		Expect(err).NotTo(HaveOccurred())
		Expect(fromStatus).To(Equal(fromSuite))
	})

	It("finds a lost webhook in the next scheduled scan", func() {
		w := newWorld(fixedMain).released(cooledAge)
		w.sim.NextDeliveries(simforge.Lost)
		suite := w.sim.AddSuite(repoName, simforge.FinishedSuite(w.head(), branch, "failure", "boom"))
		w.sim.SuiteCompleted(w.ctx, repoName, suite)
		Expect(w.received.count()).To(BeZero(), "the webhook never arrived")
		scan, ran, err := w.rt.Tick(w.ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ran).To(BeTrue())
		Expect(scan.Queued).To(Equal(1))
		w.settle()
		Expect(w.pushes()).To(Equal(1), "the repair started")
	})

	It("reconsiders a cooldown await in a scan once the cooldown passes", func() {
		w := newWorld(fixedMain).released(youngAge)
		w.sim.AddSuite(repoName, simforge.FinishedSuite(w.head(), branch, "failure", "boom"))
		Expect(w.backfill().Queued).To(Equal(1))
		w.settle()
		Expect(w.fixer.Calls()).To(BeZero(), "still inside its cooldown")

		again := w.backfill()
		Expect(again.Queued).To(BeZero(), "nothing changed, so nothing runs")
		Expect(again.Unchanged).To(Equal(1))

		w.Clock.Advance(cooledAge - youngAge)
		Expect(w.backfill().Queued).To(Equal(1), "it cooled")
		w.settle()
		Expect(w.fixer.Calls()).To(Equal(1), "now it is repaired")
	})

	It("clears a status a dead run left pending in the next scan", func() {
		w := newWorld(fixedMain).released(cooledAge)
		sha := strings.Repeat("c", 40)
		w.sim.MoveBranch(repoName, w.number, sha, "fix\n\nAlmanac-Key: k")
		w.sim.AddSuite(repoName, simforge.FinishedSuite(sha, branch, "success", ""))
		w.status(sha, "pending")
		Expect(w.backfill().Queued).To(Equal(1))
		w.settle()
		Expect(w.lastStatus()).To(Equal(posted{sha, "success"}))
	})

	// A security fix too young to validate is held on purpose, and green checks do not change
	// that.
	It("leaves alone a hold on a commit with no repair", func() {
		w := newWorld(fixedMain).released(cooledAge)
		sha := w.head()
		w.sim.AddSuite(repoName, simforge.FinishedSuite(sha, branch, "success", ""))
		w.status(sha, "pending")
		Expect(w.backfill().Queued).To(BeZero())
		w.settle()
		Expect(w.lastStatus()).To(Equal(posted{sha, "pending"}))
	})

	It("never repairs a person's branch named like Renovate's", func() {
		w := newWorld(fixedMain, openedBy("someone")).released(cooledAge)
		sha := w.head()
		w.sim.AddSuite(repoName, simforge.FinishedSuite(sha, branch, "failure", "boom"))
		payload := renovate.SuitePayload(hostile.MustParseSlug(repoName), hostile.MustParseSlug(branch), hostile.MustParseSha(sha), true, false)
		_, err := w.rt.Ingest(w.ctx, runtime.Envelope{
			Source: "github", DeliveryID: "d", Kind: "github.check_suite.completed", Origin: store.Webhook, Payload: payload,
		})
		Expect(err).NotTo(HaveOccurred())
		w.settle()
		Expect(w.fixer.Calls()).To(BeZero())
		Expect(w.pushes()).To(BeZero())
	})

	It("keeps what a patch's session used", func() {
		var p patch.Patch
		Expect(json.Unmarshal([]byte(`{"changes": [], "diff": "", "usage": {"cost_usd": 0.25}}`), &p)).To(Succeed())
		Expect(p.Usage).To(MatchJSON(`{"cost_usd": 0.25}`))
	})
})
