package compare_test

import (
	"context"
	"io"
	"log/slog"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/advisory"
	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/compare"
	"github.com/ivy/almanac/almanacs/renovate/renovatetest"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/store/memory"
	"github.com/ivy/almanac/workflow"
)

// The names are synthetic: this repository is public.
const (
	owner    = "owner"
	repoName = "owner/repo"
	// buildBranch updates tar from 1.0.0 to 7.0.0, and its build fails.
	buildBranch = "renovate/tar-7.x"
	buildBody   = "| Package | Change |\n|---|---|\n| [tar](https://x) | [`1` → `7`](https://renovatebot.com/diffs/npm/tar/1.0.0/7.0.0) |\n"
	// securityBranch fixes an advisory in tar, from 6.2.0 to 6.2.1.
	securityBranch = "renovate/npm-tar-vulnerability"
	securityBody   = "| Package | Change |\n|---|---|\n| [tar](https://x) | [`6.2.0` → `6.2.1`](https://renovatebot.com/diffs/npm/tar/6.2.0/6.2.1) |\n"

	// trailer is the line the Rust version's forge puts on every commit it makes.
	trailer = "\n\nAlmanac-Key: run-7:0"
)

// now is when every spec runs: a week after simforge opened its pull requests.
var now = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// world is a forge of Renovate pull requests, the registry and advisory feed their updates are
// read from, and the Go version running read-only beside them: a dry run that calls no model
// and writes nothing. What the Rust version did is set on the forge by hand.
type world struct {
	*renovatetest.World
	ctx       context.Context
	sim       *simforge.Forge
	store     *memory.Store
	workflows []workflow.Registered
}

func newWorld() *world {
	GinkgoHelper()
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	sim.AddRepo(simforge.Repo{FullName: repoName, Workflows: true})
	workflows, err := compare.Workflows()
	Expect(err).NotTo(HaveOccurred())
	return &world{
		World:     renovatetest.NewWorld(sim, owner, now),
		ctx:       context.Background(),
		sim:       sim,
		store:     memory.New(),
		workflows: workflows,
	}
}

// failingBuild is a Renovate pull request whose build failed on its head, for an update
// published age ago.
func (w *world) failingBuild(age time.Duration) (number int, head string) {
	GinkgoHelper()
	pull := simforge.RenovatePull(buildBranch, "Update tar")
	pull.Body = buildBody
	number = w.sim.AddPull(repoName, pull)
	head = w.sim.Pull(repoName, number).HeadSHA
	w.sim.AddSuite(repoName, simforge.FinishedSuite(head, buildBranch, "failure", "error: boom"))
	w.Registry.Release("tar", "7.0.0", now.Add(-age), nil)
	return number, head
}

// securityFix is a Renovate security pull request for tar, labelled as one. When it names the
// fix, its body holds Renovate's table and the feed names the fix, so it passes every check
// and reaches the reviewer; when it does not, its body holds no table.
func (w *world) securityFix(namesTheFix bool) (number int, head string) {
	GinkgoHelper()
	pull := simforge.RenovatePull(securityBranch, "Update tar [SECURITY]")
	pull.Labels = []string{"security"}
	if namesTheFix {
		pull.Body = securityBody
		from, to := hostile.MustParseSlug("6.0.0"), hostile.MustParseSlug("6.2.1")
		w.Advisories.With(advisory.Advisory{
			ID: hostile.MustParseSlug("GHSA-tar0-tar0-tar0"),
			Affected: []advisory.Affected{{
				Ecosystem: hostile.MustParseSlug("npm"),
				Package:   hostile.MustParseSlug("tar"),
				Ranges:    []advisory.Range{{Introduced: &from, Fixed: &to}},
			}},
			Summary: hostile.New("A path traversal"),
		})
		w.Registry.Release("tar", "6.2.0", now.Add(-1000*time.Hour), map[string]string{"index.js": "old\n"})
		w.Registry.Release("tar", "6.2.1", now.Add(-200*time.Hour), map[string]string{"index.js": "new\n"})
	}
	number = w.sim.AddPull(repoName, pull)
	head = w.sim.Pull(repoName, number).HeadSHA
	w.sim.AddSuite(repoName, simforge.FinishedSuite(head, securityBranch, "success", ""))
	return number, head
}

// goRuns has the Go version scan and run what it found, read-only, as a dry-run daemon does.
// It returns how each run came out.
func (w *world) goRuns() []runtime.Outcome {
	GinkgoHelper()
	rt := runtime.New(w.workflows, w.Set,
		runtime.ReadOnly(),
		runtime.WithStore(w.store),
		runtime.WithClock(w.Clock),
		runtime.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	_, err := rt.Backfill(w.ctx, nil)
	Expect(err).NotTo(HaveOccurred())
	outcomes, err := rt.Drain(w.ctx)
	Expect(err).NotTo(HaveOccurred())
	Expect(w.sim.Writes()).To(BeEmpty(), "a read-only Go run writes nothing")
	return outcomes
}

// rustStatus sets almanac/trust on a commit, as the Rust version does.
func (w *world) rustStatus(sha, state string) {
	GinkgoHelper()
	Expect(renovatetest.SetStatus(w.sim, repoName, sha, renovate.TrustContext, state)).To(Succeed())
}

// rustRepair pushes a commit, sha, carrying the Rust version's trailer onto the pull request's
// head.
func (w *world) rustRepair(number int, sha string) {
	w.sim.MoveBranch(repoName, number, sha, "fix(deps): repair the build after this update"+trailer)
}

// rustAsk asks a person on the pull request through a tracker that marks the question with its
// key, as the Rust version's does.
func (w *world) rustAsk(number int, key string) {
	GinkgoHelper()
	question := effect.Question{
		Question: "A repair was refused by the patch policy",
		Target:   hostile.MustParseSlug(repoName),
		Pull:     uint64(number),
		Body:     hostile.New("the patch"),
	}
	Expect(github.NewIssues(w.Client, "").Ask(w.ctx, key, question)).To(Succeed())
}

// compared is what compare-effects finds now, reading the forge through a client that refuses
// every write.
func (w *world) compared() compare.Effects {
	GinkgoHelper()
	writes := len(w.sim.Writes())
	before, err := w.store.RunsSince(w.ctx, time.Time{})
	Expect(err).NotTo(HaveOccurred())
	found, err := compare.CompareEffects(w.ctx, compare.EffectSources{
		GitHub:    github.New(w.sim.URL(), github.Token("t"), github.ReadOnly()),
		Store:     w.store,
		Workflows: w.workflows,
		Owner:     owner,
		Author:    simforge.Renovate,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(w.sim.Writes()).To(HaveLen(writes), "a comparison writes nothing to GitHub")
	after, err := w.store.RunsSince(w.ctx, time.Time{})
	Expect(err).NotTo(HaveOccurred())
	Expect(after).To(Equal(before), "a comparison starts no run")
	return found
}

// only is the one pull request a comparison found.
func only(e compare.Effects) compare.PullEffects {
	GinkgoHelper()
	Expect(e.Pulls).To(HaveLen(1))
	return e.Pulls[0]
}

// says is what each action says, in order.
func says(actions []compare.Action) []string {
	out := make([]string, len(actions))
	for i, a := range actions {
		out[i] = a.Says
	}
	return out
}

// failedAt is the outcome of every run, which a read-only run that reaches an agent ends with.
func failedAt(outcomes []runtime.Outcome) []string {
	var reasons []string
	for _, o := range outcomes {
		if o.Result == runtime.Failed {
			reasons = append(reasons, o.Reason)
		}
	}
	return reasons
}

// short is a SHA as a comparison prints it.
func short(sha string) string { return sha[:7] }
