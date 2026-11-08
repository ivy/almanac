package renovate_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/renovatetest"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/workflow"
)

const (
	repoName = "owner/repo"
	branch   = "renovate/lodash-4.x"
)

var (
	repo = hostile.MustParseSlug(repoName)
	// now is when every spec runs: a week after the forge's pull requests were opened.
	now = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
)

// world is a forge with one repository, and the providers a vocabulary reads it through.
type world struct {
	*renovatetest.World
	sim *simforge.Forge
}

func newWorld() *world {
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	sim.AddRepo(simforge.PublicRepo(repoName))
	return &world{World: renovatetest.NewWorld(sim, "owner", now), sim: sim}
}

// pull adds a Renovate pull request on branch whose body is body, and returns its number and
// head.
func (w *world) pull(body string) (uint64, string) {
	p := simforge.RenovatePull(branch, "Update lodash")
	p.Body = body
	number := w.sim.AddPull(repoName, p)
	return uint64(number), w.sim.Pull(repoName, number).HeadSHA
}

// status sets a commit status, as anything with write access can.
func (w *world) status(sha, context, state string) {
	Expect(renovatetest.SetStatus(w.sim, repoName, sha, context, state)).To(Succeed())
}

// observe reads through the part registered as name, as a run's observe step does, and decodes
// the reading as a run's record of it would.
func observe[Out any](w *world, name string, in any) Out {
	entry, ok := renovate.Vocabulary.Lookup(name)
	Expect(ok).To(BeTrue(), name)
	raw, err := entry.Observe(context.Background(), scannerOf(w), []any{in})
	Expect(err).NotTo(HaveOccurred())
	value, err := vocab.Decode(entry.Result, raw)
	Expect(err).NotTo(HaveOccurred())
	return value.(Out)
}

// observeErr is the error reading through the part registered as name.
func observeErr(w *world, name string, in any) error {
	entry, ok := renovate.Vocabulary.Lookup(name)
	Expect(ok).To(BeTrue(), name)
	_, err := entry.Observe(context.Background(), scannerOf(w), []any{in})
	return err
}

func scannerOf(w *world) *workflow.Scanner { return workflow.NewScanner(w.Set) }

// scan runs the scan registered as name over the world.
func scan(w *world, name string) []workflow.Found {
	entry, ok := renovate.Vocabulary.Lookup(name)
	Expect(ok).To(BeTrue(), name)
	found, err := entry.Scan(context.Background(), scannerOf(w))
	Expect(err).NotTo(HaveOccurred())
	return found
}

// npmRow is a Renovate table updating an npm package from one version to another.
func npmRow(name, current, version string) string {
	return "| Package | Change |\n|---|---|\n" +
		"| [" + name + "](https://example.com) | [`" + current + "` → `" + version + "`](https://renovatebot.com/diffs/npm/" + name + "/" + current + "/" + version + ") |\n"
}

// text is a pull request's text holding body, as an observer reads it.
func text(body string) hostile.Of[renovate.PullText] {
	return hostile.New(renovate.PullText{Body: &body})
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

func (r *receiver) last() json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	Expect(r.payloads).NotTo(BeEmpty())
	return r.payloads[len(r.payloads)-1]
}
