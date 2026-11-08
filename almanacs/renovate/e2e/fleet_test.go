package e2e_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/simforge"
)

// The Rust specification is almanacs/renovate/tests/e2e/main.rs. These specs drive
// build_failures across a fleet; security_test.go drives security_fixes.
var _ = Describe("The Renovate almanac, end to end", Label("e2e"), func() {
	It("a fleet ends with every pull request in a final state", func() {
		w := newWorld(agent)
		reds := w.fleet()
		w.daemon()
		w.settle(reds, func() bool { return w.pushes("repo-") == 4 })
		Eventually(func() int {
			w.runCI(reds)
			return w.report().Totals["mergeable"]
		}).WithTimeout(wait).WithPolling(500*time.Millisecond).Should(Equal(28), "every repaired pull request passes, and its hold clears")

		report := w.report()
		Expect(report.RepositoriesScanned).To(Equal(69))
		Expect(report.pulls()).To(Equal(28))
		Expect(w.questions()).To(BeEmpty(), "no one needed to be asked")
		Expect(w.pushes("repo-")).To(Equal(4), "one repair each")
	})

	It("a bad day keeps healthy work moving, and asks about the rest once", func() {
		w := newWorld(agent)
		reds := w.fleet()
		doomed := reds[0].repo
		reds[0].fixable = false
		// The first webhook is lost before anything is listening, so only a scan can find it.
		w.sim.NextDeliveries(simforge.Lost, simforge.Duplicated, simforge.Duplicated)
		w.runCI(reds)
		w.sim.FailRequests("/pulls", simforge.RateLimited(time.Second))
		w.daemon()

		w.settle(reds, func() bool { return len(w.questions()) > 0 })
		for start := time.Now(); time.Since(start) < 10*time.Second; time.Sleep(200 * time.Millisecond) {
			w.runCI(reds)
		}
		questions := w.questions()
		Expect(questions).To(HaveLen(1), "the failing one asks a person once")
		Expect(questions[0]).To(HavePrefix("@" + owner + " "))
		Expect(w.assigned()).To(Equal([]any{[]any{owner}}))
		Expect(w.pushes(doomed)).To(Equal(3), "stopped at the attempt cap")
		Expect(w.report().Totals).To(HaveKeyWithValue("needs-person", 1))
		Expect(len(w.model.Requests())).To(BeNumerically("<=", 2*20), "budgets held")
	})

	It("killing the process mid-run loses no effect and duplicates none", func() {
		w := newWorld(agent)
		w.model.Hold()
		repo := owner + "/repo-00"
		w.sim.AddRepo(simforge.PublicRepo(repo))
		w.sim.SetTarball(repo, source())
		branch := "renovate/pkg-0"
		number := w.sim.AddPull(repo, update(branch, "pkg-0"))
		broken := w.sim.Pull(repo, number).HeadSHA
		d := w.daemon()
		w.ci(repo, number, "failure", branch)

		Eventually(func() int { return len(w.model.Requests()) }).WithTimeout(wait).Should(BeNumerically(">", 0), "the run reaches its agent")
		d.crash()
		Expect(w.pushes(repo)).To(Equal(0), "killed before it could push")

		w.model.Release()
		w.daemon()
		Eventually(func() int { return w.pushes(repo) }).WithTimeout(wait).Should(Equal(1), "the repair lands")
		Consistently(func() int { return w.pushes(repo) }).WithTimeout(3*time.Second).Should(Equal(1), "the effect happened exactly once")
		Expect(w.sim.Pull(repo, number).HeadSHA).NotTo(Equal(broken))
	})

	It("a dry run leaves a live run everything still to do", func() {
		w := newWorld(agent)
		reds := w.fleet()
		w.runCI(reds)
		var dry struct {
			Backfill struct {
				Queued int `json:"queued"`
			} `json:"backfill"`
		}
		Expect(json.Unmarshal(w.verb("--format", "json", "backfill", "--dry-run"), &dry)).To(Succeed())
		Expect(dry.Backfill.Queued).To(BeNumerically(">", 0))
		seen := "SELECT count(*) FROM seen WHERE almanac = $1"
		Expect(w.count(seen, "almanac-renovate.dry-run")).To(BeNumerically(">", 0))
		Expect(w.count(seen, "almanac-renovate")).To(Equal(0), "live has seen nothing")
		Expect(w.pushes("repo-")).To(Equal(0), "a dry run pushes nothing")
	})

	It("a second reader of the log sees every delivery and disturbs nothing", func() {
		w := newWorld(agent)
		reds := w.fleet()
		w.daemon()
		_, err := w.pool.Exec(w.ctx, "INSERT INTO cursors (almanac, seq) VALUES ('second', 0)")
		Expect(err).NotTo(HaveOccurred())
		w.settle(reds, func() bool { return w.pushes("repo-") == 4 })

		logged := w.count("SELECT count(*) FROM deliveries")
		Expect(logged).To(BeNumerically(">", 0))
		first, err := deliveries.New(w.pool).After(w.ctx, 0, 10_000)
		Expect(err).NotTo(HaveOccurred())
		Expect(first).To(HaveLen(logged), "everything is there for a second reader")
		Expect(w.count("SELECT count(*) FROM cursors WHERE almanac = 'almanac-renovate'")).To(Equal(1))
		Expect(w.count("SELECT seq FROM cursors WHERE almanac = 'second'")).To(Equal(0), "its cursor is its own")
	})
})
