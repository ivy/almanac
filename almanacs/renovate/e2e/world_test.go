package e2e_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/ivy/almanac/almanacs/renovate/renovatetest"
	"github.com/ivy/almanac/harness/claude/stub"
	"github.com/ivy/almanac/internal/pgtest"
	"github.com/ivy/almanac/simforge"
)

const (
	// owner is the account whose repositories the almanac serves.
	owner = "fleet"
	// alerts is where a tripped budget is reported.
	alerts = owner + "/ops"
	secret = "a webhook secret"
	image  = "localhost/almanac-fixer:test"
	// wait is how long anything the almanac does may take: a repair runs the claude CLI in a
	// Podman sandbox, and scanned work runs at the log's next wake.
	wait = 180 * time.Second
	// trust is the status Almanac holds a pull request with.
	trust = "almanac/trust"
)

// world is one spec's whole deployment: a database, a forge whose webhooks reach a running
// broker, a stub model, a package registry and an advisory feed. Daemons and verbs run as
// processes against it.
type world struct {
	ctx        context.Context
	database   string
	pool       *pgxpool.Pool
	sim        *simforge.Forge
	model      *stub.Model
	registry   *registry
	advisories *advisories
	health     string
	// env is set for every almanac-renovate process, after the world's own settings.
	env []string
}

// newWorld starts a world whose model answers as agent does.
func newWorld(agent func(json.RawMessage) stub.Turn) *world {
	GinkgoHelper()
	ctx, cancel := context.WithCancel(context.Background())
	DeferCleanup(cancel)
	w := &world{ctx: ctx, database: pgtest.Database(), health: freeAddress()}
	var err error
	w.pool, err = pgxpool.New(ctx, w.database)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(w.pool.Close)

	w.sim = simforge.Start()
	DeferCleanup(w.sim.Close)
	w.model, err = stub.Answering(agent)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(w.model.Close)
	w.registry = newRegistry()
	w.advisories = newAdvisories()

	listen := freeAddress()
	cmd := exec.Command(broker)
	cmd.Env = []string{"DATABASE_URL=" + w.database, "ALMANAC_WEBHOOK_SECRET=" + secret, "ALMANAC_BROKER_LISTEN=" + listen}
	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { session.Kill().Wait(10 * time.Second) })
	Eventually(answers).WithArguments("http://"+listen+"/readyz").WithTimeout(wait).Should(Equal(http.StatusOK), "the broker")
	w.sim.DeliverTo("http://"+listen+"/webhooks/github", secret)
	return w
}

// command is almanac-renovate with args, configured for the world. The environment holds only
// what the almanac reads, as a systemd unit's would.
func (w *world) command(args ...string) *exec.Cmd {
	cmd := exec.Command(almanac, args...)
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"DATABASE_URL=" + w.database,
		"GITHUB_API_URL=" + w.sim.URL(),
		"GITHUB_TOKEN=t",
		"ALMANAC_OWNER=" + owner,
		"ALMANAC_RENOVATE_AUTHOR=" + simforge.Renovate,
		"ALMANAC_REGISTRY_URL=" + w.registry.URL(),
		"ALMANAC_ADVISORIES_URL=" + w.advisories.URL(),
		"ALMANAC_SANDBOX_IMAGE=" + image,
		"ALMANAC_LISTEN=" + w.health,
		"ALMANAC_ALERTS_REPO=" + alerts,
		"ALMANAC_CONFIG=/nonexistent/almanac.toml",
		"ANTHROPIC_API_KEY=stub",
		"ANTHROPIC_BASE_URL=" + w.model.URL,
	}, w.env...)
	return cmd
}

// daemon is a running `almanac-renovate run`.
type daemon struct {
	session *gexec.Session
}

// daemon starts the almanac live, and waits until it answers its health check. The spec
// crashes it when it ends, and stops the sandboxes it left.
func (w *world) daemon() *daemon {
	GinkgoHelper()
	cmd := w.command("run")
	cmd.Env = append(cmd.Env, "ALMANAC_MODE=live")
	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	d := &daemon{session: session}
	DeferCleanup(d.crash)
	Eventually(answers).WithArguments("http://"+w.health+"/healthz").WithTimeout(wait).Should(Equal(http.StatusOK), "the daemon")
	return d
}

// crash ends the daemon outright, as a crash would. A crashed daemon cannot end its sandboxes,
// so this stops every one it named.
func (d *daemon) crash() {
	d.session.Kill().Wait(10 * time.Second)
	named := fmt.Sprintf("name=^almanac-%d-", d.session.Command.Process.Pid)
	stop := exec.Command("podman", "stop", "--time", "0", "--filter", named)
	stop.Stdout, stop.Stderr = GinkgoWriter, GinkgoWriter
	_ = stop.Run()
}

// verb runs one verb to its end and returns what it printed. It fails the spec if the verb
// does.
func (w *world) verb(args ...string) []byte {
	GinkgoHelper()
	session, err := gexec.Start(w.command(args...), nil, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	Eventually(session).WithTimeout(wait).Should(gexec.Exit(0), "%v", args)
	return session.Out.Contents()
}

// report is what `report` says now.
func (w *world) report() fleetReport {
	GinkgoHelper()
	var found fleetReport
	Expect(json.Unmarshal(w.verb("--format", "json", "report"), &found)).To(Succeed())
	return found
}

// fleetReport is the part of the report's JSON the specs read.
type fleetReport struct {
	RepositoriesScanned int            `json:"repositories_scanned"`
	Totals              map[string]int `json:"totals"`
	Repositories        []struct {
		Repo  string `json:"repo"`
		Pulls []struct {
			Number int    `json:"number"`
			State  string `json:"state"`
		} `json:"pulls"`
	} `json:"repositories"`
}

func (r fleetReport) pulls() int {
	n := 0
	for _, repo := range r.Repositories {
		n += len(repo.Pulls)
	}
	return n
}

// count is the one number a query of the world's database returns.
func (w *world) count(query string, args ...any) int {
	GinkgoHelper()
	var n int
	Expect(w.pool.QueryRow(w.ctx, query, args...).Scan(&n)).To(Succeed())
	return n
}

// pushes are the commits Almanac pushed, as ref updates, to repositories whose name holds repo.
func (w *world) pushes(repo string) int {
	n := 0
	for _, write := range w.sim.Writes() {
		if write.Method == http.MethodPatch && strings.Contains(write.Path, "/git/refs/") && strings.Contains(write.Path, "/"+repo) {
			n++
		}
	}
	return n
}

// questions are the questions put to a person on a pull request.
func (w *world) questions() []string {
	asked := []string{}
	for _, write := range w.sim.Writes() {
		if write.Method == http.MethodPost && strings.HasSuffix(write.Path, "/comments") {
			body, _ := write.Body["body"].(string)
			asked = append(asked, body)
		}
	}
	return asked
}

// assigned is who each assignment named.
func (w *world) assigned() []any {
	named := []any{}
	for _, write := range w.sim.Writes() {
		if strings.HasSuffix(write.Path, "/assignees") {
			named = append(named, write.Body["assignees"])
		}
	}
	return named
}

// statuses are the states of the trust statuses posted on sha, in order.
func (w *world) statuses(repo, sha string) []string {
	states := []string{}
	for _, write := range w.sim.Writes() {
		if write.Method == http.MethodPost && write.Path == "/repos/"+repo+"/statuses/"+sha && write.Body["context"] == trust {
			state, _ := write.Body["state"].(string)
			states = append(states, state)
		}
	}
	return states
}

// issues are the titles of the issues opened in repo.
func (w *world) issues(repo string) []string {
	titles := []string{}
	for _, write := range w.sim.Writes() {
		if write.Method == http.MethodPost && write.Path == "/repos/"+repo+"/issues" {
			title, _ := write.Body["title"].(string)
			titles = append(titles, title)
		}
	}
	return titles
}

// ci finishes CI on a pull request's current head, and GitHub says so.
func (w *world) ci(repo string, number int, conclusion, branch string) {
	head := w.sim.Pull(repo, number).HeadSHA
	id := w.sim.AddSuite(repo, simforge.FinishedSuite(head, branch, conclusion, "error: tests failed"))
	w.sim.SuiteCompleted(w.ctx, repo, id)
}

// red is a pull request whose CI fails until a repair, if it is fixable.
type red struct {
	repo, branch string
	number       int
	fixable      bool
	seen         map[string]bool
}

// runCI runs CI for each failing pull request whose head is new, as a real pipeline would: the
// first head fails, and a repaired one passes if the failure was fixable.
func (w *world) runCI(reds []*red) {
	for _, r := range reds {
		head := w.sim.Pull(r.repo, r.number).HeadSHA
		if r.seen[head] {
			continue
		}
		r.seen[head] = true
		conclusion := "failure"
		if len(r.seen) > 1 && r.fixable {
			conclusion = "success"
		}
		w.ci(r.repo, r.number, conclusion, r.branch)
	}
}

// settle runs CI until done holds.
func (w *world) settle(reds []*red, done func() bool) {
	GinkgoHelper()
	Eventually(func() bool {
		w.runCI(reds)
		return done()
	}).WithTimeout(wait).WithPolling(200*time.Millisecond).Should(BeTrue(), "the fleet did not settle")
}

// fleet is 68 repositories and 28 Renovate pull requests, the shape of a real fleet, with
// synthetic names. Four are red and fixable; the rest are green.
func (w *world) fleet() []*red {
	for i := range 68 {
		w.sim.AddRepo(simforge.PublicRepo(fmt.Sprintf("%s/repo-%02d", owner, i)))
	}
	w.sim.AddRepo(simforge.PublicRepo(alerts))
	var reds []*red
	for i := range 28 {
		repo := fmt.Sprintf("%s/repo-%02d", owner, i%6)
		w.sim.SetTarball(repo, source())
		branch := fmt.Sprintf("renovate/pkg-%d", i)
		number := w.sim.AddPull(repo, update(branch, fmt.Sprintf("pkg-%d", i)))
		if i < 4 {
			reds = append(reds, &red{repo: repo, branch: branch, number: number, fixable: true, seen: map[string]bool{}})
			continue
		}
		w.sim.AddSuite(repo, simforge.FinishedSuite(w.sim.Pull(repo, number).HeadSHA, branch, "success", ""))
	}
	return reds
}

// update is a Renovate pull request on branch updating an npm package from 1.0.0 to 2.0.0.
func update(branch, pkg string) simforge.Pull {
	p := simforge.RenovatePull(branch, "Update")
	p.Body = table + row(pkg, "1.0.0", "2.0.0")
	return p
}

const table = "| Package | Change |\n|---|---|\n"

// row is a row of Renovate's table, updating an npm package.
func row(pkg, from, to string) string {
	return fmt.Sprintf("| [%s](https://x) | [`%s` → `%s`](https://renovatebot.com/diffs/npm/%s/%s/%s) |\n", pkg, from, to, pkg, from, to)
}

// source is a repository's source as GitHub's tarball endpoint serves it, holding one broken
// file.
func source() []byte {
	var out bytes.Buffer
	zipped := gzip.NewWriter(&out)
	archive := tar.NewWriter(zipped)
	text := "print('broken')\n"
	Expect(archive.WriteHeader(&tar.Header{Name: "repo-abc/src/main.py", Mode: 0o644, Size: int64(len(text)), Typeflag: tar.TypeReg})).To(Succeed())
	_, err := archive.Write([]byte(text))
	Expect(err).NotTo(HaveOccurred())
	Expect(archive.Close()).To(Succeed())
	Expect(zipped.Close()).To(Succeed())
	return out.Bytes()
}

// agent answers as the fixer and the reviewer would. The fixer writes one file and finishes,
// and the reviewer, offered no tools, has no concerns. It answers from each request alone, so
// sessions may interleave.
func agent(request json.RawMessage) stub.Turn {
	var r struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(request, &r)
	fixer := false
	for _, tool := range r.Tools {
		fixer = fixer || tool.Name == "mcp__sandbox__write_file"
	}
	if !fixer {
		return stub.Text(`{"verdict": "NoConcerns", "notes": "The diff only fixes the advisory."}`)
	}
	for _, m := range r.Messages {
		var blocks []struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(m.Content, &blocks)
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return stub.Text(`{"summary": "repaired"}`)
			}
		}
	}
	return stub.Tool("mcp__sandbox__write_file", map[string]string{"path": "src/main.py", "content": "print('fixed')\n"})
}

// registry is an npm registry. A release it was not given is dated by its package's name:
// `young-` means published an hour ago, and anything else a month ago.
type registry struct {
	server *httptest.Server
	mu     sync.Mutex
	// releases are archives by name, then version.
	releases map[string]map[string]release
}

type release struct {
	at      time.Time
	archive []byte
}

func newRegistry() *registry {
	r := &registry{releases: map[string]map[string]release{}}
	r.server = httptest.NewServer(http.HandlerFunc(r.serve))
	DeferCleanup(r.server.Close)
	return r
}

func (r *registry) URL() string { return r.server.URL }

// release publishes name at version, published hours ago, with files packed as npm packs them.
func (r *registry) release(name, version string, hours int, files map[string]string) {
	GinkgoHelper()
	archive, err := renovatetest.Archive(files)
	Expect(err).NotTo(HaveOccurred())
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.releases[name] == nil {
		r.releases[name] = map[string]release{}
	}
	r.releases[name][version] = release{at: time.Now().Add(-time.Duration(hours) * time.Hour), archive: archive}
}

// serve answers a package's document at /NAME and its archives at /-/NAME-VERSION.tgz.
func (r *registry) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if file, ok := strings.CutPrefix(req.URL.Path, "/-/"); ok {
		for name, versions := range r.releases {
			for version, rel := range versions {
				if file == name+"-"+version+".tgz" {
					_, _ = w.Write(rel.archive)
					return
				}
			}
		}
		http.NotFound(w, req)
		return
	}
	name := path.Base(req.URL.Path)
	times, versions := map[string]string{}, map[string]any{}
	if known, ok := r.releases[name]; ok {
		for version, rel := range known {
			times[version] = rel.at.UTC().Format(time.RFC3339)
			versions[version] = map[string]any{"dist": map[string]string{"tarball": r.server.URL + "/-/" + name + "-" + version + ".tgz"}}
		}
	} else {
		age := 30 * 24 * time.Hour
		if strings.HasPrefix(name, "young-") {
			age = time.Hour
		}
		times["2.0.0"] = time.Now().Add(-age).UTC().Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"time": times, "versions": versions})
}

// advisories is an advisory feed, as cve.now's API serves one.
type advisories struct {
	server  *httptest.Server
	mu      sync.Mutex
	entries map[string]map[string]any
}

func newAdvisories() *advisories {
	a := &advisories{entries: map[string]map[string]any{}}
	a.server = httptest.NewServer(http.HandlerFunc(a.serve))
	DeferCleanup(a.server.Close)
	return a
}

func (a *advisories) URL() string { return a.server.URL }

// fixes publishes an advisory affecting an npm package from introduced, fixed in fixed.
func (a *advisories) fixes(id, pkg, introduced, fixed string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries[strings.ToUpper(id)] = map[string]any{
		"id":      id,
		"summary": "A path traversal",
		"affected": []any{map[string]any{
			"ecosystem": "npm", "package": pkg,
			"ranges": []any{map[string]any{"introduced": introduced, "fixed": fixed}},
		}},
	}
}

func (a *advisories) serve(w http.ResponseWriter, req *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if id, ok := strings.CutPrefix(req.URL.Path, "/api/v1/entries/"); ok {
		entry, known := a.entries[strings.ToUpper(id)]
		if !known {
			http.NotFound(w, req)
			return
		}
		_ = json.NewEncoder(w).Encode(entry)
		return
	}
	if req.URL.Path != "/api/v1/search" {
		http.NotFound(w, req)
		return
	}
	pkg := req.URL.Query().Get("package")
	found := []any{}
	for _, entry := range a.entries {
		for _, affected := range entry["affected"].([]any) {
			if affected.(map[string]any)["package"] == pkg {
				found = append(found, map[string]any{"id": entry["id"]})
			}
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": found})
}

// answers is the status url answers with, or an error if nothing answers.
func answers(url string) (int, error) {
	response, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	return response.StatusCode, response.Body.Close()
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
