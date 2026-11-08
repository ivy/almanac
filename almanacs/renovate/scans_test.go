package renovate_test

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// state is what a found event's delivery ID says about when to look again.
func state(found workflow.Found) string {
	_, after, ok := strings.Cut(found.DeliveryID, "~")
	if !ok {
		return ""
	}
	return after
}

var _ = Describe("The scan for failing pull requests", func() {
	var w *world

	BeforeEach(func() { w = newWorld() })

	It("finds a red pull request as a failed check suite, cooled once its update has", func() {
		number, sha := w.pull(npmRow("lodash", "4.17.20", "4.17.21"))
		w.sim.AddSuite(repoName, simforge.FinishedSuite(sha, branch, "failure", "boom"))
		found := scan(w, "renovate.failing_pulls")
		Expect(found).To(HaveLen(1))
		Expect(found[0].Kind).To(Equal("github.check_suite.completed"))
		Expect(found[0].DeliveryID).To(HavePrefix("scan:owner/repo#1@" + sha))
		Expect(state(found[0])).To(ContainSubstring("cooled=false,held=false"))
		event, err := wire.Parse[renovate.BuildEvent](found[0].Payload)
		Expect(err).NotTo(HaveOccurred())
		Expect(event.Failed).To(BeTrue())
		Expect(event.Patched).To(BeFalse())
		Expect(event.Branch.String()).To(Equal(branch))
		Expect(number).To(Equal(uint64(1)))

		w.registry.release("lodash", "4.17.21", longAgo, nil)
		cooled := scan(w, "renovate.failing_pulls")
		Expect(state(cooled[0])).To(ContainSubstring("cooled=true"))
		Expect(cooled[0].DeliveryID).NotTo(Equal(found[0].DeliveryID))
	})

	It("finds a held repair whose CI passed, so a hold left by a run that died is cleared", func() {
		number, _ := w.pull("")
		patched := strings.Repeat("f", 40)
		w.sim.MoveBranch(repoName, int(number), patched, "fix(deps): repair\n\nAlmanac-Key: run:0")
		w.sim.AddSuite(repoName, simforge.FinishedSuite(patched, branch, "success", ""))
		w.status(patched, renovate.TrustContext, "pending")
		found := scan(w, "renovate.failing_pulls")
		Expect(found).To(HaveLen(1))
		Expect(state(found[0])).To(ContainSubstring("held=true"))
		event, err := wire.Parse[renovate.BuildEvent](found[0].Payload)
		Expect(err).NotTo(HaveOccurred())
		Expect(event.Patched).To(BeTrue())
		Expect(event.Failed).To(BeFalse())
	})

	It("passes over a green pull request, and a hold that is not on a repair", func() {
		_, sha := w.pull("")
		w.sim.AddSuite(repoName, simforge.FinishedSuite(sha, branch, "success", ""))
		Expect(scan(w, "renovate.failing_pulls")).To(BeEmpty())
		w.status(sha, renovate.TrustContext, "pending")
		Expect(scan(w, "renovate.failing_pulls")).To(BeEmpty())
	})

	It("passes over a pull request someone else opened", func() {
		p := simforge.RenovatePull(branch, "Update lodash")
		p.Author = "mallory"
		number := w.sim.AddPull(repoName, p)
		w.sim.AddSuite(repoName, simforge.FinishedSuite(w.sim.Pull(repoName, number).HeadSHA, branch, "failure", ""))
		Expect(scan(w, "renovate.failing_pulls")).To(BeEmpty())
	})

	It("refuses a backfill of a repository it cannot see, because scanning nothing reads as all clear", func() {
		entry, _ := renovate.Vocabulary.Lookup("renovate.failing_pulls")
		_, err := entry.Scan(context.Background(), scannerOf(w).Only(repoOf("owner/elsewhere")))
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("The scan for security pull requests", func() {
	var w *world

	BeforeEach(func() { w = newWorld() })

	security := func(branch string, labels ...string) (int, string) {
		p := simforge.RenovatePull(branch, "Update lodash [SECURITY]")
		p.Labels = labels
		number := w.sim.AddPull(repoName, p)
		return number, w.sim.Pull(repoName, number).HeadSHA
	}

	It("finds a labelled security pull request with no verdict, as a pull request opened", func() {
		number, sha := security("renovate/npm-lodash", "security")
		found := scan(w, "renovate.security_pulls")
		Expect(found).To(HaveLen(1))
		Expect(found[0].Kind).To(Equal("github.pull_request.opened"))
		Expect(found[0].DeliveryID).To(HavePrefix("scan:security:owner/repo#1@" + sha))
		Expect(state(found[0])).To(ContainSubstring("unknown,tested=false,gave_up=true"))
		event, err := wire.Parse[renovate.SecurityEvent](found[0].Payload)
		Expect(err).NotTo(HaveOccurred())
		Expect(event.Labeled).To(BeTrue())
		Expect(event.Number).To(Equal(uint64(number)))
		Expect(event.Sha.String()).To(Equal(sha))
	})

	It("finds one by Renovate's vulnerability branch name, with no label", func() {
		security("renovate/npm-lodash-vulnerability")
		Expect(scan(w, "renovate.security_pulls")).To(HaveLen(1))
	})

	It("passes over an ordinary update, and one with a final verdict", func() {
		security("renovate/npm-lodash")
		_, sha := security("renovate/npm-other-vulnerability")
		w.status(sha, renovate.TrustContext, "pending")
		Expect(scan(w, "renovate.security_pulls")).To(HaveLen(1))
		w.status(sha, renovate.TrustContext, "success")
		Expect(scan(w, "renovate.security_pulls")).To(BeEmpty())
	})

	It("looks again once CI has had its hour, and carries none of the pull request's text", func() {
		security("renovate/npm-lodash", "security")
		opened := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
		providers.Provide[clock.Clock](w.set, clock.NewFake(opened.Add(30*time.Minute)))
		early := scan(w, "renovate.security_pulls")
		Expect(state(early[0])).To(ContainSubstring("gave_up=false"))
		providers.Provide[clock.Clock](w.set, clock.NewFake(opened.Add(renovate.CIPatience)))
		late := scan(w, "renovate.security_pulls")
		Expect(state(late[0])).To(ContainSubstring("gave_up=true"))
		Expect(late[0].DeliveryID).NotTo(Equal(early[0].DeliveryID))
		var payload map[string]any
		Expect(json.Unmarshal(late[0].Payload, &payload)).To(Succeed())
		Expect(payload["pull_request"]).NotTo(HaveKey("title"))
		Expect(payload["pull_request"]).NotTo(HaveKey("body"))
	})
})

func repoOf(name string) hostile.Slug { return hostile.MustParseSlug(name) }
