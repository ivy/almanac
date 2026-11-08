package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// The workflows here are written by hand as workflow.Code values, so the specs control every
// read. They mirror the workflows in crates/definitions/tests/daemon.rs and
// almanacs/renovate/tests/cli.rs.

const (
	sha     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	trust   = "almanac/trust"
	held    = "owner/repo@" + sha + ":" + trust
	opened  = "github.pull_request.opened"
	okReply = `{"answer": "ok"}`
)

// pullRequest is a pull request opened, as a webhook delivers it.
type pullRequest struct {
	Number uint64
	Repo   hostile.Slug
	Branch hostile.Slug
	Head   hostile.Sha
	Title  hostile.Of[string]
}

func (pullRequest) Kind() string        { return opened }
func (pullRequest) AlsoKinds() []string { return nil }
func (pullRequest) Operation() string   { return "pulls/get" }

func (pullRequest) Projections() []wire.Projection {
	return []wire.Projection{
		{From: "/number", To: "/number"},
		{From: "/base/repo/full_name", To: "/repository/full_name"},
		{From: "/head/ref", To: "/pull_request/head/ref"},
		{From: "/head/sha", To: "/pull_request/head/sha"},
		{From: "/title", To: "/pull_request/title"},
	}
}

func (e *pullRequest) Parse(p *wire.Payload) {
	e.Number = wire.Field[uint64](p, "/number")
	e.Repo = wire.Field[hostile.Slug](p, "/repository/full_name")
	e.Branch = wire.Field[hostile.Slug](p, "/pull_request/head/ref")
	e.Head = wire.Field[hostile.Sha](p, "/pull_request/head/sha")
	e.Title = p.Text("/pull_request/title")
}

// pull is a pull request opened on owner/repo from renovate/p{number}, as a webhook sends it.
func pull(number int) []byte {
	return pullOn("owner/repo", fmt.Sprintf("renovate/p%d", number), number)
}

func pullOn(repo, branch string, number int) []byte {
	body, err := json.Marshal(map[string]any{
		"action":     "opened",
		"number":     number,
		"sender":     map[string]any{"login": "bot"},
		"repository": map[string]any{"full_name": repo},
		"pull_request": map[string]any{
			"title": "Update",
			"head":  map[string]any{"ref": branch, "sha": sha},
		},
	})
	Expect(err).NotTo(HaveOccurred())
	return body
}

var reviewer = agent.NewQuarantined("agent", agent.MustPrompt(agent.ParsePrompt("inline", "Review this.")))

// holding holds a pull request's commit, asks an agent, and attests success. It wants only
// Renovate's branches.
var holding = workflow.Code[pullRequest](workflow.Coded[pullRequest, pullRequest]{
	Name:        "holding",
	Intent:      "Hold, read, and attest.",
	May:         []effect.Kind{effect.KindAttest},
	RunsPerHour: 1000,
	Agents:      []agent.Info{reviewer.Info()},
	Wanted:      func(pr pullRequest) bool { return strings.HasPrefix(pr.Branch.String(), "renovate/") },
	Key:         func(pr pullRequest) string { return pr.Repo.String() + ":" + pr.Branch.String() },
	Gather: func(ctx context.Context, cx workflow.Cx, pr pullRequest) (pullRequest, error) {
		if err := cx.Hold(ctx, trust, pr.Repo, pr.Head); err != nil {
			return pr, err
		}
		_, err := workflow.Read[json.RawMessage](ctx, cx, reviewer, agent.Vars{}, pr.Title)
		return pr, err
	},
	Decide: func(pr pullRequest) []effect.Effect {
		return []effect.Effect{effect.Attest{Context: trust, State: effect.Success, Repo: pr.Repo, Sha: pr.Head}}
	},
})

// fleet is the repositories a scan can see, each with one open pull request.
type fleet struct{ repos []hostile.Slug }

// fleetScanner is what watching needs to scan.
type fleetScanner interface {
	Scan(ctx context.Context, scanner *workflow.Scanner) ([]workflow.Found, error)
}

func (f fleet) Scan(_ context.Context, scanner *workflow.Scanner) ([]workflow.Found, error) {
	repos, err := scanner.Narrow(f.repos)
	if err != nil {
		return nil, err
	}
	found := make([]workflow.Found, 0, len(repos))
	for _, repo := range repos {
		rest, err := json.Marshal(map[string]any{
			"number": 1, "title": "Update",
			"base": map[string]any{"repo": map[string]any{"full_name": repo.String()}},
			"head": map[string]any{"ref": "renovate/p1", "sha": sha},
		})
		if err != nil {
			return nil, err
		}
		event, err := workflow.ScannedPull[pullRequest](repo, 1, hostile.MustParseSha(sha), rest)
		if err != nil {
			return nil, err
		}
		found = append(found, event)
	}
	return found, nil
}

// watching is holding with a scan over the fleet a provider lists.
var watching = workflow.Code[pullRequest](workflow.Coded[pullRequest, pullRequest]{
	Name:        "watching",
	Intent:      "Hold, read, and attest every open pull request.",
	May:         []effect.Kind{effect.KindAttest},
	RunsPerHour: 1000,
	Agents:      []agent.Info{reviewer.Info()},
	Requires:    []providers.Requirement{providers.Require[fleetScanner]()},
	Key:         func(pr pullRequest) string { return pr.Repo.String() + ":" + pr.Branch.String() },
	Gather: func(ctx context.Context, cx workflow.Cx, pr pullRequest) (pullRequest, error) {
		_, err := workflow.Read[json.RawMessage](ctx, cx, reviewer, agent.Vars{}, pr.Title)
		return pr, err
	},
	Decide: func(pr pullRequest) []effect.Effect {
		return []effect.Effect{effect.Attest{Context: trust, State: effect.Success, Repo: pr.Repo, Sha: pr.Head}}
	},
	Scan: func(ctx context.Context, scanner *workflow.Scanner) ([]workflow.Found, error) {
		source, err := scanner.Provider(providers.Require[fleetScanner]())
		if err != nil {
			return nil, err
		}
		return source.(fleetScanner).Scan(ctx, scanner)
	},
})

// world is what one spec's almanac reaches: fakes for everything outside.
type world struct {
	mu      sync.Mutex
	forge   *almanactest.FakeForge
	tracker *almanactest.FakeTracker
	harness *almanactest.ScriptedHarness
	fleet   *fleet
	// setups counts how often the almanac set up its providers.
	setups int
}

func newWorld() *world {
	return &world{
		forge:   &almanactest.FakeForge{},
		tracker: &almanactest.FakeTracker{},
		harness: almanactest.Replying(okReply),
		fleet:   &fleet{repos: []hostile.Slug{hostile.MustParseSlug("owner/repo")}},
	}
}

// answering has every later setup use h as the agent's harness, as a restart with a new
// process would.
func (w *world) answering(h *almanactest.ScriptedHarness) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.harness = h
}

func (w *world) setup(context.Context, app.Setting) (*providers.Set, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.setups++
	set := providers.New()
	providers.Provide[effect.Forge](set, w.forge)
	providers.Provide[effect.Tracker](set, w.tracker)
	if w.fleet != nil {
		providers.Provide[fleetScanner](set, *w.fleet)
	}
	return set.UseHarness(w.harness), nil
}

// almanac is an almanac named almanac-spec over w, with the workflows given.
func (w *world) almanac(workflows ...workflow.Registered) app.Almanac {
	return app.Almanac{Name: "almanac-spec", Workflows: workflows, Setup: w.setup}
}

// ran is what one command line did.
type ran struct {
	code           int
	stdout, stderr string
}

// execute runs a command line in this process.
func execute(a app.Almanac, args ...string) ran {
	GinkgoHelper()
	var stdout, stderr bytes.Buffer
	code := a.Execute(context.Background(), args, &stdout, &stderr)
	return ran{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// decoded is one line of JSON output.
func decoded(text string) map[string]any {
	GinkgoHelper()
	var value map[string]any
	Expect(json.Unmarshal([]byte(text), &value)).To(Succeed(), text)
	return value
}

// The environment every almanac reads. Each spec starts with none of it set.
var environment = []string{
	"ALMANAC_MODE", "ALMANAC_READ_ONLY", "DATABASE_URL", "ALMANAC_LISTEN", "ALMANAC_MAX_CHAIN_DEPTH",
	"ALMANAC_RUNS_PER_HOUR_CAP", "ALMANAC_OVERALL_RUNS_PER_HOUR_CAP", "ALMANAC_CONCURRENCY",
	"ALMANAC_SCAN_INTERVAL_MINUTES", "NOTIFY_SOCKET",
}

// cleanEnvironment unsets what an almanac reads and points ALMANAC_CONFIG at a file that does
// not exist, for the length of the spec. Specs in one process run one at a time.
var _ = BeforeEach(func() {
	for _, name := range environment {
		unsetenv(name)
	}
	setenv("ALMANAC_CONFIG", filepath.Join(GinkgoT().TempDir(), "almanac.toml"))
})

func setenv(name, value string) {
	GinkgoHelper()
	restore(name)
	Expect(os.Setenv(name, value)).To(Succeed())
}

func unsetenv(name string) {
	GinkgoHelper()
	restore(name)
	Expect(os.Unsetenv(name)).To(Succeed())
}

func restore(name string) {
	was, set := os.LookupEnv(name)
	DeferCleanup(func() {
		if set {
			_ = os.Setenv(name, was)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

// configure writes almanac.toml for the spec, where ALMANAC_CONFIG points.
func configure(text string) {
	GinkgoHelper()
	Expect(os.WriteFile(os.Getenv("ALMANAC_CONFIG"), []byte(text), 0o600)).To(Succeed())
}

// fixture writes a file for simulate to read, and returns its path.
func fixture(body []byte) string {
	GinkgoHelper()
	path := filepath.Join(GinkgoT().TempDir(), "event.json")
	Expect(os.WriteFile(path, body, 0o600)).To(Succeed())
	return path
}

// freeAddress is a loopback address nothing listens on.
func freeAddress() string {
	GinkgoHelper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	address := listener.Addr().String()
	Expect(listener.Close()).To(Succeed())
	return address
}
