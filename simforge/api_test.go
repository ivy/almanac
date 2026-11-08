package simforge_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/simforge"
)

const branch = "renovate/lodash-4.x"

var _ = Describe("The API", func() {
	var (
		sim    *simforge.Forge
		number int
	)

	BeforeEach(func() {
		sim, number = scripted()
	})

	path := func(format string, args ...any) string {
		return sim.URL() + "/repos/" + repo + fmt.Sprintf(format, args...)
	}

	It("refuses a request without a token", func() {
		response, err := http.Get(path(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())
		Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
	})

	It("follows Link headers to every item, keeping the query", func() {
		for i := range 4 {
			sim.AddPull(repo, simforge.RenovatePull(fmt.Sprintf("renovate/p%d", i), "Update"))
		}
		closed := simforge.RenovatePull("renovate/closed", "Update")
		closed.State = "closed"
		sim.AddPull(repo, closed)

		var seen []any
		url := path("/pulls?state=all&per_page=2")
		for url != "" {
			response, body := call(http.MethodGet, url, nil)
			for _, p := range decoded(body).([]any) {
				seen = append(seen, at(p, "number"))
			}
			url = nextLink(response)
		}
		Expect(seen).To(HaveLen(6))
		for i, n := range seen {
			Expect(n).To(BeEquivalentTo(i + 1))
		}
	})

	It("links a middle page to next, last, prev and first", func() {
		for i := range 4 {
			sim.AddPull(repo, simforge.RenovatePull(fmt.Sprintf("renovate/p%d", i), "Update"))
		}
		response, _ := call(http.MethodGet, path("/pulls?per_page=2&page=2"), nil)
		for _, rel := range []string{"next", "last", "prev", "first"} {
			Expect(response.Header.Get("Link")).To(ContainSubstring(`rel="` + rel + `"`))
		}
		response, _ = call(http.MethodGet, path("/pulls"), nil)
		Expect(response.Header.Get("Link")).To(BeEmpty())
	})

	Describe("failure injection", func() {
		It("answers as GitHub does, in order, then clears", func() {
			sim.FailRequests("/pulls", simforge.RateLimited(7*time.Second), simforge.ServerError(502))
			first, body := call(http.MethodGet, path("/pulls"), nil)
			Expect(first.StatusCode).To(Equal(http.StatusTooManyRequests))
			Expect(first.Header.Get("Retry-After")).To(Equal("7"))
			Expect(first.Header.Get("X-RateLimit-Remaining")).To(Equal("0"))
			Expect(at(decoded(body), "message")).To(Equal("API rate limit exceeded"))
			second, _ := call(http.MethodGet, path("/pulls"), nil)
			Expect(second.StatusCode).To(Equal(http.StatusBadGateway))
			third, _ := call(http.MethodGet, path("/pulls"), nil)
			Expect(third.StatusCode).To(Equal(http.StatusOK))
		})

		It("leaves requests that do not match alone", func() {
			sim.FailRequests("/pulls/", simforge.ServerError(404))
			response, _ := call(http.MethodGet, path(""), nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			response, _ = call(http.MethodGet, path("/pulls/%d", number), nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			response, _ = call(http.MethodGet, path("/pulls/%d", number), nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
		})

		It("does not record a write that failed", func() {
			sim.FailRequests("/statuses/", simforge.ServerError(500))
			call(http.MethodPost, path("/statuses/abc"), map[string]any{"state": "success"})
			Expect(sim.Writes()).To(BeEmpty())
		})
	})

	Describe("writes", func() {
		It("records every write, and a push moves the pull request", func() {
			before := sim.Pull(repo, number).HeadSHA
			call(http.MethodPost, path("/statuses/%s", before), map[string]any{"state": "pending", "context": "almanac/trust"})
			moved := strings.Repeat("a", 40)
			response, body := call(http.MethodPatch, path("/git/refs/heads/%s", branch), map[string]any{"sha": moved})
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(at(decoded(body), "ref")).To(Equal("refs/heads/" + branch))
			Expect(at(decoded(body), "object", "sha")).To(Equal(moved))

			writes := sim.Writes()
			Expect(writes).To(HaveLen(2))
			Expect(writes[0].Method).To(Equal("POST"))
			Expect(writes[0].Path).To(Equal("/repos/" + repo + "/statuses/" + before))
			Expect(writes[0].Body).To(HaveKeyWithValue("context", "almanac/trust"))
			Expect(writes[1].Method).To(Equal("PATCH"))
			Expect(sim.Pull(repo, number).HeadSHA).To(Equal(moved))
		})

		It("refuses to move a branch that does not exist", func() {
			response, body := call(http.MethodPatch, path("/git/refs/heads/nowhere"), map[string]any{"sha": "a"})
			Expect(response.StatusCode).To(Equal(http.StatusUnprocessableEntity))
			Expect(at(decoded(body), "message")).To(Equal("Reference does not exist"))
		})

		It("builds a commit from a blob and a tree, and pushing it adds its message", func() {
			response, body := call(http.MethodPost, path("/git/blobs"), map[string]any{"content": "x"})
			Expect(response.StatusCode).To(Equal(http.StatusCreated))
			Expect(at(decoded(body), "sha")).To(HaveLen(40))
			response, body = call(http.MethodPost, path("/git/trees"), map[string]any{"tree": []any{}})
			Expect(response.StatusCode).To(Equal(http.StatusCreated))
			tree := at(decoded(body), "sha")

			response, body = call(http.MethodPost, path("/git/commits"), map[string]any{"message": "fix: build", "tree": tree})
			Expect(response.StatusCode).To(Equal(http.StatusCreated))
			commit := decoded(body)
			Expect(at(commit, "message")).To(Equal("fix: build"))
			Expect(at(commit, "tree", "sha")).To(Equal(tree))
			Expect(at(commit, "author", "name")).To(Equal("almanac"))
			sha := at(commit, "sha").(string)

			read := getJSON(sim, "/repos/"+repo+"/git/commits/"+sha)
			Expect(at(read, "message")).To(Equal("fix: build"))
			Expect(at(read, "verification", "verified")).To(BeFalse())

			call(http.MethodPatch, path("/git/refs/heads/%s", branch), map[string]any{"sha": sha})
			Expect(sim.Pull(repo, number).Commits).To(Equal([]string{"Update", "fix: build"}))
		})

		It("reads a commit it does not know as missing", func() {
			response, _ := call(http.MethodGet, path("/git/commits/%s", strings.Repeat("f", 40)), nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("keeps one status per context and combines them", func() {
			sha := sim.Pull(repo, number).HeadSHA
			combined := func() any { return getJSON(sim, "/repos/"+repo+"/commits/"+sha+"/status") }
			Expect(at(combined(), "state")).To(Equal("pending"))
			Expect(at(combined(), "total_count")).To(BeEquivalentTo(0))

			response, body := call(http.MethodPost, path("/statuses/%s", sha), map[string]any{"state": "success", "context": "a", "description": "fine"})
			Expect(response.StatusCode).To(Equal(http.StatusCreated))
			Expect(at(decoded(body), "description")).To(Equal("fine"))
			Expect(at(combined(), "state")).To(Equal("success"))

			call(http.MethodPost, path("/statuses/%s", sha), map[string]any{"state": "pending", "context": "b"})
			Expect(at(combined(), "state")).To(Equal("pending"))
			call(http.MethodPost, path("/statuses/%s", sha), map[string]any{"state": "error", "context": "b"})
			Expect(at(combined(), "state")).To(Equal("failure"))
			Expect(at(combined(), "total_count")).To(BeEquivalentTo(2))
			Expect(at(combined(), "repository", "full_name")).To(Equal(repo))
		})

		It("creates issues numbered after pull requests, and lists them by label", func() {
			response, body := call(http.MethodPost, path("/issues"), map[string]any{"title": "t", "body": "b", "labels": []string{"almanac"}})
			Expect(response.StatusCode).To(Equal(http.StatusCreated))
			Expect(at(decoded(body), "number")).To(BeEquivalentTo(2))
			call(http.MethodPost, path("/issues"), map[string]any{"title": "other"})
			Expect(sim.AddPull(repo, simforge.RenovatePull("renovate/next", "Update"))).To(Equal(4))

			Expect(getJSON(sim, "/repos/"+repo+"/issues")).To(HaveLen(2))
			labelled := getJSON(sim, "/repos/"+repo+"/issues?labels=almanac")
			Expect(labelled).To(HaveLen(1))
			Expect(at(labelled, 0, "title")).To(Equal("t"))
			Expect(at(labelled, 0, "labels", 0, "name")).To(Equal("almanac"))
			Expect(getJSON(sim, "/repos/"+repo+"/issues?labels=almanac,other")).To(BeEmpty())
		})

		It("keeps comments on a thread", func() {
			response, body := call(http.MethodPost, path("/issues/%d/comments", number), map[string]any{"body": "first"})
			Expect(response.StatusCode).To(Equal(http.StatusCreated))
			Expect(at(decoded(body), "body")).To(Equal("first"))
			call(http.MethodPost, path("/issues/%d/comments", number), map[string]any{"body": "second"})
			thread := getJSON(sim, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number))
			Expect(at(thread, 0, "body")).To(Equal("first"))
			Expect(at(thread, 1, "body")).To(Equal("second"))
			Expect(getJSON(sim, "/repos/"+repo+"/issues/99/comments")).To(BeEmpty())
		})

		It("assigns people", func() {
			response, body := call(http.MethodPost, path("/issues/%d/assignees", number), map[string]any{"assignees": []string{"a"}})
			Expect(response.StatusCode).To(Equal(http.StatusCreated))
			Expect(at(decoded(body), "number")).To(BeEquivalentTo(number))
			Expect(sim.Writes()[0].Body).To(HaveKeyWithValue("assignees", []any{"a"}))
		})

		It("mints an installation token", func() {
			response, body := call(http.MethodPost, sim.URL()+"/app/installations/7/access_tokens", nil)
			Expect(response.StatusCode).To(Equal(http.StatusCreated))
			Expect(at(decoded(body), "token")).To(Equal("ghs_simulated"))
		})
	})

	Describe("repositories", func() {
		BeforeEach(func() {
			sim.AddRepo(simforge.Repo{FullName: "owner/private", Private: true})
			sim.AddRepo(simforge.Repo{FullName: "owner/old", Archived: true})
			sim.AddRepo(simforge.PublicRepo("someone/else"))
		})

		names := func(value any) []any {
			var out []any
			for _, r := range value.([]any) {
				out = append(out, at(r, "full_name"))
			}
			return out
		}

		It("lists an owner's repositories, archived ones included", func() {
			Expect(names(getJSON(sim, "/users/owner/repos"))).To(Equal([]any{"owner/old", "owner/private", "owner/repo"}))
		})

		It("lists every repository for the user", func() {
			Expect(getJSON(sim, "/user/repos")).To(HaveLen(4))
		})

		It("lists an installation's repositories with a total, past the first page", func() {
			for i := range 130 {
				sim.AddRepo(simforge.PublicRepo(fmt.Sprintf("owner/repo-%03d", i)))
			}
			var all []any
			url := sim.URL() + "/installation/repositories?per_page=100"
			for url != "" {
				response, body := call(http.MethodGet, url, nil)
				page := decoded(body)
				Expect(at(page, "total_count")).To(BeEquivalentTo(134))
				all = append(all, at(page, "repositories").([]any)...)
				url = nextLink(response)
			}
			Expect(all).To(HaveLen(134))
		})

		It("reads one repository, its visibility and its archived flag", func() {
			private := getJSON(sim, "/repos/owner/private")
			Expect(at(private, "private")).To(BeTrue())
			Expect(at(private, "visibility")).To(Equal("private"))
			Expect(at(getJSON(sim, "/repos/owner/old"), "archived")).To(BeTrue())
			Expect(at(getJSON(sim, "/repos/"+repo), "owner", "login")).To(Equal("owner"))
			response, _ := call(http.MethodGet, sim.URL()+"/repos/owner/missing", nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("serves renovate.json where a repository has one", func() {
			sim.AddRepo(simforge.Repo{FullName: "owner/configured", Renovate: `{"automerge": false}`})
			file := getJSON(sim, "/repos/owner/configured/contents/renovate.json")
			Expect(at(file, "encoding")).To(Equal("base64"))
			content, err := base64.StdEncoding.DecodeString(at(file, "content").(string))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(Equal(`{"automerge": false}`))

			response, _ := call(http.MethodGet, path("/contents/renovate.json"), nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			response, _ = call(http.MethodGet, sim.URL()+"/repos/owner/configured/contents/other/file.json", nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("lists a repository's workflows where it has them", func() {
			sim.AddRepo(simforge.Repo{FullName: "owner/tested", Workflows: true})
			listing := getJSON(sim, "/repos/owner/tested/contents/.github/workflows")
			Expect(listing).To(HaveLen(1))
			Expect(at(listing, 0, "path")).To(Equal(".github/workflows/ci.yml"))

			response, _ := call(http.MethodGet, path("/contents/.github/workflows"), nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("reports branch protection from required checks", func() {
			sim.AddRepo(simforge.Repo{FullName: "owner/guarded", RequiredChecks: []string{"ci"}})
			guarded := getJSON(sim, "/repos/owner/guarded/branches/main")
			Expect(at(guarded, "protected")).To(BeTrue())
			Expect(at(guarded, "protection", "required_status_checks", "contexts")).To(Equal([]any{"ci"}))
			open := getJSON(sim, "/repos/"+repo+"/branches/main")
			Expect(at(open, "protected")).To(BeFalse())
			Expect(at(open, "protection", "required_status_checks", "contexts")).To(Equal([]any{}))
		})

		It("serves a repository's tarball for any commit", func() {
			response, _ := call(http.MethodGet, path("/tarball/abc"), nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			sim.SetTarball(repo, []byte{0x1f, 0x8b, 0})
			response, body := call(http.MethodGet, path("/tarball/abc"), nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(body).To(Equal([]byte{0x1f, 0x8b, 0}))
		})
	})

	Describe("pull requests", func() {
		It("lists open ones by default, and filters by state and head", func() {
			closed := simforge.RenovatePull("renovate/closed", "Update")
			closed.State = "closed"
			sim.AddPull(repo, closed)
			Expect(getJSON(sim, "/repos/"+repo+"/pulls")).To(HaveLen(1))
			Expect(getJSON(sim, "/repos/"+repo+"/pulls?state=closed")).To(HaveLen(1))
			Expect(getJSON(sim, "/repos/"+repo+"/pulls?state=all")).To(HaveLen(2))
			byHead := getJSON(sim, "/repos/"+repo+"/pulls?state=all&head=owner:renovate/closed")
			Expect(byHead).To(HaveLen(1))
			Expect(at(byHead, 0, "head", "ref")).To(Equal("renovate/closed"))
			Expect(byHead).NotTo(ContainElement(HaveKey("mergeable")))
			response, _ := call(http.MethodGet, sim.URL()+"/repos/owner/missing/pulls", nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("reads one with what GitHub computes for it", func() {
			p := getJSON(sim, fmt.Sprintf("/repos/%s/pulls/%d", repo, number))
			Expect(at(p, "mergeable")).To(BeTrue())
			Expect(at(p, "mergeable_state")).To(Equal("clean"))
			Expect(at(p, "head", "sha")).To(Equal(sim.Pull(repo, number).HeadSHA))
			Expect(at(p, "head", "label")).To(Equal("owner:" + branch))
			Expect(at(p, "base", "ref")).To(Equal("main"))
			Expect(at(p, "user", "login")).To(Equal(simforge.Renovate))
			Expect(at(p, "body")).To(Equal("This PR contains the following updates:"))
			response, _ := call(http.MethodGet, path("/pulls/99"), nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("reads a dirty one as not mergeable, and one with no body as null", func() {
			dirty := simforge.RenovatePull("renovate/dirty", "Update")
			dirty.MergeableState = "dirty"
			dirty.Body = ""
			n := sim.AddPull(repo, dirty)
			p := getJSON(sim, fmt.Sprintf("/repos/%s/pulls/%d", repo, n))
			Expect(at(p, "mergeable")).To(BeFalse())
			Expect(at(p, "body")).To(BeNil())
		})

		It("reads mergeable as null until GitHub has computed it", func() {
			sim.MergeableAfter(2)
			read := func() any { return at(getJSON(sim, fmt.Sprintf("/repos/%s/pulls/%d", repo, number)), "mergeable") }
			Expect(read()).To(BeNil())
			Expect(read()).To(BeNil())
			Expect(read()).To(BeTrue())
		})

		It("names a fork as the head repository", func() {
			fork := simforge.RenovatePull("renovate/fork", "From a fork")
			fork.HeadRepo = "stranger/repo"
			n := sim.AddPull(repo, fork)
			p := getJSON(sim, fmt.Sprintf("/repos/%s/pulls/%d", repo, n))
			Expect(at(p, "head", "repo", "full_name")).To(Equal("stranger/repo"))
			Expect(at(p, "base", "repo", "full_name")).To(Equal(repo))
		})

		It("lists commits, oldest first", func() {
			sim.MoveBranch(repo, number, strings.Repeat("c", 40), "someone else's commit")
			commits := getJSON(sim, fmt.Sprintf("/repos/%s/pulls/%d/commits", repo, number))
			Expect(at(commits, 0, "commit", "message")).To(Equal("Update"))
			Expect(at(commits, 1, "commit", "message")).To(Equal("someone else's commit"))
			Expect(sim.Pull(repo, number).HeadSHA).To(Equal(strings.Repeat("c", 40)))
			response, _ := call(http.MethodGet, path("/pulls/99/commits"), nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("lists changed files, with a patch where there is one", func() {
			p := simforge.RenovatePull("renovate/files", "Update")
			p.Files = []string{"package.json", "big.lock"}
			p.Diffs = map[string]string{"package.json": "@@ -1 +1 @@\n-1\n+2"}
			n := sim.AddPull(repo, p)
			files := getJSON(sim, fmt.Sprintf("/repos/%s/pulls/%d/files", repo, n))
			Expect(files).To(Equal([]any{
				map[string]any{"filename": "package.json", "patch": "@@ -1 +1 @@\n-1\n+2"},
				map[string]any{"filename": "big.lock"},
			}))
			response, _ := call(http.MethodGet, path("/pulls/99/files"), nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("hands out copies, so a spec cannot change the world by accident", func() {
			p := sim.Pull(repo, number)
			p.Commits[0] = "changed"
			p.Labels = append(p.Labels, "changed")
			Expect(sim.Pull(repo, number).Commits).To(Equal([]string{"Update"}))
			Expect(sim.Pull(repo, number).Labels).To(BeEmpty())
		})
	})

	Describe("checks", func() {
		It("makes failure logs readable through the check run", func() {
			sha := sim.Pull(repo, number).HeadSHA
			sim.AddSuite(repo, simforge.FinishedSuite(sha, branch, "failure", "error: boom"))
			runs := getJSON(sim, "/repos/"+repo+"/commits/"+sha+"/check-runs")
			Expect(at(runs, "total_count")).To(BeEquivalentTo(1))
			Expect(at(runs, "check_runs", 0, "conclusion")).To(Equal("failure"))
			id := at(runs, "check_runs", 0, "id")
			response, logs := call(http.MethodGet, path("/actions/jobs/%v/logs", id), nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(string(logs)).To(Equal("error: boom"))
			response, _ = call(http.MethodGet, path("/actions/jobs/999/logs"), nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("lists the suites at a commit, a ghost suite included", func() {
			sha := sim.Pull(repo, number).HeadSHA
			sim.AddSuite(repo, simforge.GhostSuite(sha, branch))
			sim.AddSuite(repo, simforge.FinishedSuite(sha, branch, "success", ""))
			sim.AddSuite(repo, simforge.FinishedSuite("elsewhere", branch, "failure", ""))
			suites := getJSON(sim, "/repos/"+repo+"/commits/"+sha+"/check-suites")
			Expect(at(suites, "total_count")).To(BeEquivalentTo(2))
			Expect(at(suites, "check_suites", 0, "status")).To(Equal("queued"))
			Expect(at(suites, "check_suites", 0, "conclusion")).To(BeNil())
			Expect(at(suites, "check_suites", 0, "latest_check_runs_count")).To(BeEquivalentTo(0))
			Expect(at(suites, "check_suites", 1, "conclusion")).To(Equal("success"))
			Expect(at(suites, "check_suites", 1, "latest_check_runs_count")).To(BeEquivalentTo(1))
		})

		It("answers a commit with no checks with empty lists", func() {
			Expect(getJSON(sim, "/repos/"+repo+"/commits/none/check-suites")).To(HaveKeyWithValue("check_suites", []any{}))
			Expect(getJSON(sim, "/repos/"+repo+"/commits/none/check-runs")).To(HaveKeyWithValue("check_runs", []any{}))
		})
	})
})
