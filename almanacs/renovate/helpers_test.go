package renovate_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/advisory"
	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/registry"
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
	sim        *simforge.Forge
	set        *providers.Set
	registry   *archives
	advisories *advisory.Fake
	clock      *clock.Fake
}

func newWorld() *world {
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	sim.AddRepo(simforge.PublicRepo(repoName))
	w := &world{
		sim:        sim,
		set:        providers.New(),
		registry:   &archives{published: map[string]time.Time{}, files: map[string][]byte{}},
		advisories: advisory.NewFake(),
		clock:      clock.NewFake(now),
	}
	retry := github.Retrying(github.Retry{Attempts: 2, Backoff: time.Millisecond, LongestWait: time.Second})
	providers.Provide(w.set, github.New(sim.URL(), github.Token("token"), retry))
	providers.Provide(w.set, renovate.Target{Owner: "owner", Author: simforge.Renovate})
	providers.Provide[clock.Clock](w.set, w.clock)
	providers.Provide[registry.Registry](w.set, w.registry)
	providers.Provide[advisory.Advisories](w.set, w.advisories)
	return w
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
	body, err := json.Marshal(map[string]string{"context": context, "state": state})
	Expect(err).NotTo(HaveOccurred())
	request, err := http.NewRequest(http.MethodPost, w.sim.URL()+"/repos/"+repoName+"/statuses/"+sha, bytes.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	request.Header.Set("Authorization", "Bearer token")
	response, err := http.DefaultClient.Do(request)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.Body.Close()).To(Succeed())
	Expect(response.StatusCode).To(Equal(http.StatusCreated))
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

func scannerOf(w *world) *workflow.Scanner { return workflow.NewScanner(w.set) }

// scan runs the scan registered as name over the world.
func scan(w *world, name string) []workflow.Found {
	entry, ok := renovate.Vocabulary.Lookup(name)
	Expect(ok).To(BeTrue(), name)
	found, err := entry.Scan(context.Background(), scannerOf(w))
	Expect(err).NotTo(HaveOccurred())
	return found
}

// archives is a registry of releases and their archives, by name@version.
type archives struct {
	mu        sync.Mutex
	published map[string]time.Time
	files     map[string][]byte
	asked     []string
	broken    bool
}

var errRegistryDown = errors.New("the registry is down")

func (a *archives) release(name, version string, at time.Time, files map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.published[name+"@"+version] = at
	a.files[name+"@"+version] = tgz(files)
}

func (a *archives) Published(_ context.Context, p registry.Package) (time.Time, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, p.String())
	at, ok := a.published[p.String()]
	return at, ok, nil
}

func (a *archives) Artifact(_ context.Context, p registry.Package) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, p.String())
	if a.broken {
		return nil, errRegistryDown
	}
	archive, ok := a.files[p.String()]
	if !ok {
		return nil, registry.ErrNoArtifact
	}
	return archive, nil
}

func (a *archives) packages() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asked...)
}

// tgz is a package archive holding files under one top directory, as npm packs one.
func tgz(files map[string]string) []byte {
	var out bytes.Buffer
	zipped := gzip.NewWriter(&out)
	archive := tar.NewWriter(zipped)
	for name, content := range files {
		Expect(archive.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})).To(Succeed())
		_, err := io.Copy(archive, strings.NewReader(content))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(archive.Close()).To(Succeed())
	Expect(zipped.Close()).To(Succeed())
	return out.Bytes()
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
