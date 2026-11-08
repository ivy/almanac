package simforge

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// timestamp is the one time the forge reports for every created and updated field.
const timestamp = "2026-11-05T12:00:00Z"

// object is a JSON object as GitHub renders one.
type object = map[string]any

// Repo is a repository on the forge.
type Repo struct {
	FullName string
	Private  bool
	Archived bool
	// Renovate is the text of renovate.json. Empty means the repository has none.
	Renovate string
	// Workflows is whether the repository has a .github/workflows directory, which holds one
	// workflow, ci.yml.
	Workflows bool
	// RequiredChecks are the status contexts required on the default branch.
	RequiredChecks []string
}

// PublicRepo is a public, unarchived repository with no Renovate configuration.
func PublicRepo(fullName string) Repo {
	return Repo{FullName: fullName}
}

// Owner is the part of the full name before the slash.
func (r Repo) Owner() string {
	owner, _, _ := strings.Cut(r.FullName, "/")
	return owner
}

// Name is the part of the full name after the slash.
func (r Repo) Name() string {
	_, name, _ := strings.Cut(r.FullName, "/")
	return name
}

// Pull is a pull request on the forge.
type Pull struct {
	// Number is assigned by AddPull.
	Number int
	Title  string
	// Body renders as null when empty, as GitHub renders a pull request with no description.
	Body   string
	Author string
	// HeadRef is the branch the pull request merges from.
	HeadRef string
	// HeadSHA is the tip of HeadRef. AddPull assigns one when it is empty.
	HeadSHA string
	BaseRef string
	Labels  []string
	// State is "open" or "closed".
	State string
	Draft bool
	// MergeableState is GitHub's mergeable_state. "dirty" also makes mergeable false.
	MergeableState string
	// HeadRepo is the full name of the fork the pull request comes from, or empty for a
	// branch in the same repository.
	HeadRepo string
	// Commits are the messages of the commits on the branch, oldest first.
	Commits []string
	// Files are the paths the pull request changes.
	Files []string
	// Diffs holds the patch of a file in Files, where GitHub would give one.
	Diffs map[string]string

	// before is the head before the branch last moved, for a synchronize event.
	before string
}

// RenovatePull is an open Renovate pull request from headRef into main.
func RenovatePull(headRef, title string) Pull {
	return Pull{
		Title:          title,
		Body:           "This PR contains the following updates:",
		Author:         Renovate,
		HeadRef:        headRef,
		BaseRef:        "main",
		State:          "open",
		MergeableState: "clean",
		Commits:        []string{"Update"},
	}
}

func (p Pull) clone() Pull {
	p.Labels = slices.Clone(p.Labels)
	p.Commits = slices.Clone(p.Commits)
	p.Files = slices.Clone(p.Files)
	if p.Diffs != nil {
		diffs := make(map[string]string, len(p.Diffs))
		for file, diff := range p.Diffs {
			diffs[file] = diff
		}
		p.Diffs = diffs
	}
	return p
}

// Suite is a check suite and the single check run it reports through.
type Suite struct {
	// ID is assigned by AddSuite. It is also the ID of the suite's check run and job.
	ID     int64
	SHA    string
	Branch string
	// Status is "queued", "in_progress" or "completed".
	Status string
	// Conclusion renders as null when empty.
	Conclusion string
	// Logs are what the suite's job log serves.
	Logs string
	// Runs is the number of check runs behind the suite. A GitHub App with nothing to
	// report leaves a suite queued forever with none.
	Runs int
}

// FinishedSuite is a completed suite with one check run.
func FinishedSuite(sha, branch, conclusion, logs string) Suite {
	return Suite{SHA: sha, Branch: branch, Status: "completed", Conclusion: conclusion, Logs: logs, Runs: 1}
}

// GhostSuite is a suite an installed App created and will never run.
func GhostSuite(sha, branch string) Suite {
	return Suite{SHA: sha, Branch: branch, Status: "queued"}
}

// Write is one request that was not a GET, recorded for assertions.
type Write struct {
	Method string
	// Path is the request path as sent, without the query.
	Path string
	// Body is the JSON object sent, or nil when the body was not one.
	Body map[string]any
}

type status struct {
	sha, context, state, description string
}

type issue struct {
	number      int
	title, body string
	labels      []string
}

type delivery struct {
	id           int64
	guid         string
	event        string
	action       string
	payload      object
	statusCode   int
	redelivery   bool
	repositoryID int
}

type thread struct {
	repo   string
	number int
}

type branchRef struct {
	repo, branch string
}

type queuedFailure struct {
	pathContains string
	failure      Failure
}

type target struct {
	url, secret string
}

// world is the scripted state. Every field is guarded by Forge.mu.
type world struct {
	baseURL    string
	repos      map[string]Repo
	pulls      map[string][]Pull
	suites     map[string][]Suite
	statuses   map[string][]status
	issues     map[string][]issue
	comments   map[thread][]string
	assignees  map[thread][]string
	refs       map[branchRef]string
	commits    map[string]string
	tarballs   map[string][]byte
	writes     []Write
	failures   []queuedFailure
	deliveries []delivery
	held       []delivery
	fates      []Fate
	target     *target
	// unknownReads is how many more reads of a pull request answer mergeable: null.
	unknownReads int
	next         int64
}

func newWorld() *world {
	return &world{
		repos:     map[string]Repo{},
		pulls:     map[string][]Pull{},
		suites:    map[string][]Suite{},
		statuses:  map[string][]status{},
		issues:    map[string][]issue{},
		comments:  map[thread][]string{},
		assignees: map[thread][]string{},
		refs:      map[branchRef]string{},
		commits:   map[string]string{},
		tarballs:  map[string][]byte{},
	}
}

// nextID numbers suites, deliveries and generated SHAs from one counter.
func (w *world) nextID() int64 {
	w.next++
	return w.next
}

// nextSHA is a stable, commit-shaped SHA: the first 40 hex digits of SHA-256 over the next ID.
func (w *world) nextSHA() string {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(w.nextID()))
	sum := sha256.Sum256(n[:])
	return hex.EncodeToString(sum[:])[:40]
}

// moveHead points a pull request at a new head commit.
func (p *Pull) moveHead(sha, message string) {
	p.before = p.HeadSHA
	p.HeadSHA = sha
	p.Commits = append(p.Commits, message)
}

func (w *world) addPull(repo string, p Pull) int {
	p = p.clone()
	p.Number = len(w.pulls[repo]) + len(w.issues[repo]) + 1
	if p.HeadSHA == "" {
		p.HeadSHA = w.nextSHA()
	}
	w.refs[branchRef{repo, p.HeadRef}] = p.HeadSHA
	w.pulls[repo] = append(w.pulls[repo], p)
	return p.Number
}

// pull finds a pull request, or nil.
func (w *world) pull(repo string, number int) *Pull {
	for i := range w.pulls[repo] {
		if w.pulls[repo][i].Number == number {
			return &w.pulls[repo][i]
		}
	}
	return nil
}

// mustPull is for scenario methods, where a missing pull request is a mistake in the spec.
func (w *world) mustPull(repo string, number int) *Pull {
	p := w.pull(repo, number)
	if p == nil {
		panic(fmt.Sprintf("simforge: no pull request %s#%d was added", repo, number))
	}
	return p
}

func (w *world) mustRepo(name string) Repo {
	r, ok := w.repos[name]
	if !ok {
		panic(fmt.Sprintf("simforge: no repository %s was added", name))
	}
	return r
}

// sortedRepos lists repositories by full name, the order GitHub's fake lists them in.
func (w *world) sortedRepos() []Repo {
	names := make([]string, 0, len(w.repos))
	for name := range w.repos {
		names = append(names, name)
	}
	slices.Sort(names)
	repos := make([]Repo, len(names))
	for i, name := range names {
		repos[i] = w.repos[name]
	}
	return repos
}

// repoID is a repository's position by full name, from 1, or 0 for an unknown one. It shifts
// as repositories are added.
func (w *world) repoID(fullName string) int {
	for i, r := range w.sortedRepos() {
		if r.FullName == fullName {
			return i + 1
		}
	}
	return 0
}

func (w *world) url(path string) string {
	return w.baseURL + path
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// list renders a slice as a JSON array that is never null.
func list[T any](items []T, render func(T) any) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, render(item))
	}
	return out
}

func (w *world) userJSON(login string) object {
	kind := "User"
	if strings.HasSuffix(login, "[bot]") {
		kind = "Bot"
	}
	return object{
		"login": login, "id": 1, "node_id": "U_1", "type": kind, "site_admin": false,
		"avatar_url": w.url("/avatar"), "gravatar_id": "",
		"url":      w.url("/users/" + login),
		"html_url": w.url("/" + login),
	}
}

func (w *world) repoJSON(r Repo) object {
	visibility := "public"
	if r.Private {
		visibility = "private"
	}
	return object{
		"id": w.repoID(r.FullName), "node_id": "R_1",
		"name": r.Name(), "full_name": r.FullName,
		"owner":   w.userJSON(r.Owner()),
		"private": r.Private, "archived": r.Archived, "disabled": false, "fork": false,
		"visibility":     visibility,
		"default_branch": "main",
		"description":    nil,
		"url":            w.url("/repos/" + r.FullName),
		"html_url":       w.url("/" + r.FullName),
		"created_at":     timestamp, "updated_at": timestamp, "pushed_at": timestamp,
	}
}

func (w *world) branchJSON(r Repo, branch, sha string) object {
	return object{
		"label": r.Owner() + ":" + branch, "ref": branch, "sha": sha,
		"user": w.userJSON(r.Owner()), "repo": w.repoJSON(r),
	}
}

func (w *world) headJSON(r Repo, p *Pull) object {
	head := w.branchJSON(r, p.HeadRef, p.HeadSHA)
	if p.HeadRepo != "" {
		head["repo"].(object)["full_name"] = p.HeadRepo
	}
	return head
}

func labelJSON(name string) any {
	return object{
		"id": 1, "node_id": "L_1", "url": "", "name": name, "description": "",
		"color": "ededed", "default": false,
	}
}

// pullSimpleJSON is a pull request as a list shows it, without what GitHub only computes for
// a single pull request.
func (w *world) pullSimpleJSON(r Repo, p *Pull) object {
	return object{
		"id": p.Number, "node_id": "PR_1", "number": p.Number, "state": p.State,
		"locked": false, "title": p.Title, "body": nullable(p.Body), "draft": p.Draft,
		"user":       w.userJSON(p.Author),
		"labels":     list(p.Labels, labelJSON),
		"head":       w.headJSON(r, p),
		"base":       w.branchJSON(r, p.BaseRef, strings.Repeat("0", 40)),
		"url":        w.url(fmt.Sprintf("/repos/%s/pulls/%d", r.FullName, p.Number)),
		"html_url":   w.url(fmt.Sprintf("/%s/pull/%d", r.FullName, p.Number)),
		"created_at": timestamp, "updated_at": timestamp,
		"closed_at": nil, "merged_at": nil, "merge_commit_sha": nil,
		"assignee": nil, "assignees": []any{}, "requested_reviewers": []any{}, "requested_teams": []any{},
		"milestone": nil, "author_association": "NONE", "auto_merge": nil,
	}
}

func (w *world) pullJSON(r Repo, p *Pull) object {
	value := w.pullSimpleJSON(r, p)
	if w.unknownReads > 0 {
		value["mergeable"] = nil
	} else {
		value["mergeable"] = p.MergeableState != "dirty"
	}
	value["mergeable_state"] = p.MergeableState
	value["merged"] = false
	return value
}

func (w *world) commitSummary(sha string) object {
	message, ok := w.commits[sha]
	if !ok {
		message = "Update"
	}
	person := object{"name": "a", "email": "a@b"}
	return object{
		"id": sha, "tree_id": sha, "message": message, "timestamp": timestamp,
		"author": person, "committer": person,
	}
}

// actionsJSON is the GitHub App behind every check suite and run: GitHub Actions.
func (w *world) actionsJSON() object {
	return object{
		"id": 15368, "slug": "github-actions", "node_id": "A_1", "name": "GitHub Actions",
		"owner": w.userJSON("github"), "description": "", "client_id": "Iv1.1",
		"external_url": "https://help.github.com/en/actions", "html_url": w.url("/apps/github-actions"),
		"created_at": timestamp, "updated_at": timestamp,
		"permissions": object{"checks": "write"}, "events": []any{"check_suite"},
	}
}

func (w *world) suiteJSON(s Suite) object {
	return object{
		"id": s.ID, "node_id": "CS_1", "head_branch": s.Branch, "head_sha": s.SHA,
		"status": s.Status, "conclusion": nullable(s.Conclusion),
		"url":    w.url(fmt.Sprintf("/check-suites/%d", s.ID)),
		"before": nil, "after": nil, "pull_requests": []any{}, "app": w.actionsJSON(),
		"head_commit": w.commitSummary(s.SHA),
		"created_at":  timestamp, "updated_at": timestamp,
		"latest_check_runs_count": s.Runs,
	}
}

func (w *world) checkRunJSON(repo string, s Suite) object {
	return object{
		"id": s.ID, "node_id": "CR_1", "head_sha": s.SHA, "name": "ci", "external_id": "",
		"status": s.Status, "conclusion": nullable(s.Conclusion),
		"url":         w.url(fmt.Sprintf("/check-runs/%d", s.ID)),
		"html_url":    w.url(fmt.Sprintf("/%s/runs/%d", repo, s.ID)),
		"details_url": w.url(fmt.Sprintf("/%s/actions/runs/%d", repo, s.ID)),
		"started_at":  timestamp, "completed_at": timestamp,
		"output": object{
			"title": nil, "summary": nil, "text": nil,
			"annotations_count": 0, "annotations_url": "",
		},
		"check_suite": object{"id": s.ID}, "app": w.actionsJSON(), "pull_requests": []any{},
	}
}

func (w *world) statusJSON(s status) object {
	return object{
		"id": 1, "node_id": "S_1", "state": s.state, "context": s.context,
		"description": nullable(s.description), "target_url": nil, "avatar_url": nil,
		"url":        w.url("/statuses/" + s.sha),
		"created_at": timestamp, "updated_at": timestamp,
	}
}

func (w *world) commentJSON(repo string, number int, body string) object {
	path := fmt.Sprintf("/repos/%s/issues/%d", repo, number)
	return object{
		"id": number, "node_id": "IC_1", "body": body, "user": nil,
		"url":        w.url(path + "/comments/1"),
		"html_url":   w.url(fmt.Sprintf("/%s/issues/%d#issuecomment-1", repo, number)),
		"issue_url":  w.url(path),
		"created_at": timestamp, "updated_at": timestamp, "author_association": "NONE",
	}
}

func (w *world) issueJSON(repo string, i issue) object {
	return object{
		"id": i.number, "node_id": "I_1", "number": i.number, "title": i.title,
		"body": nullable(i.body), "state": "open", "locked": false, "labels": list(i.labels, labelJSON),
		"user": nil, "assignee": nil, "assignees": []any{}, "milestone": nil, "comments": 0,
		"url":        w.url(fmt.Sprintf("/repos/%s/issues/%d", repo, i.number)),
		"html_url":   w.url(fmt.Sprintf("/%s/issues/%d", repo, i.number)),
		"created_at": timestamp, "updated_at": timestamp, "closed_at": nil,
		"author_association": "NONE",
	}
}

func (w *world) deliveryJSON(d delivery) object {
	outcome := "failed"
	if d.statusCode < 300 {
		outcome = "OK"
	}
	return object{
		"id": d.id, "guid": d.guid, "delivered_at": timestamp, "redelivery": d.redelivery,
		"duration": 0.1, "status": outcome,
		"status_code": d.statusCode, "event": d.event, "action": nullable(d.action),
		"installation_id": 1, "repository_id": d.repositoryID,
	}
}
