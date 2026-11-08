package compare_test

import (
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate/compare"
)

// The SHAs of the Rust version's repair commits.
var (
	repaired = strings.Repeat("e", 40)
	later    = strings.Repeat("f", 40)
)

var _ = Describe("Comparing the Go version's effects with the Rust version's", func() {
	var w *world
	BeforeEach(func() { w = newWorld() })

	Describe("a read-only Go run that reaches an agent", func() {
		It("stops where the fixer would start a repair, and a Rust repair commit on that commit answers it", func() {
			number, head := w.failingBuild(100 * time.Hour)
			Expect(failedAt(w.goRuns())).To(ConsistOf(ContainSubstring("a model would be called here")))

			w.rustStatus(head, "pending")
			w.rustRepair(number, repaired)
			w.rustStatus(repaired, "success")

			p := only(w.compared())
			Expect(p.Verdict).To(Equal(compare.Agreed))
			Expect(p.Runs).To(HaveLen(1))
			Expect(p.Runs[0].Outcome).To(Equal("stopped where an agent would run"))
			Expect(p.Matched).To(HaveLen(1))
			Expect(p.Matched[0].Go.Says).To(Equal("would start a repair on " + short(head)))
			Expect(says(p.Matched[0].Rust)).To(ConsistOf(
				"left almanac/trust pending on "+short(head),
				"pushed repair "+short(repaired)+" onto "+short(head)+", Almanac-Key run-7:0",
			))
			Expect(p.GoOnly).To(BeEmpty(), "a repair a read-only run could not push is not a missing push")
			Expect(p.RustOnly).To(BeEmpty())
		})

		It("stops where it would ask the reviewer, and the Rust verdict status on that commit answers it", func() {
			_, head := w.securityFix(true)
			Expect(failedAt(w.goRuns())).To(ConsistOf(ContainSubstring("a model would be called here")))

			w.rustStatus(head, "success")

			p := only(w.compared())
			Expect(p.Verdict).To(Equal(compare.Agreed))
			Expect(p.Matched).To(HaveLen(1))
			Expect(p.Matched[0].Go.Says).To(Equal("would ask the reviewer about " + short(head)))
			Expect(says(p.Matched[0].Rust)).To(Equal([]string{"passed almanac/trust on " + short(head)}))
		})

		It("is answered by a repair that came to nothing: a failed status, and the question after it", func() {
			number, head := w.failingBuild(100 * time.Hour)
			w.goRuns()

			w.rustStatus(head, "failure")
			w.rustAsk(number, "run-7:1")

			p := only(w.compared())
			Expect(p.Verdict).To(Equal(compare.Agreed))
			Expect(says(p.Matched[0].Rust)).To(ConsistOf(
				"failed almanac/trust on "+short(head),
				"asked a person, almanac:run-7:1",
			))
		})

		It("is a disagreement when the Rust version did nothing to that commit", func() {
			_, head := w.failingBuild(100 * time.Hour)
			w.goRuns()

			p := only(w.compared())
			Expect(p.Verdict).To(Equal(compare.Disagreed))
			Expect(says(p.GoOnly)).To(Equal([]string{"would start a repair on " + short(head)}))
		})
	})

	It("agrees where both versions did the same", func() {
		number, head := w.securityFix(false)
		w.goRuns()

		w.rustStatus(head, "failure")
		w.rustAsk(number, "run-3:1")

		p := only(w.compared())
		Expect(p.Verdict).To(Equal(compare.Agreed))
		Expect(p.Runs[0].Outcome).To(Equal("would act"))
		Expect(p.Matched).To(HaveLen(2))
		Expect(p.Matched[0].Go.Says).To(Equal("would fail almanac/trust on " + short(head)))
		Expect(says(p.Matched[0].Rust)).To(Equal([]string{"failed almanac/trust on " + short(head)}))
		Expect(p.Matched[1].Go.Says).To(Equal("would ask a person: This update is not the fix an advisory names"))
		Expect(says(p.Matched[1].Rust)).To(Equal([]string{"asked a person, almanac:run-3:1"}))
	})

	It("lists each Go effect the Rust version did not apply as a disagreement", func() {
		_, head := w.securityFix(false)
		w.goRuns()

		found := w.compared()
		p := only(found)
		Expect(p.Verdict).To(Equal(compare.Disagreed))
		Expect(says(p.GoOnly)).To(Equal([]string{
			"would fail almanac/trust on " + short(head),
			"would ask a person: This update is not the fix an advisory names",
		}))
		Expect(found.Totals[compare.Disagreed]).To(Equal(1))
		Expect(found.Disagrees()).To(BeTrue())
	})

	It("lists a Rust action no Go effect matches as a disagreement", func() {
		number, head := w.failingBuild(10 * time.Hour)
		w.goRuns()

		w.rustRepair(number, repaired)
		w.rustStatus(repaired, "failure")

		p := only(w.compared())
		Expect(p.Verdict).To(Equal(compare.Disagreed))
		Expect(says(p.Waits)).To(Equal([]string{"would wait for the update's cooldown to pass"}))
		Expect(says(p.RustOnly)).To(Equal([]string{"pushed repair " + short(repaired) + " onto " + short(head) + ", Almanac-Key run-7:0"}))
		Expect(p.GoOnly).To(BeEmpty())
	})

	It("reports a pull request the Rust version is working on now as in progress, and counts nothing on it", func() {
		_, head := w.failingBuild(100 * time.Hour)
		w.goRuns()

		w.rustStatus(head, "pending")

		found := w.compared()
		p := only(found)
		Expect(p.Verdict).To(Equal(compare.RustWorking))
		Expect(p.Trust).To(Equal("pending"))
		Expect(says(p.GoOnly)).To(Equal([]string{"would start a repair on " + short(head)}))
		Expect(found.Totals).To(Equal(map[compare.Verdict]int{
			compare.Agreed: 0, compare.Disagreed: 0, compare.RustWorking: 1, compare.NotCompared: 0,
		}))
		Expect(found.Disagrees()).To(BeFalse())
	})

	It("does not compare what the Rust version did to a commit no Go run looked at", func() {
		number, head := w.failingBuild(100 * time.Hour)
		w.goRuns()
		w.rustRepair(number, repaired)
		w.rustRepair(number, later)
		w.rustStatus(later, "success")

		p := only(w.compared())
		Expect(p.Verdict).To(Equal(compare.Agreed))
		Expect(says(p.Matched[0].Rust)).To(Equal([]string{"pushed repair " + short(repaired) + " onto " + short(head) + ", Almanac-Key run-7:0"}))
		Expect(says(p.Unseen)).To(Equal([]string{"pushed repair " + short(later) + " onto " + short(repaired) + ", Almanac-Key run-7:0"}))
	})

	It("does not compare a pull request no Go run was about", func() {
		number, head := w.failingBuild(100 * time.Hour)
		w.rustStatus(head, "failure")
		w.rustAsk(number, "run-1:1")

		p := only(w.compared())
		Expect(p.Verdict).To(Equal(compare.NotCompared))
		Expect(p.Runs).To(BeEmpty())
		Expect(says(p.Unseen)).To(Equal([]string{"asked a person, almanac:run-1:1"}))
	})

	It("compares only the Go runs in the window", func() {
		w.failingBuild(100 * time.Hour)
		w.goRuns()

		found, err := compare.CompareEffects(w.ctx, compare.EffectSources{
			GitHub: w.Client, Store: w.store, Workflows: w.workflows, Owner: owner, Author: "ivy-s-renovate-bot[bot]",
			Since: now.Add(time.Minute),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(only(found).Verdict).To(Equal(compare.NotCompared))
		Expect(*found.Since).To(Equal(now.Add(time.Minute)))
	})

	It("prints each pull request's actions for a person, and the same as JSON", func() {
		number, head := w.failingBuild(100 * time.Hour)
		w.goRuns()
		w.rustStatus(head, "pending")
		w.rustRepair(number, repaired)
		w.rustStatus(repaired, "success")
		found := w.compared()
		run := only(found).Runs[0].ID

		Expect(compare.RenderEffects(found)).To(Equal(
			"agree            owner/repo#1  renovate/tar-7.x  head eeeeeee, almanac/trust success\n" +
				"    go run " + run + " (build_failures) on " + short(head) + ": stopped where an agent would run\n" +
				"    both       go would start a repair on " + short(head) + "  ~  rust left almanac/trust pending on " + short(head) +
				"; pushed repair eeeeeee onto " + short(head) + ", Almanac-Key run-7:0\n" +
				"1 open Renovate pull requests, against every Go run: 1 agree, 0 disagree, 0 rust-in-progress, 0 not-compared\n"))

		text, err := json.Marshal(found)
		Expect(err).NotTo(HaveOccurred())
		Expect(text).To(MatchJSON(`{
			"since": null,
			"totals": {"agree": 1, "disagree": 0, "rust-in-progress": 0, "not-compared": 0},
			"pulls": [{
				"pull": "owner/repo#1", "branch": "renovate/tar-7.x", "head": "` + repaired + `", "verdict": "agree", "trust": "success",
				"go_runs": [{"run": "` + run + `", "workflow": "build_failures", "sha": "` + head + `", "outcome": "stopped where an agent would run"}],
				"matched": [{
					"go": {"kind": "repair", "sha": "` + head + `", "run": "` + run + `", "detail": "fixer", "says": "would start a repair on ` + short(head) + `"},
					"rust": [
						{"kind": "hold", "sha": "` + head + `", "says": "left almanac/trust pending on ` + short(head) + `"},
						{"kind": "push", "sha": "` + head + `", "commit": "` + repaired + `", "key": "run-7:0", "says": "pushed repair eeeeeee onto ` + short(head) + `, Almanac-Key run-7:0"}
					]
				}],
				"go_only": [], "rust_only": [], "waits": [], "unseen": []
			}]
		}`))
	})
})
