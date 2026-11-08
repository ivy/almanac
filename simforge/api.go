package simforge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const (
	defaultPerPage = 30
	maxBody        = 16 << 20
)

// handler routes the API. Paths and shapes follow specs/github/api.github.com.json.
func (f *Forge) handler() http.Handler {
	mux := http.NewServeMux()
	routes := map[string]http.HandlerFunc{
		"GET /users/{owner}/repos":                               f.userRepos,
		"GET /user/repos":                                        f.ownRepos,
		"GET /installation/repositories":                         f.installationRepos,
		"POST /app/installations/{id}/access_tokens":             installationToken,
		"GET /repos/{owner}/{repo}":                              f.repository,
		"GET /repos/{owner}/{repo}/pulls":                        f.pulls,
		"GET /repos/{owner}/{repo}/pulls/{number}":               f.pull,
		"GET /repos/{owner}/{repo}/pulls/{number}/commits":       f.pullCommits,
		"GET /repos/{owner}/{repo}/pulls/{number}/files":         f.pullFiles,
		"GET /repos/{owner}/{repo}/tarball/{sha}":                f.tarball,
		"GET /repos/{owner}/{repo}/branches/{branch}":            f.branch,
		"GET /repos/{owner}/{repo}/contents/{path...}":           f.contents,
		"GET /repos/{owner}/{repo}/commits/{sha}/check-suites":   f.checkSuites,
		"GET /repos/{owner}/{repo}/commits/{sha}/check-runs":     f.checkRuns,
		"GET /repos/{owner}/{repo}/commits/{sha}/status":         f.combinedStatus,
		"GET /repos/{owner}/{repo}/actions/jobs/{id}/logs":       f.jobLogs,
		"POST /repos/{owner}/{repo}/statuses/{sha}":              f.postStatus,
		"POST /repos/{owner}/{repo}/git/blobs":                   f.createBlob,
		"POST /repos/{owner}/{repo}/git/trees":                   f.createTree,
		"POST /repos/{owner}/{repo}/git/commits":                 f.createCommit,
		"GET /repos/{owner}/{repo}/git/commits/{sha}":            f.commit,
		"PATCH /repos/{owner}/{repo}/git/refs/heads/{branch...}": f.updateRef,
		"GET /repos/{owner}/{repo}/issues":                       f.issues,
		"POST /repos/{owner}/{repo}/issues":                      f.createIssue,
		"GET /repos/{owner}/{repo}/issues/{number}/comments":     f.comments,
		"POST /repos/{owner}/{repo}/issues/{number}/comments":    f.createComment,
		"POST /repos/{owner}/{repo}/issues/{number}/assignees":   f.addAssignees,
		"GET /app/hook/deliveries":                               f.deliveries,
		"POST /app/hook/deliveries/{id}/attempts":                f.redeliver,
	}
	for pattern, handle := range routes {
		mux.HandleFunc(pattern, handle)
	}
	return f.gatekeeper(mux)
}

// gatekeeper refuses a request without a token, answers with a scripted failure if one
// matches, and records every write.
func (f *Forge) gatekeeper(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			reply(w, http.StatusUnauthorized, object{"message": "Requires authentication"})
			return
		}
		path := r.URL.EscapedPath()
		if failure, ok := f.takeFailure(path); ok {
			failed(w, failure)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			reply(w, http.StatusBadRequest, object{"message": "Problems parsing JSON"})
			return
		}
		if r.Method != http.MethodGet {
			var sent map[string]any
			if json.Unmarshal(body, &sent) != nil {
				sent = nil
			}
			f.mu.Lock()
			f.world.writes = append(f.world.writes, Write{Method: r.Method, Path: path, Body: sent})
			f.mu.Unlock()
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

func (f *Forge) takeFailure(path string) (Failure, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	at := slices.IndexFunc(f.world.failures, func(q queuedFailure) bool {
		return strings.Contains(path, q.pathContains)
	})
	if at < 0 {
		return Failure{}, false
	}
	failure := f.world.failures[at].failure
	f.world.failures = slices.Delete(f.world.failures, at, at+1)
	return failure, true
}

func failed(w http.ResponseWriter, failure Failure) {
	if failure.status != http.StatusTooManyRequests {
		reply(w, failure.status, object{"message": "Server Error"})
		return
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(failure.retryAfter.Seconds())))
	w.Header().Set("X-RateLimit-Remaining", "0")
	reply(w, http.StatusTooManyRequests, object{"message": "API rate limit exceeded"})
}

// reply writes value as JSON, unescaped as GitHub writes it.
func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(encode(value))
}

func encode(value any) []byte {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		panic(fmt.Sprintf("simforge: rendering JSON: %v", err))
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

func missing(w http.ResponseWriter) {
	reply(w, http.StatusNotFound, object{"message": "Not Found"})
}

// decode reads a JSON object body. Fields that are absent or of another type read as zero.
func decode(r *http.Request) map[string]any {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body
}

func str(body map[string]any, key string) string {
	s, _ := body[key].(string)
	return s
}

func strs(body map[string]any, key string) []string {
	items, _ := body[key].([]any)
	var out []string
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func fullName(r *http.Request) string {
	return r.PathValue("owner") + "/" + r.PathValue("repo")
}

// number reads the issue or pull request number in the path. One that is not a number finds
// nothing.
func number(r *http.Request) int {
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		return -1
	}
	return n
}

func positive(query url.Values, key string, otherwise int) int {
	n, err := strconv.Atoi(query.Get(key))
	if err != nil || n < 1 {
		return otherwise
	}
	return n
}

// page is one page of items, by per_page and the page number held in key.
func page(query url.Values, key string, items []any) (shown []any, number, perPage, last int) {
	perPage = positive(query, "per_page", defaultPerPage)
	number = positive(query, key, 1)
	last = max(1, (len(items)+perPage-1)/perPage)
	start := min(len(items), (number-1)*perPage)
	end := min(len(items), start+perPage)
	return append([]any{}, items[start:end]...), number, perPage, last
}

// paged answers one page of items with a Link header as GitHub writes it. key is "page", or
// "cursor" for endpoints that page by one. Other query parameters carry into the links.
func (f *Forge) paged(w http.ResponseWriter, r *http.Request, key string, items []any) {
	f.link(w, r, key, items)
	shown, _, _, _ := page(r.URL.Query(), key, items)
	reply(w, http.StatusOK, shown)
}

func (f *Forge) link(w http.ResponseWriter, r *http.Request, key string, items []any) {
	query := r.URL.Query()
	_, n, perPage, last := page(query, key, items)
	to := func(n int) string {
		query.Set("per_page", strconv.Itoa(perPage))
		query.Set(key, strconv.Itoa(n))
		return fmt.Sprintf("<%s%s?%s>", f.world.baseURL, r.URL.EscapedPath(), query.Encode())
	}
	var links []string
	if n < last {
		links = append(links, to(n+1)+`; rel="next"`, to(last)+`; rel="last"`)
	}
	if n > 1 {
		links = append(links, to(n-1)+`; rel="prev"`, to(1)+`; rel="first"`)
	}
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
}

func (f *Forge) userRepos(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owner := r.PathValue("owner")
	var items []any
	for _, repo := range f.world.sortedRepos() {
		if repo.Owner() == owner {
			items = append(items, f.world.repoJSON(repo))
		}
	}
	f.paged(w, r, "page", items)
}

func (f *Forge) ownRepos(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paged(w, r, "page", list(f.world.sortedRepos(), func(repo Repo) any { return f.world.repoJSON(repo) }))
}

func (f *Forge) installationRepos(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := list(f.world.sortedRepos(), func(repo Repo) any { return f.world.repoJSON(repo) })
	f.link(w, r, "page", all)
	shown, _, _, _ := page(r.URL.Query(), "page", all)
	reply(w, http.StatusOK, object{"total_count": len(all), "repositories": shown})
}

func installationToken(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusCreated, object{"token": "ghs_simulated", "expires_at": "2099-01-01T00:00:00Z"})
}

func (f *Forge) repository(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	repo, ok := f.world.repos[fullName(r)]
	if !ok {
		missing(w)
		return
	}
	reply(w, http.StatusOK, f.world.repoJSON(repo))
}

func (f *Forge) pulls(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := fullName(r)
	repo, ok := f.world.repos[name]
	if !ok {
		missing(w)
		return
	}
	query := r.URL.Query()
	state := query.Get("state")
	if state == "" {
		state = "open"
	}
	_, headRef, byHead := strings.Cut(query.Get("head"), ":")
	var items []any
	for i := range f.world.pulls[name] {
		p := &f.world.pulls[name][i]
		if (state == "all" || p.State == state) && (!byHead || p.HeadRef == headRef) {
			items = append(items, f.world.pullSimpleJSON(repo, p))
		}
	}
	f.paged(w, r, "page", items)
}

func (f *Forge) pull(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := fullName(r)
	repo, ok := f.world.repos[name]
	p := f.world.pull(name, number(r))
	if ok && p != nil {
		reply(w, http.StatusOK, f.world.pullJSON(repo, p))
	} else {
		missing(w)
	}
	f.world.unknownReads = max(0, f.world.unknownReads-1)
}

func (f *Forge) pullCommits(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.world.pull(fullName(r), number(r))
	if p == nil {
		missing(w)
		return
	}
	who := object{"name": "a", "email": "a@b", "date": timestamp}
	f.paged(w, r, "page", list(p.Commits, func(message string) any {
		return object{
			"sha": strings.Repeat("0", 40), "node_id": "C_1", "url": "/c", "html_url": "/c",
			"comments_url": "/c", "parents": []any{},
			"commit": object{
				"url": "/c", "author": who, "committer": who, "message": message,
				"comment_count": 0, "tree": object{"sha": "t", "url": "/t"},
			},
		}
	}))
}

func (f *Forge) pullFiles(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.world.pull(fullName(r), number(r))
	if p == nil {
		missing(w)
		return
	}
	f.paged(w, r, "page", list(p.Files, func(file string) any {
		item := object{"filename": file}
		if diff, ok := p.Diffs[file]; ok {
			item["patch"] = diff
		}
		return item
	}))
}

func (f *Forge) tarball(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tarball, ok := f.world.tarballs[fullName(r)]
	if !ok {
		missing(w)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(tarball)
}

func (f *Forge) branch(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	repo, ok := f.world.repos[fullName(r)]
	if !ok {
		missing(w)
		return
	}
	contexts := list(repo.RequiredChecks, func(c string) any { return c })
	reply(w, http.StatusOK, object{
		"name":       r.PathValue("branch"),
		"protected":  len(contexts) > 0,
		"protection": object{"required_status_checks": object{"contexts": contexts}},
	})
}

// workflowsDirectory is where a repository keeps its GitHub Actions workflows.
const workflowsDirectory = ".github/workflows"

func (f *Forge) contents(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.PathValue("path")
	repo, ok := f.world.repos[fullName(r)]
	if ok && path == workflowsDirectory && repo.Workflows {
		file := workflowsDirectory + "/ci.yml"
		reply(w, http.StatusOK, []any{object{
			"type": "file", "name": "ci.yml", "path": file, "size": 0, "sha": "0", "url": "/f",
			"git_url": nil, "html_url": nil, "download_url": nil,
			"_links": object{"self": "/f", "git": nil, "html": nil},
		}})
		return
	}
	if !ok || path != "renovate.json" || repo.Renovate == "" {
		missing(w)
		return
	}
	reply(w, http.StatusOK, object{
		"type": "file", "encoding": "base64", "name": path, "path": path, "size": len(repo.Renovate),
		"sha": "0", "url": "/f", "git_url": nil, "html_url": nil, "download_url": nil,
		"content": base64.StdEncoding.EncodeToString([]byte(repo.Renovate)),
		"_links":  object{"self": "/f", "git": nil, "html": nil},
	})
}

func (f *Forge) suitesAt(repo, sha string) []Suite {
	var at []Suite
	for _, s := range f.world.suites[repo] {
		if s.SHA == sha {
			at = append(at, s)
		}
	}
	return at
}

func (f *Forge) checkSuites(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	suites := list(f.suitesAt(fullName(r), r.PathValue("sha")), func(s Suite) any { return f.world.suiteJSON(s) })
	reply(w, http.StatusOK, object{"total_count": len(suites), "check_suites": suites})
}

func (f *Forge) checkRuns(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := fullName(r)
	runs := list(f.suitesAt(name, r.PathValue("sha")), func(s Suite) any { return f.world.checkRunJSON(name, s) })
	reply(w, http.StatusOK, object{"total_count": len(runs), "check_runs": runs})
}

func (f *Forge) combinedStatus(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name, sha := fullName(r), r.PathValue("sha")
	repo, ok := f.world.repos[name]
	if !ok {
		missing(w)
		return
	}
	var statuses []status
	for _, s := range f.world.statuses[name] {
		if s.sha == sha {
			statuses = append(statuses, s)
		}
	}
	reply(w, http.StatusOK, object{
		"state": combinedState(statuses), "sha": sha, "total_count": len(statuses),
		"statuses":   list(statuses, func(s status) any { return f.world.statusJSON(s) }),
		"repository": f.world.repoJSON(repo),
		"commit_url": f.world.url(fmt.Sprintf("/repos/%s/commits/%s", name, sha)),
		"url":        f.world.url(fmt.Sprintf("/repos/%s/commits/%s/status", name, sha)),
	})
}

// combinedState is failure if any status failed or errored, success if there are statuses
// and all succeeded, and pending otherwise.
func combinedState(statuses []status) string {
	if slices.ContainsFunc(statuses, func(s status) bool { return s.state == "failure" || s.state == "error" }) {
		return "failure"
	}
	if len(statuses) > 0 && !slices.ContainsFunc(statuses, func(s status) bool { return s.state != "success" }) {
		return "success"
	}
	return "pending"
}

func (f *Forge) jobLogs(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	at := slices.IndexFunc(f.world.suites[fullName(r)], func(s Suite) bool { return s.ID == id })
	if err != nil || at < 0 {
		missing(w)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, f.world.suites[fullName(r)][at].Logs)
}

func (f *Forge) postStatus(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	name := fullName(r)
	s := status{
		sha:         r.PathValue("sha"),
		context:     str(body, "context"),
		state:       str(body, "state"),
		description: str(body, "description"),
	}
	if s.context == "" {
		s.context = "default"
	}
	f.world.statuses[name] = slices.DeleteFunc(f.world.statuses[name], func(old status) bool {
		return old.sha == s.sha && old.context == s.context
	})
	f.world.statuses[name] = append(f.world.statuses[name], s)
	reply(w, http.StatusCreated, f.world.statusJSON(s))
}

func (f *Forge) createBlob(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha := f.world.nextSHA()
	reply(w, http.StatusCreated, object{"sha": sha, "url": "/blobs/" + sha})
}

func (f *Forge) createTree(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha := f.world.nextSHA()
	reply(w, http.StatusCreated, object{"sha": sha, "url": "/trees/" + sha, "tree": []any{}, "truncated": false})
}

func unverified() object {
	return object{"verified": false, "reason": "unsigned", "signature": nil, "payload": nil, "verified_at": nil}
}

func (f *Forge) createCommit(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	f.mu.Lock()
	sha := f.world.nextSHA()
	f.world.commits[sha] = str(body, "message")
	f.mu.Unlock()
	who := object{"name": "almanac", "email": "almanac@localhost", "date": timestamp}
	author, committer := any(who), any(who)
	if a, ok := body["author"]; ok {
		author = a
	}
	if c, ok := body["committer"]; ok {
		committer = c
	}
	reply(w, http.StatusCreated, object{
		"sha": sha, "node_id": "C_1", "url": "/commits/" + sha, "html_url": "/commit/" + sha,
		"author": author, "committer": committer,
		"message": body["message"], "parents": []any{},
		"tree":         object{"sha": body["tree"], "url": "/tree"},
		"verification": unverified(),
	})
}

func (f *Forge) commit(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha := r.PathValue("sha")
	message, ok := f.world.commits[sha]
	if !ok {
		missing(w)
		return
	}
	who := object{"name": "almanac", "email": "a@b", "date": timestamp}
	reply(w, http.StatusOK, object{
		"sha": sha, "node_id": "C_1", "url": "/commits/" + sha, "html_url": "/commit/" + sha,
		"author": who, "committer": who, "message": message, "parents": []any{},
		"tree":         object{"sha": "t", "url": "/tree"},
		"verification": unverified(),
	})
}

// updateRef moves a branch that exists, and every pull request on it.
func (f *Forge) updateRef(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	name, branch, sha := fullName(r), r.PathValue("branch"), str(body, "sha")
	ref := branchRef{name, branch}
	if _, ok := f.world.refs[ref]; !ok {
		reply(w, http.StatusUnprocessableEntity, object{"message": "Reference does not exist"})
		return
	}
	f.world.refs[ref] = sha
	message := f.world.commits[sha]
	for i := range f.world.pulls[name] {
		p := &f.world.pulls[name][i]
		if p.HeadRef == branch {
			p.moveHead(sha, message)
		}
	}
	reply(w, http.StatusOK, object{
		"ref": "refs/heads/" + branch, "node_id": "REF_1", "url": "/ref",
		"object": object{"type": "commit", "sha": sha, "url": "/commit"},
	})
}

// issues lists issues, not pull requests, carrying every label in the comma-separated labels.
func (f *Forge) issues(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := fullName(r)
	var wanted []string
	if labels := r.URL.Query().Get("labels"); labels != "" {
		wanted = strings.Split(labels, ",")
	}
	var items []any
	for _, i := range f.world.issues[name] {
		if !slices.ContainsFunc(wanted, func(l string) bool { return !slices.Contains(i.labels, l) }) {
			items = append(items, f.world.issueJSON(name, i))
		}
	}
	f.paged(w, r, "page", items)
}

func (f *Forge) createIssue(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	name := fullName(r)
	i := issue{
		number: len(f.world.pulls[name]) + len(f.world.issues[name]) + 1,
		title:  str(body, "title"),
		body:   str(body, "body"),
		labels: strs(body, "labels"),
	}
	f.world.issues[name] = append(f.world.issues[name], i)
	reply(w, http.StatusCreated, f.world.issueJSON(name, i))
}

func (f *Forge) comments(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name, n := fullName(r), number(r)
	f.paged(w, r, "page", list(f.world.comments[thread{name, n}], func(body string) any {
		return f.world.commentJSON(name, n, body)
	}))
}

func (f *Forge) createComment(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	name, n := fullName(r), number(r)
	text := str(body, "body")
	f.world.comments[thread{name, n}] = append(f.world.comments[thread{name, n}], text)
	reply(w, http.StatusCreated, f.world.commentJSON(name, n, text))
}

func (f *Forge) addAssignees(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	name, n := fullName(r), number(r)
	f.world.assignees[thread{name, n}] = append(f.world.assignees[thread{name, n}], strs(body, "assignees")...)
	reply(w, http.StatusCreated, f.world.issueJSON(name, issue{number: n}))
}

// deliveries lists webhook deliveries newest first, paged by cursor.
func (f *Forge) deliveries(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	newest := slices.Clone(f.world.deliveries)
	slices.Reverse(newest)
	f.paged(w, r, "cursor", list(newest, func(d delivery) any { return f.world.deliveryJSON(d) }))
}

// redeliver sends a delivery again with its delivery ID, and records the attempt.
func (f *Forge) redeliver(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f.mu.Lock()
	at := slices.IndexFunc(f.world.deliveries, func(d delivery) bool { return d.id == id })
	var original delivery
	if at >= 0 {
		original = f.world.deliveries[at]
	}
	f.mu.Unlock()
	if err != nil || at < 0 {
		missing(w)
		return
	}
	f.redeliverAttempt(r.Context(), original)
	reply(w, http.StatusAccepted, object{})
}
