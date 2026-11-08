package renovate_test

import (
	"context"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/wire"
)

var _ = Describe("A build finishing", func() {
	var (
		w        *world
		received *receiver
		sha      string
	)

	BeforeEach(func() {
		w = newWorld()
		received = receiving(w.sim)
		_, sha = w.pull("")
	})

	parsed := func() renovate.BuildEvent {
		event, err := wire.Parse[renovate.BuildEvent](received.last())
		Expect(err).NotTo(HaveOccurred())
		return event
	}

	It("reads a failed check suite as a failed build that finished", func() {
		suite := w.sim.AddSuite(repoName, simforge.FinishedSuite(sha, branch, "timed_out", ""))
		w.sim.SuiteCompleted(context.Background(), repoName, suite)
		event := parsed()
		Expect(event.Repo).To(Equal(repo))
		Expect(event.Branch.String()).To(Equal(branch))
		Expect(event.Sha.String()).To(Equal(sha))
		Expect(event.Failed).To(BeTrue())
		Expect(event.Finished).To(BeTrue())
		Expect(event.Patched).To(BeFalse())
		Expect(event.Bookkeeping).To(BeFalse())
	})

	It("reads a passing check suite on a commit Almanac patched as patched, and not failed", func() {
		patched := strings.Repeat("a", 40)
		w.sim.MoveBranch(repoName, 1, patched, "fix(deps): repair the build\n\nAlmanac-Key: run:0")
		suite := w.sim.AddSuite(repoName, simforge.FinishedSuite(patched, branch, "success", ""))
		w.sim.SuiteCompleted(context.Background(), repoName, suite)
		event := parsed()
		Expect(event.Failed).To(BeFalse())
		Expect(event.Patched).To(BeTrue())
	})

	DescribeTable("reads a commit status as a check suite",
		func(statusContext, state string, failed, finished, bookkeeping bool) {
			w.sim.StatusChanged(ctx(), repoName, sha, statusContext, state)
			event := parsed()
			Expect(event.Branch.String()).To(Equal(branch))
			Expect(event.Sha.String()).To(Equal(sha))
			Expect(event.Failed).To(Equal(failed))
			Expect(event.Finished).To(Equal(finished))
			Expect(event.Bookkeeping).To(Equal(bookkeeping))
		},
		Entry("a failure", "ci/test", "failure", true, true, false),
		Entry("an error", "ci/test", "error", true, true, false),
		Entry("a pending status, which has not finished", "ci/test", "pending", false, false, false),
		Entry("a success", "ci/test", "success", false, true, false),
		Entry("Almanac's own status, which says nothing about the build", "almanac/trust", "failure", true, true, true),
		Entry("Renovate's own status, which says nothing about the build", "renovate/stability-days", "pending", false, false, true),
	)

	It("parses a real webhook's green check suite as a finished build that did not fail", func() {
		content, err := os.ReadFile("fixtures/check-suite-green.json")
		Expect(err).NotTo(HaveOccurred())
		event, err := wire.Parse[renovate.BuildEvent](content)
		Expect(err).NotTo(HaveOccurred())
		Expect(event.Branch.String()).To(Equal("renovate/actions-checkout-7.x"))
		Expect(event.Finished).To(BeTrue())
		Expect(event.Failed).To(BeFalse())
	})

	It("parses a scan's check suite as a webhook's", func() {
		head := hostile.MustParseSha(sha)
		event, err := wire.Parse[renovate.BuildEvent](renovate.SuitePayload(repo, hostile.MustParseSlug(branch), head, true, true))
		Expect(err).NotTo(HaveOccurred())
		Expect(event).To(Equal(renovate.BuildEvent{
			Repo: repo, Branch: hostile.MustParseSlug(branch), Sha: head, Failed: true, Finished: true, Patched: true,
		}))
	})
})

var _ = Describe("A pull request opened or labelled", func() {
	var (
		w        *world
		received *receiver
	)

	BeforeEach(func() {
		w = newWorld()
		received = receiving(w.sim)
	})

	parsed := func() renovate.SecurityEvent {
		event, err := wire.Parse[renovate.SecurityEvent](received.last())
		Expect(err).NotTo(HaveOccurred())
		return event
	}

	It("reads the pull request, and that it carries no security label", func() {
		number, sha := w.pull("")
		w.sim.PullRequest(ctx(), repoName, int(number), "opened")
		event := parsed()
		Expect(event).To(Equal(renovate.SecurityEvent{
			Repo: repo, Branch: hostile.MustParseSlug(branch), Sha: hostile.MustParseSha(sha), Number: number,
		}))
	})

	It("reads a security label, wherever it is among the labels", func() {
		number, _ := w.pull("")
		w.sim.AddLabel(repoName, int(number), "security")
		w.sim.AddLabel(repoName, int(number), "dependencies")
		w.sim.PullRequest(ctx(), repoName, int(number), "labeled")
		Expect(parsed().Labeled).To(BeTrue())
	})
})

func ctx() context.Context { return context.Background() }
