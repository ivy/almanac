// Package simforge is a local GitHub for tests: the subset of the REST API Almanac uses, a
// scenario API to script it, signed webhook delivery, and failure injection. It keeps no
// state past the process and checks only that a token is present.
//
// A spec builds a world, starts the server, and points a client at URL:
//
//	sim := simforge.Start()
//	DeferCleanup(sim.Close)
//	sim.AddRepo(simforge.PublicRepo("owner/repo"))
//	number := sim.AddPull("owner/repo", simforge.RenovatePull("renovate/x", "Update x"))
//	sim.DeliverTo(receiverURL, secret)
//	sim.PullRequest(ctx, "owner/repo", number, "opened")
//
// Repositories and pull requests must be added before anything refers to them. A scenario
// method given an unknown one panics, because that is a mistake in the spec.
package simforge

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"time"
)

// Renovate is Renovate's login in the operator's repositories, the default pull request author.
const Renovate = "ivy-s-renovate-bot[bot]"

// Forge is a running simulated GitHub. Its methods are safe for concurrent use.
type Forge struct {
	server *httptest.Server
	client *http.Client

	mu    sync.Mutex
	world *world
}

// Start serves an empty world on a free local port. Close stops it.
func Start() *Forge {
	f := &Forge{world: newWorld(), client: &http.Client{}}
	f.server = httptest.NewServer(f.handler())
	f.world.baseURL = f.server.URL
	return f
}

// Close stops the server.
func (f *Forge) Close() {
	f.server.Close()
}

// URL is the API's base URL, as GITHUB_API_URL would hold it.
func (f *Forge) URL() string {
	return f.server.URL
}

// AddRepo adds a repository, replacing any with the same full name.
func (f *Forge) AddRepo(r Repo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.RequiredChecks = slices.Clone(r.RequiredChecks)
	f.world.repos[r.FullName] = r
}

// AddPull adds a pull request and returns its number, which follows the repository's pull
// requests and issues. It sends no webhook: call PullRequest with "opened" for that.
func (f *Forge) AddPull(repo string, p Pull) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.world.addPull(repo, p)
}

// Pull is a copy of a pull request as it stands now.
func (f *Forge) Pull(repo string, number int) Pull {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.world.mustPull(repo, number).clone()
}

// AddLabel labels a pull request. It sends no webhook: call PullRequest with "labeled".
func (f *Forge) AddLabel(repo string, number int, label string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.world.mustPull(repo, number)
	p.Labels = append(p.Labels, label)
}

// MoveBranch moves a pull request's branch to sha, as someone else's push would.
func (f *Forge) MoveBranch(repo string, number int, sha, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.world.commits[sha] = message
	p := f.world.mustPull(repo, number)
	p.moveHead(sha, message)
	f.world.refs[branchRef{repo, p.HeadRef}] = sha
}

// AddSuite adds a check suite and returns its ID. It sends no webhook: call SuiteCompleted.
func (f *Forge) AddSuite(repo string, s Suite) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	s.ID = f.world.nextID()
	f.world.suites[repo] = append(f.world.suites[repo], s)
	return s.ID
}

// SetTarball sets what the tarball route serves for every commit of a repository.
func (f *Forge) SetTarball(repo string, gzippedTar []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.world.tarballs[repo] = slices.Clone(gzippedTar)
}

// MergeableAfter makes the next reads of any single pull request answer mergeable: null, as
// GitHub does while it computes mergeability.
func (f *Forge) MergeableAfter(reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.world.unknownReads = reads
}

// Failure is how a scripted request fails.
type Failure struct {
	status     int
	retryAfter time.Duration
}

// RateLimited answers 429 with Retry-After in whole seconds and no remaining requests.
func RateLimited(retryAfter time.Duration) Failure {
	return Failure{status: http.StatusTooManyRequests, retryAfter: retryAfter}
}

// ServerError answers with status and GitHub's error body. Any status works, 404 included.
func ServerError(status int) Failure {
	return Failure{status: status}
}

// FailRequests queues failures. Each request whose path contains pathContains takes the
// oldest matching failure instead of its answer, until the queue is empty.
func (f *Forge) FailRequests(pathContains string, failures ...Failure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, failure := range failures {
		f.world.failures = append(f.world.failures, queuedFailure{pathContains, failure})
	}
}

// Writes is every request received that was not a GET, oldest first.
func (f *Forge) Writes() []Write {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.world.writes)
}
