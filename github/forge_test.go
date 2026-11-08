package github_test

import (
	"path"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/simforge"
)

func forge(sim *simforge.Forge) *github.Forge {
	return github.NewForge(client(sim), github.DefaultAuthor())
}

func attest(sim *simforge.Forge, number uint64) effect.Attest {
	return effect.Attest{
		Context: "almanac/trust",
		State:   effect.Pending,
		Repo:    slug,
		Sha:     hostile.MustParseSha(sim.Pull(repo, int(number)).HeadSHA),
	}
}

// commitOf is a patch that writes one file and deletes another, for the pull request's head.
func commitOf(sim *simforge.Forge, number uint64, onto string) effect.Commit {
	return effect.Commit{
		Repo:    slug,
		Pull:    number,
		Branch:  hostile.MustParseSlug(onto),
		Message: "fix: repair the build",
		Base:    hostile.MustParseSha(sim.Pull(repo, int(number)).HeadSHA),
		Patch: hostile.New(patch.Patch{Changes: []patch.Change{
			{Op: patch.Write, Path: "src/main.rs", Content: "eA=="},
			{Op: patch.Delete, Path: "src/old.rs"},
		}}),
	}
}

func lastSegments(writes []simforge.Write) []string {
	var segments []string
	for _, w := range writes {
		segments = append(segments, path.Base(w.Path))
	}
	return segments
}

var _ = Describe("The GitHub forge", func() {
	// crates/github/tests/client.rs: a_status_is_posted
	It("posts a status", func(ctx SpecContext) {
		sim, number := scripted()
		Expect(forge(sim).Attest(ctx, "k", attest(sim, number))).To(Succeed())
		writes := sim.Writes()
		Expect(writes).To(HaveLen(1))
		Expect(writes[0].Path).To(Equal("/repos/owner/repo/statuses/" + sim.Pull(repo, int(number)).HeadSHA))
		Expect(writes[0].Body).To(Equal(map[string]any{"state": "pending", "context": "almanac/trust"}))
	})

	It("posts each state by GitHub's name for it", func(ctx SpecContext) {
		sim, number := scripted()
		for _, state := range []effect.AttestState{effect.Success, effect.Failure} {
			status := attest(sim, number)
			status.State = state
			Expect(forge(sim).Attest(ctx, "k", status)).To(Succeed())
		}
		writes := sim.Writes()
		Expect(writes[0].Body["state"]).To(Equal("success"))
		Expect(writes[1].Body["state"]).To(Equal("failure"))
	})

	// crates/github/tests/client.rs: a_patch_lands_as_one_commit_by_the_stable_author
	It("lands a patch as one commit by the stable author", func(ctx SpecContext) {
		sim, number := scripted()
		commit := commitOf(sim, number, branch)
		base := commit.Base.String()
		Expect(forge(sim).Push(ctx, "run-1:0", commit)).To(Succeed())

		writes := sim.Writes()
		Expect(lastSegments(writes)).To(Equal([]string{"blobs", "trees", "commits", "lodash-4.x"}))
		Expect(writes[0].Body).To(Equal(map[string]any{"content": "eA==", "encoding": "base64"}))
		tree := writes[1].Body
		Expect(tree["base_tree"]).To(Equal(base))
		entries := tree["tree"].([]any)
		Expect(entries[0]).To(HaveKeyWithValue("mode", "100644"))
		Expect(entries[1]).To(HaveKeyWithValue("sha", BeNil()), "a delete")
		made := writes[2].Body
		Expect(made["author"]).To(Equal(map[string]any{"name": "almanac[bot]", "email": "almanac[bot]@users.noreply.github.com"}))
		Expect(made["committer"]).To(Equal(made["author"]))
		Expect(made["parents"]).To(Equal([]any{base}))
		Expect(made["message"]).To(Equal("fix: repair the build\n\nAlmanac-Key: run-1:0"))
		Expect(writes[3].Method).To(Equal("PATCH"))
		Expect(writes[3].Body["force"]).To(BeFalse())
		Expect(sim.Pull(repo, int(number)).HeadSHA).NotTo(Equal(base))
	})

	It("holds the new commit pending before the branch moves to it", func(ctx SpecContext) {
		sim, number := scripted()
		commit := commitOf(sim, number, branch)
		commit.Hold = "almanac/trust"
		commit.Patch = hostile.New(patch.Patch{Changes: []patch.Change{{Op: patch.Write, Path: "run.sh", Content: "eA==", Executable: true}}})
		Expect(forge(sim).Push(ctx, "run-1:0", commit)).To(Succeed())

		writes := sim.Writes()
		Expect(lastSegments(writes)).To(HaveLen(5))
		Expect(writes[1].Body["tree"].([]any)[0]).To(HaveKeyWithValue("mode", "100755"))
		hold := writes[3]
		Expect(hold.Path).To(HavePrefix("/repos/owner/repo/statuses/"))
		Expect(hold.Body).To(Equal(map[string]any{"state": "pending", "context": "almanac/trust"}))
		Expect(strings.TrimPrefix(hold.Path, "/repos/owner/repo/statuses/")).To(Equal(sim.Pull(repo, int(number)).HeadSHA))
		Expect(writes[4].Method).To(Equal("PATCH"))
	})

	// crates/github/tests/client.rs: pushing_to_another_branch_is_refused
	It("refuses to push to another branch, and writes nothing", func(ctx SpecContext) {
		sim, number := scripted()
		err := forge(sim).Push(ctx, "k", commitOf(sim, number, "main"))
		Expect(err).To(haveCode(diag.ForgePushRefused))
		Expect(err).To(MatchError(ContainSubstring("not this pull request's branch")))
		Expect(sim.Writes()).To(BeEmpty())
	})

	// crates/github/tests/client.rs: pushing_to_a_fork_is_refused
	It("refuses to push to a fork, and writes nothing", func(ctx SpecContext) {
		sim := simforge.Start()
		DeferCleanup(sim.Close)
		sim.AddRepo(simforge.PublicRepo(repo))
		pull := simforge.RenovatePull(branch, "From a fork")
		pull.HeadRepo = "stranger/repo"
		number := uint64(sim.AddPull(repo, pull))

		err := forge(sim).Push(ctx, "k", commitOf(sim, number, branch))
		Expect(err).To(haveCode(diag.ForgePushRefused))
		Expect(err).To(MatchError(ContainSubstring("fork")))
		Expect(sim.Writes()).To(BeEmpty())
	})

	It("refuses to push to a pull request that is not open", func(ctx SpecContext) {
		sim := simforge.Start()
		DeferCleanup(sim.Close)
		sim.AddRepo(simforge.PublicRepo(repo))
		pull := simforge.RenovatePull(branch, "Closed")
		pull.State = "closed"
		number := uint64(sim.AddPull(repo, pull))

		err := forge(sim).Push(ctx, "k", commitOf(sim, number, branch))
		Expect(err).To(MatchError(ContainSubstring("not open")))
		Expect(sim.Writes()).To(BeEmpty())
	})

	// crates/github/tests/client.rs: a_branch_that_moved_refuses_the_push
	It("refuses the push when the branch moved after the patch was checked", func(ctx SpecContext) {
		sim, number := scripted()
		commit := commitOf(sim, number, branch)
		sim.MoveBranch(repo, int(number), strings.Repeat("b", 40), "someone else's commit")
		err := forge(sim).Push(ctx, "k", commit)
		Expect(err).To(MatchError(ContainSubstring("moved")))
		Expect(sim.Writes()).To(BeEmpty())
	})

	// crates/github/tests/client.rs: a_retry_after_a_crash_recognises_its_own_commit
	It("recognises its own commit on a retry after a crash", func(ctx SpecContext) {
		sim, number := scripted()
		commit := commitOf(sim, number, branch)
		f := forge(sim)
		Expect(f.Push(ctx, "run-1:0", commit)).To(Succeed())
		writes := len(sim.Writes())
		Expect(f.Push(ctx, "run-1:0", commit)).To(Succeed())
		Expect(sim.Writes()).To(HaveLen(writes), "applying twice is a no-op")

		Expect(f.Push(ctx, "run-2:0", commit)).To(MatchError(ContainSubstring("moved")), "another run's key is not its own")
	})

	It("refuses a link, which no checked patch holds", func(ctx SpecContext) {
		sim, number := scripted()
		commit := commitOf(sim, number, branch)
		commit.Patch = hostile.New(patch.Patch{Changes: []patch.Change{{Op: patch.Link, Path: "x"}}})
		Expect(forge(sim).Push(ctx, "k", commit)).To(MatchError(ContainSubstring("link")))
		Expect(sim.Writes()).To(BeEmpty())
	})
})
