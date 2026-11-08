package e2e_test

import (
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"

	"github.com/ivy/almanac/simforge"
)

// These specs drive security_fixes, which no Rust end-to-end test does: a security update is
// held, checked against its advisory and its package, and read by the reviewer through the
// real claude CLI.
var _ = Describe("The Renovate almanac's security fixes, end to end", Label("e2e"), func() {
	// secure holds a release of tar that fixes its advisory, and locked one of lodash that also
	// adds an install script.
	var w *world
	BeforeEach(func() {
		w = newWorld(agent)
		w.sim.AddRepo(simforge.PublicRepo(alerts))
		w.advisories.fixes("GHSA-tar0-tar0-tar0", "tar", "6.0.0", "6.2.1")
		w.advisories.fixes("GHSA-lodh-lodh-lodh", "lodash", "4.0.0", "4.17.21")
		w.registry.release("tar", "6.2.0", 1000, map[string]string{"index.js": "old\n", "package.json": `{"name": "tar"}`})
		w.registry.release("tar", "6.2.1", 200, map[string]string{"index.js": "new\n", "package.json": `{"name": "tar"}`})
		w.registry.release("lodash", "4.17.20", 1000, map[string]string{"index.js": "old\n", "package.json": `{"name": "lodash"}`})
		w.registry.release("lodash", "4.17.21", 200, map[string]string{
			"index.js": "new\n", "package.json": `{"name": "lodash", "scripts": {"postinstall": "node setup.js"}}`,
		})
	})

	It("proves a fix once however its webhook arrives, ends the chain at its own status, and refuses a lost one a scan finds", func() {
		d := w.daemon()
		d.scanned()
		secure, tarPull, tarHead := w.securityPull("secure", "tar", "6.2.0", "6.2.1")
		locked, lodashPull, lodashHead := w.securityPull("locked", "lodash", "4.17.20", "4.17.21")

		// GitHub sends the first twice and loses the second.
		w.sim.NextDeliveries(simforge.Duplicated, simforge.Lost)
		w.sim.PullRequest(w.ctx, secure, tarPull, "opened")
		w.sim.PullRequest(w.ctx, locked, lodashPull, "opened")
		Eventually(func() []string { return w.statuses(secure, tarHead) }).WithTimeout(wait).
			Should(Equal([]string{"pending", "success"}), "held, then proved: tested, the fix its advisory names, and no concerns")
		Expect(w.questions()).To(BeEmpty())

		// GitHub reports Almanac's own status back to it, and build_failures turns it away.
		w.sim.StatusChanged(w.ctx, secure, tarHead, trust, "success")
		d.took(w, 2)
		Consistently(func() []string { return w.statuses(secure, tarHead) }).WithTimeout(3*time.Second).
			Should(HaveLen(2), "a chain ends at the status it set")
		Expect(w.statuses(locked, lodashHead)).To(BeEmpty(), "its webhook was lost")

		// A backfill finds the lost one, and passes over the one already proved.
		var found struct {
			Backfill struct {
				Found int `json:"found"`
			} `json:"backfill"`
		}
		w.env = append(w.env, "ALMANAC_MODE=live")
		Expect(json.Unmarshal(w.verb("--format", "json", "backfill"), &found)).To(Succeed())
		Expect(found.Backfill.Found).To(Equal(1), "a final verdict is not looked at again")
		Eventually(func() []string { return w.statuses(locked, lodashHead) }).WithTimeout(wait).
			Should(Equal([]string{"pending", "failure"}), "held, then refused")
		questions := w.questions()
		Expect(questions).To(HaveLen(1), "asked about once")
		Expect(questions[0]).To(HavePrefix("@" + owner + " "))
		Expect(questions[0]).To(ContainSubstring("adds an install script"))
		Expect(w.statuses(secure, tarHead)).To(HaveLen(2))
		Expect(w.reviews()).To(Equal(1), "only the update that passed every check reached the reviewer")
	})

	It("holds work past its run budget, and tells a person once", func() {
		w.env = append(w.env, "ALMANAC_RUNS_PER_HOUR_CAP=1")
		w.daemon().scanned()
		heads := map[string]string{}
		for i := range 3 {
			repo, number, head := w.securityPull(fmt.Sprintf("secure-%d", i), "tar", "6.2.0", "6.2.1")
			heads[repo] = head
			w.sim.PullRequest(w.ctx, repo, number, "opened")
		}

		Eventually(func() []string { return w.issues(alerts) }).WithTimeout(wait).
			Should(Equal([]string{"A run budget was reached for security_fixes"}))
		proved := func() int {
			n := 0
			for repo, head := range heads {
				if len(w.statuses(repo, head)) > 0 {
					n++
				}
			}
			return n
		}
		Eventually(proved).WithTimeout(wait).Should(Equal(1))
		Consistently(proved).WithTimeout(5*time.Second).Should(Equal(1), "the rest wait for the hourly window")
		Expect(w.issues(alerts)).To(HaveLen(1), "a person is told once a window")
		Expect(w.reviews()).To(Equal(1))
	})
})

// securityPull opens Renovate's pull request on name's vulnerability branch in a new
// repository with CI, updating pkg from one version to another, and CI passes on it. It returns
// the repository, the pull request's number and its head.
func (w *world) securityPull(name, pkg, from, to string) (string, int, string) {
	repo := owner + "/" + name
	w.sim.AddRepo(simforge.Repo{FullName: repo, Workflows: true})
	branch := "renovate/npm-" + pkg + "-vulnerability"
	p := simforge.RenovatePull(branch, "Update "+pkg+" [SECURITY]")
	p.Body = table + row(pkg, from, to)
	number := w.sim.AddPull(repo, p)
	head := w.sim.Pull(repo, number).HeadSHA
	w.sim.AddSuite(repo, simforge.FinishedSuite(head, branch, "success", ""))
	return repo, number, head
}

// reviews is how many sessions the reviewer had: requests offered no tools that the model
// answered.
func (w *world) reviews() int {
	n := 0
	for _, raw := range w.model.Requests() {
		var r struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(raw, &r) == nil && len(r.Tools) == 0 {
			n++
		}
	}
	return n
}

// scanned waits for the daemon's first scheduled scan to finish, so that what the spec adds
// afterwards reaches it only by webhook or by a later scan.
func (d *daemon) scanned() {
	GinkgoHelper()
	Eventually(d.session.Err).WithTimeout(wait).Should(gbytes.Say(`"msg":"scheduled scan finished"`))
}

// took waits until the daemon has read to the end of the log, which holds n deliveries: one a
// duplicate is logged once, and one GitHub lost not at all.
func (d *daemon) took(w *world, n int) {
	GinkgoHelper()
	Eventually(func() int {
		return w.count("SELECT coalesce(max(seq), 0) FROM deliveries") - w.count("SELECT seq FROM cursors WHERE almanac = 'almanac-renovate'")
	}).WithTimeout(wait).Should(Equal(0), "the daemon reads to the end of the log")
	Expect(w.count("SELECT count(*) FROM deliveries")).To(Equal(n))
}
