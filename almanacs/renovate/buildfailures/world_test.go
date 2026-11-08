package buildfailures_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/buildfailures"
	"github.com/ivy/almanac/almanacs/renovate/renovatetest"
	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/workflow"
)

const (
	repoName = "owner/repo"
	branch   = "renovate/tar-7.x"
	// body is a Renovate pull request's table, updating tar from 1.0.0 to 7.0.0 on npm.
	body = "| Package | Change |\n|---|---|\n| [tar](https://x) | [`1` → `7`](https://renovatebot.com/diffs/npm/tar/1.0.0/7.0.0) |\n"
	// buildLog is what a failed job's log ends with.
	buildLog = "error[E0308]: mismatched types"

	// cooledAge is old enough to be past the cooldown, and youngAge is not.
	cooledAge = 100 * time.Hour
	youngAge  = 10 * time.Hour
)

// now is when every spec runs: a week after simforge opened its pull requests.
var now = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// The fixer's answers.
var (
	// fixedMain repairs the build in src/main.rs, a file the update did not touch.
	fixedMain = writing("src/main.rs", "x")
	// touchesCI changes a workflow, which the patch policy refuses.
	touchesCI = writing(".github/workflows/ci.yml", "x")
	// undoesUpdate puts tar in package.json back to the version the update replaced.
	undoesUpdate = writing("package.json", "{\n  \"tar\": \"6.0.0\"\n}\n")
	// changesNothing explains why there is nothing to push.
	changesNothing = `{"answer": {"summary": "This needs a CI change."}, "diff": "", "changes": []}`
)

// writing is a fixer's answer that writes text to path.
func writing(path, text string) string {
	content := base64.StdEncoding.EncodeToString([]byte(text))
	return `{"answer": {"summary": "fixed"}, "diff": "the diff", "changes": [
		{"Write": {"path": "` + path + `", "content": "` + content + `", "executable": false}}]}`
}

// world is a forge with one Renovate pull request on branch, a registry, a scripted fixer, and
// a live runtime running build_failures over them.
type world struct {
	*renovatetest.World
	ctx      context.Context
	sim      *simforge.Forge
	fixer    *almanactest.ScriptedHarness
	tracker  *almanactest.FakeTracker
	received *receiver
	// set is what the runtime reaches: the vocabulary's providers, the forge, the tracker and
	// the fixer.
	set    *providers.Set
	rt     *runtime.Runtime
	number int
	next   int
}

// pullEdit changes the pull request a world starts with.
type pullEdit func(*simforge.Pull)

// updating is a pull request whose update changed file, with diff as its patch.
func updating(file, diff string) pullEdit {
	return func(p *simforge.Pull) {
		p.Files = append(p.Files, file)
		if p.Diffs == nil {
			p.Diffs = map[string]string{}
		}
		p.Diffs[file] = diff
	}
}

// openedBy is a pull request someone other than Renovate opened.
func openedBy(author string) pullEdit {
	return func(p *simforge.Pull) { p.Author = author }
}

// inConflict is a pull request GitHub says cannot merge.
func inConflict(p *simforge.Pull) { p.MergeableState = "dirty" }

// newWorld is a world whose fixer answers reply. Nothing dates its update until released says
// when it was published.
func newWorld(reply string, edits ...pullEdit) *world {
	GinkgoHelper()
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	sim.AddRepo(simforge.PublicRepo(repoName))
	pull := simforge.RenovatePull(branch, "Update tar")
	pull.Body = body
	for _, edit := range edits {
		edit(&pull)
	}
	w := &world{
		World:    renovatetest.NewWorld(sim, "owner", now),
		ctx:      context.Background(),
		sim:      sim,
		fixer:    almanactest.Replying(reply),
		tracker:  &almanactest.FakeTracker{},
		received: receiving(sim),
		number:   sim.AddPull(repoName, pull),
	}
	w.set = w.Set.Clone()
	providers.Provide[effect.Forge](w.set, github.NewForge(w.Client, github.DefaultAuthor()))
	providers.Provide[effect.Tracker](w.set, w.tracker)
	w.set.UseHarnessFor("fixer", w.fixer).ActAs("almanac[bot]")
	w.rt = runtime.New(workflows(), w.set,
		runtime.WithClock(w.Clock),
		runtime.InMode(effect.Live),
		runtime.WithLogger(slog.New(slog.NewTextHandler(GinkgoWriter, nil))),
	)
	Expect(w.rt.Validate()).To(Succeed())
	return w
}

// released publishes the release the update installs, age before now.
func (w *world) released(age time.Duration) *world {
	w.Registry.Release("tar", "7.0.0", now.Add(-age), nil)
	return w
}

// workflows is build_failures, ready to run.
func workflows() []workflow.Registered {
	GinkgoHelper()
	bf, err := buildfailures.New()
	Expect(err).NotTo(HaveOccurred())
	return []workflow.Registered{bf}
}

// head is the pull request's head commit now.
func (w *world) head() string { return w.sim.Pull(repoName, w.number).HeadSHA }

// suiteFinishes finishes a check suite on the head with conclusion, and delivers its webhook.
func (w *world) suiteFinishes(conclusion string) store.Ingest {
	GinkgoHelper()
	suite := w.sim.AddSuite(repoName, simforge.FinishedSuite(w.head(), branch, conclusion, buildLog))
	w.sim.SuiteCompleted(w.ctx, repoName, suite)
	return w.deliver("github.check_suite.completed")
}

// deliver ingests the last webhook the forge sent, as kind, under a new delivery ID.
func (w *world) deliver(kind string) store.Ingest {
	GinkgoHelper()
	w.next++
	ingest, err := w.rt.Ingest(w.ctx, runtime.Envelope{
		Source:     "github",
		DeliveryID: fmt.Sprintf("d%d", w.next),
		Kind:       kind,
		Origin:     store.Webhook,
		Payload:    w.received.last(),
	})
	Expect(err).NotTo(HaveOccurred())
	return ingest
}

// settle runs everything queued and applies what the runs decided.
func (w *world) settle() []runtime.Outcome {
	GinkgoHelper()
	outcomes, err := w.rt.Settle(w.ctx)
	Expect(err).NotTo(HaveOccurred())
	for _, outcome := range outcomes {
		Expect(outcome.Result).NotTo(Equal(runtime.Failed), outcome.Reason)
	}
	return outcomes
}

// backfill scans for pull requests that need a run, and queues them.
func (w *world) backfill() runtime.Backfill {
	GinkgoHelper()
	report, err := w.rt.Backfill(w.ctx, nil)
	Expect(err).NotTo(HaveOccurred())
	return report
}

// status sets a commit status on the forge, as a run's hold does.
func (w *world) status(sha, state string) {
	GinkgoHelper()
	Expect(renovatetest.SetStatus(w.sim, repoName, sha, renovate.TrustContext, state)).To(Succeed())
}

// posted is a status written to the forge: the commit it is on, and its state.
type posted struct{ sha, state string }

// statuses are the statuses written to the forge, oldest first.
func (w *world) statuses() []posted {
	var all []posted
	for _, write := range w.sim.Writes() {
		if !strings.Contains(write.Path, "/statuses/") {
			continue
		}
		state, _ := write.Body["state"].(string)
		all = append(all, posted{sha: write.Path[strings.LastIndex(write.Path, "/")+1:], state: state})
	}
	return all
}

// lastStatus is the latest status written to the forge.
func (w *world) lastStatus() posted {
	GinkgoHelper()
	all := w.statuses()
	Expect(all).NotTo(BeEmpty())
	return all[len(all)-1]
}

// pushes is how many times a branch moved.
func (w *world) pushes() int {
	count := 0
	for _, write := range w.sim.Writes() {
		if write.Method == http.MethodPatch {
			count++
		}
	}
	return count
}

// asked are the titles of the questions put to people.
func (w *world) asked() []string {
	var titles []string
	for _, filed := range w.tracker.Filed() {
		titles = append(titles, filed.Title)
	}
	return titles
}

// planned is effects as the outbox stores them.
func planned(effects []effect.Effect) string {
	GinkgoHelper()
	plans := make([]effect.Planned, len(effects))
	for i, e := range effects {
		var err error
		plans[i], err = effect.Plan(e)
		Expect(err).NotTo(HaveOccurred())
	}
	raw, err := json.Marshal(plans)
	Expect(err).NotTo(HaveOccurred())
	return string(raw)
}

// receiver collects the webhook payloads a forge delivers.
type receiver struct {
	mu       sync.Mutex
	payloads []json.RawMessage
}

func receiving(sim *simforge.Forge) *receiver {
	r := &receiver{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		Expect(err).NotTo(HaveOccurred())
		r.mu.Lock()
		r.payloads = append(r.payloads, body)
		r.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	DeferCleanup(server.Close)
	sim.DeliverTo(server.URL, "secret")
	return r
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.payloads)
}

func (r *receiver) last() json.RawMessage {
	GinkgoHelper()
	r.mu.Lock()
	defer r.mu.Unlock()
	Expect(r.payloads).NotTo(BeEmpty())
	return r.payloads[len(r.payloads)-1]
}
