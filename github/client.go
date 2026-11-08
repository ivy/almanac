// Package github reaches GitHub through its REST API: the reads workflows and scans make, a
// [Forge] that sets statuses and pushes checked patches, an [Issues] tracker that files work and
// asks people, and a sweep that asks GitHub to resend webhook deliveries that failed.
//
// Everything GitHub answers may hold text an attacker wrote, so reads return it hostile: JSON as
// a hostile.Of[json.RawMessage], which a caller reads with hostile.DecodeClean into a clean type
// or keeps hostile with hostile.Decode. Writes are not exported. They happen only through Forge,
// Issues and the sweep, which is how a workflow is kept to its effects and never merges.
//
// See https://docs.github.com/en/rest for the API.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/stall"
)

const (
	userAgent = "almanac"
	// perPage is the most items GitHub returns in one page.
	perPage = 100
	// mostRedeliveries keeps one sweep from flooding a receiver that was down for a while.
	mostRedeliveries = 20
	// GitHub allows an App's own token at most ten minutes, and clocks drift.
	jwtLifetime = 540 * time.Second
	jwtBackdate = 60 * time.Second
	// tokenMargin is how close to expiry an installation token is replaced before use.
	tokenMargin = 120 * time.Second
)

// Retry is how hard a client tries before it reports a failure.
type Retry struct {
	Attempts int
	// Backoff is the first wait after a server error or a failed send, doubled each time.
	Backoff time.Duration
	// LongestWait is the longest rate limit a client waits out. A longer one is reported.
	LongestWait time.Duration
}

// DefaultRetry tries five times, backing off from half a second, and waits out a rate limit
// of up to a minute.
func DefaultRetry() Retry {
	return Retry{Attempts: 5, Backoff: 500 * time.Millisecond, LongestWait: time.Minute}
}

// Auth is how a client proves who it is: a [Token] or an [App].
type Auth interface{ auth() }

// Token is a personal access token, for read-only use.
type Token string

func (Token) auth() {}

// App authenticates as a GitHub App installation. The client signs a token as the App, then
// trades it for the installation's token, which it keeps until shortly before it expires.
// See https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app.
type App struct {
	ID string
	// PrivateKey is the App's key in PEM. An environment file holds one line, so newlines may
	// arrive escaped as \n; a PEM has no backslashes of its own.
	PrivateKey   string
	Installation uint64
}

func (App) auth() {}

// Option changes how a client behaves.
type Option func(*Client)

// ReadOnly refuses every write, whatever its caller believes. A dry run against real
// repositories uses this as well as effect.DryRun.
func ReadOnly() Option { return func(c *Client) { c.readOnly = true } }

// Retrying replaces [DefaultRetry].
func Retrying(r Retry) Option { return func(c *Client) { c.retry = r } }

// Client is GitHub's REST API, with authentication, pagination, retries and rate limits.
// It is safe for concurrent use.
type Client struct {
	http     *http.Client
	baseURL  string
	auth     Auth
	readOnly bool
	retry    Retry

	// mu is held while an installation token is read or minted, so concurrent requests mint
	// one.
	mu           sync.Mutex
	installation *installationToken
}

type installationToken struct {
	token     string
	expiresAt time.Time
}

// New is a client of the API at baseURL, such as https://api.github.com.
func New(baseURL string, auth Auth, options ...Option) *Client {
	c := &Client{
		http:    stall.Client(),
		baseURL: strings.TrimSuffix(baseURL, "/"),
		auth:    auth,
		retry:   DefaultRetry(),
	}
	for _, option := range options {
		option(c)
	}
	return c
}

// Refusal is the HTTP status GitHub refused a request with. The *diag.Error for a refusal
// wraps one, so errors.As finds it.
type Refusal struct {
	Status int
}

func (r *Refusal) Error() string { return "GitHub answered " + strconv.Itoa(r.Status) }

func refused(action string, status int) error {
	err := diag.Refused(action, status)
	err.Cause = &Refusal{Status: status}
	return err
}

// notFound reports whether err is GitHub answering 404.
func notFound(err error) bool {
	var refusal *Refusal
	return errors.As(err, &refusal) && refusal.Status == http.StatusNotFound
}

func (c *Client) jwt(app App) (string, error) {
	failed := func(err error) error { return diag.Unreachable("signing an App token", err.Error()) }
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(strings.ReplaceAll(app.PrivateKey, `\n`, "\n")))
	if err != nil {
		return "", failed(err)
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    app.ID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-jwtBackdate)),
		ExpiresAt: jwt.NewNumericDate(now.Add(jwtLifetime)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		return "", failed(err)
	}
	return signed, nil
}

// bearer is the token for path. /app endpoints take the App's own token, and everything else
// an installation's.
func (c *Client) bearer(ctx context.Context, path string) (string, error) {
	switch auth := c.auth.(type) {
	case Token:
		return string(auth), nil
	case App:
		return c.appBearer(ctx, auth, path)
	default:
		return "", fmt.Errorf("github: no way to authenticate with a %T", c.auth)
	}
}

func (c *Client) appBearer(ctx context.Context, app App, path string) (string, error) {
	if path == "/app" || strings.HasPrefix(path, "/app/") {
		return c.jwt(app)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.installation != nil && time.Until(c.installation.expiresAt) > tokenMargin {
		return c.installation.token, nil
	}
	signed, err := c.jwt(app)
	if err != nil {
		return "", err
	}
	const action = "getting an installation token"
	address := fmt.Sprintf("%s/app/installations/%d/access_tokens", c.baseURL, app.Installation)
	response, err := c.send(ctx, http.MethodPost, address, signed, nil, action)
	if err != nil {
		return "", err
	}
	var minted struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := decodeBody(response, action, &minted); err != nil {
		return "", err
	}
	expiresAt, err := time.Parse(time.RFC3339, minted.ExpiresAt)
	if err != nil {
		expiresAt = time.Now()
	}
	c.installation = &installationToken{token: minted.Token, expiresAt: expiresAt}
	return minted.Token, nil
}

// send makes a request with retries. A rate limit is waited out as asked, unless it asks too
// long, and a server error or a failed send backs off. Any other refusal is reported at once.
func (c *Client) send(ctx context.Context, method, address, token string, body []byte, action string) (*http.Response, error) {
	backoff := c.retry.Backoff
	lastFailure := "no attempt was made"
	for attempt := 1; attempt <= c.retry.Attempts; attempt++ {
		var sent io.Reader
		if body != nil {
			sent = bytes.NewReader(body)
		}
		request, err := http.NewRequestWithContext(ctx, method, address, sent)
		if err != nil {
			return nil, diag.Unreachable(action, err.Error())
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		request.Header.Set("User-Agent", userAgent)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		wait := backoff
		response, err := c.http.Do(request)
		if err != nil {
			lastFailure = err.Error()
		} else {
			limit, limited := rateLimitWait(response)
			switch {
			case limited && (limit > c.retry.LongestWait || attempt == c.retry.Attempts):
				discard(response)
				return nil, diag.RateLimited(action, limit)
			case limited:
				discard(response)
				wait = limit
			case response.StatusCode >= 500:
				discard(response)
				lastFailure = "GitHub answered " + response.Status
			case response.StatusCode >= 200 && response.StatusCode < 300:
				return response, nil
			default:
				discard(response)
				return nil, refused(action, response.StatusCode)
			}
		}
		backoff *= 2
		if attempt < c.retry.Attempts {
			if err := sleep(ctx, wait); err != nil {
				return nil, diag.Unreachable(action, err.Error())
			}
		}
	}
	return nil, diag.Unreachable(action, lastFailure)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func discard(response *http.Response) {
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}

// rateLimitWait is how long a refusal asks to wait, if it is a rate limit at all. See
// https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api.
func rateLimitWait(response *http.Response) (time.Duration, bool) {
	header := response.Header
	retryAfter := header.Get("Retry-After")
	remaining, err := strconv.ParseUint(header.Get("X-RateLimit-Remaining"), 10, 64)
	exhausted := err == nil && remaining == 0
	limited := response.StatusCode == http.StatusTooManyRequests ||
		(response.StatusCode == http.StatusForbidden && (retryAfter != "" || exhausted))
	if !limited {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(retryAfter, 10, 64); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	reset, _ := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64)
	return time.Duration(max(reset-time.Now().Unix(), 1)) * time.Second, true
}

// request sends method to path, with body as JSON if it is not nil. A read-only client
// refuses anything but a GET before it is sent.
func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	action := method + " " + path
	if c.readOnly && method != http.MethodGet {
		return nil, diag.ReadOnly(action)
	}
	var encoded []byte
	if body != nil {
		var err error
		if encoded, err = json.Marshal(body); err != nil {
			return nil, fmt.Errorf("github: encoding the body of %s: %w", action, err)
		}
	}
	token, err := c.bearer(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.send(ctx, method, c.baseURL+path, token, encoded, action)
}

// decodeBody reads a response's JSON into into, and closes it.
func decodeBody(response *http.Response, action string, into any) error {
	defer discard(response)
	if err := json.NewDecoder(response.Body).Decode(into); err != nil {
		return diag.Unreachable(action, "the answer is not the JSON expected: "+err.Error())
	}
	return nil
}

// readBody reads a whole response, and closes it.
func readBody(response *http.Response, action string) ([]byte, error) {
	defer discard(response)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, diag.Unreachable(action, err.Error())
	}
	return body, nil
}

// getJSON reads path into into.
func (c *Client) getJSON(ctx context.Context, path string, into any) error {
	response, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return decodeBody(response, path, into)
}

// write sends a write and reads its answer into into, unless into is nil.
func (c *Client) write(ctx context.Context, method, path string, body, into any) error {
	response, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if into == nil {
		discard(response)
		return nil
	}
	return decodeBody(response, path, into)
}

// Get reads path, such as /repos/owner/repo, as hostile JSON.
func (c *Client) Get(ctx context.Context, path string) (hostile.Of[json.RawMessage], error) {
	var raw json.RawMessage
	if err := c.getJSON(ctx, path, &raw); err != nil {
		return hostile.Of[json.RawMessage]{}, err
	}
	return hostile.New(raw), nil
}

// GetAll reads every item of a list, following Link headers to each page, each item as hostile
// JSON. path must not set per_page itself.
func (c *Client) GetAll(ctx context.Context, path string) ([]hostile.Of[json.RawMessage], error) {
	items, err := c.all(ctx, path)
	if err != nil {
		return nil, err
	}
	return each(items), nil
}

func each(items []json.RawMessage) []hostile.Of[json.RawMessage] {
	out := make([]hostile.Of[json.RawMessage], len(items))
	for i, item := range items {
		out[i] = hostile.New(item)
	}
	return out
}

// all reads every item of a list, following Link headers. A link to anywhere but this
// client's API ends the list, so the token never goes elsewhere.
func (c *Client) all(ctx context.Context, path string) ([]json.RawMessage, error) {
	joiner := "?"
	if strings.Contains(path, "?") {
		joiner = "&"
	}
	token, err := c.bearer(ctx, path)
	if err != nil {
		return nil, err
	}
	address := c.baseURL + path + joiner + "per_page=" + strconv.Itoa(perPage)
	var items []json.RawMessage
	for {
		response, err := c.send(ctx, http.MethodGet, address, token, nil, path)
		if err != nil {
			return nil, err
		}
		next, more := nextPage(response.Header.Get("Link"), c.baseURL)
		body, err := readBody(response, path)
		if err != nil {
			return nil, err
		}
		if !json.Valid(body) {
			return nil, diag.Unreachable(path, "the answer is not JSON")
		}
		// An answer that is not a list holds no items.
		var page []json.RawMessage
		if json.Unmarshal(body, &page) == nil {
			items = append(items, page...)
		}
		if !more {
			return items, nil
		}
		address = next
	}
}

// nextPage is the rel="next" link of a Link header, if it is on baseURL.
func nextPage(link, baseURL string) (string, bool) {
	for part := range strings.SplitSeq(link, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start, end := strings.Index(part, "<"), strings.Index(part, ">")
		if start < 0 || end < start {
			return "", false
		}
		next := part[start+1 : end]
		return next, strings.HasPrefix(next, baseURL)
	}
	return "", false
}

// installationRepositories reads every repository the installation can reach. The answer is
// an object, not a list, so it is paged by number rather than by Link.
func (c *Client) installationRepositories(ctx context.Context) ([]json.RawMessage, error) {
	var all []json.RawMessage
	for page := 1; ; page++ {
		var found struct {
			Repositories []json.RawMessage `json:"repositories"`
		}
		path := fmt.Sprintf("/installation/repositories?per_page=%d&page=%d", perPage, page)
		if err := c.getJSON(ctx, path, &found); err != nil {
			return nil, err
		}
		all = append(all, found.Repositories...)
		if len(found.Repositories) < perPage {
			return all, nil
		}
	}
}

// Repositories are the repositories owner owns that the client can reach, leaving out
// archived ones. A token lists its user's own repositories, and an App its installation's.
func (c *Client) Repositories(ctx context.Context, owner string) ([]hostile.Of[json.RawMessage], error) {
	var all []json.RawMessage
	var err error
	if _, isApp := c.auth.(App); isApp {
		all, err = c.installationRepositories(ctx)
	} else {
		all, err = c.all(ctx, "/user/repos?affiliation=owner")
	}
	if err != nil {
		return nil, err
	}
	var kept []json.RawMessage
	for _, item := range all {
		var repo struct {
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
			Archived bool `json:"archived"`
		}
		if json.Unmarshal(item, &repo) == nil && repo.Owner.Login == owner && !repo.Archived {
			kept = append(kept, item)
		}
	}
	return each(kept), nil
}

// byAuthor keeps the pull requests author opened.
func byAuthor(pulls []json.RawMessage, author string) []json.RawMessage {
	var kept []json.RawMessage
	for _, item := range pulls {
		var pull struct {
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if json.Unmarshal(item, &pull) == nil && pull.User.Login == author {
			kept = append(kept, item)
		}
	}
	return kept
}

// PullsBy are the open pull requests in repo that author opened, as REST responses.
func (c *Client) PullsBy(ctx context.Context, repo hostile.Slug, author string) ([]hostile.Of[json.RawMessage], error) {
	pulls, err := c.all(ctx, "/repos/"+repo.String()+"/pulls?state=open")
	if err != nil {
		return nil, err
	}
	return each(byAuthor(pulls, author)), nil
}

func pullPath(repo hostile.Slug, number uint64) string {
	return fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
}

// Pull is a pull request, as a REST response.
func (c *Client) Pull(ctx context.Context, repo hostile.Slug, number uint64) (hostile.Of[json.RawMessage], error) {
	return c.Get(ctx, pullPath(repo, number))
}

// PullSettled is a pull request once GitHub has worked out whether it can merge, which it does
// lazily: it reads up to reads times, pause apart. If mergeable is still null after that, it
// returns the last read, which a caller treats as still waiting.
func (c *Client) PullSettled(ctx context.Context, repo hostile.Slug, number uint64, reads int, pause time.Duration) (hostile.Of[json.RawMessage], error) {
	var raw json.RawMessage
	for read := 1; ; read++ {
		if err := c.getJSON(ctx, pullPath(repo, number), &raw); err != nil {
			return hostile.Of[json.RawMessage]{}, err
		}
		var pull struct {
			Mergeable *bool `json:"mergeable"`
		}
		if read >= reads || (json.Unmarshal(raw, &pull) == nil && pull.Mergeable != nil) {
			return hostile.New(raw), nil
		}
		if err := sleep(ctx, pause); err != nil {
			return hostile.Of[json.RawMessage]{}, err
		}
	}
}

// CheckSuites are the check suites on a commit.
func (c *Client) CheckSuites(ctx context.Context, repo hostile.Slug, sha hostile.Sha) (hostile.Of[json.RawMessage], error) {
	return c.Get(ctx, fmt.Sprintf("/repos/%s/commits/%s/check-suites", repo, sha))
}

// CheckRuns are the check runs on a commit.
func (c *Client) CheckRuns(ctx context.Context, repo hostile.Slug, sha hostile.Sha) (hostile.Of[json.RawMessage], error) {
	return c.Get(ctx, fmt.Sprintf("/repos/%s/commits/%s/check-runs", repo, sha))
}

// CombinedStatus is a commit's statuses, combined.
func (c *Client) CombinedStatus(ctx context.Context, repo hostile.Slug, sha hostile.Sha) (hostile.Of[json.RawMessage], error) {
	return c.Get(ctx, fmt.Sprintf("/repos/%s/commits/%s/status", repo, sha))
}

// JobLogs is the log of an Actions job, as text.
func (c *Client) JobLogs(ctx context.Context, repo hostile.Slug, job uint64) (hostile.Of[string], error) {
	path := fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, job)
	response, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return hostile.Of[string]{}, err
	}
	body, err := readBody(response, path)
	if err != nil {
		return hostile.Of[string]{}, err
	}
	return hostile.New(string(body)), nil
}

// PullForBranch is the open pull request whose head is branch in repo itself, as a REST
// response, and whether there is one.
func (c *Client) PullForBranch(ctx context.Context, repo, branch hostile.Slug) (hostile.Of[json.RawMessage], bool, error) {
	owner, _, _ := strings.Cut(repo.String(), "/")
	head := url.QueryEscape(owner + ":" + branch.String())
	pulls, err := c.all(ctx, "/repos/"+repo.String()+"/pulls?state=open&head="+head)
	if err != nil || len(pulls) == 0 {
		return hostile.Of[json.RawMessage]{}, false, err
	}
	return hostile.New(pulls[0]), true, nil
}

type pullFile struct {
	Filename string  `json:"filename"`
	Patch    *string `json:"patch"`
}

func (c *Client) pullFiles(ctx context.Context, repo hostile.Slug, number uint64) ([]pullFile, error) {
	path := pullPath(repo, number) + "/files"
	items, err := c.all(ctx, path)
	if err != nil {
		return nil, err
	}
	files := make([]pullFile, 0, len(items))
	for _, item := range items {
		var file pullFile
		if err := json.Unmarshal(item, &file); err != nil {
			return nil, diag.Unreachable(path, "the answer is not the JSON expected: "+err.Error())
		}
		files = append(files, file)
	}
	return files, nil
}

// PullFiles are the paths a pull request changes.
func (c *Client) PullFiles(ctx context.Context, repo hostile.Slug, number uint64) (hostile.Of[[]string], error) {
	files, err := c.pullFiles(ctx, repo, number)
	if err != nil {
		return hostile.Of[[]string]{}, err
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file.Filename != "" {
			paths = append(paths, file.Filename)
		}
	}
	return hostile.New(paths), nil
}

// AddedLine is a line a pull request adds to a file.
type AddedLine struct {
	Path string `json:"path"`
	Line string `json:"line"`
}

// PullAddedLines are the lines a pull request adds. GitHub leaves out the diff of a large file,
// so such a file has none here.
func (c *Client) PullAddedLines(ctx context.Context, repo hostile.Slug, number uint64) (hostile.Of[[]AddedLine], error) {
	files, err := c.pullFiles(ctx, repo, number)
	if err != nil {
		return hostile.Of[[]AddedLine]{}, err
	}
	var added []AddedLine
	for _, file := range files {
		if file.Patch == nil {
			continue
		}
		for line := range strings.Lines(*file.Patch) {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if text, ok := strings.CutPrefix(line, "+"); ok {
				added = append(added, AddedLine{Path: file.Filename, Line: text})
			}
		}
	}
	return hostile.New(added), nil
}

// Tarball is a commit's source as a gzipped tar, with one top directory. Its contents are
// hostile; they go only into a sandbox.
func (c *Client) Tarball(ctx context.Context, repo hostile.Slug, sha hostile.Sha) ([]byte, error) {
	path := fmt.Sprintf("/repos/%s/tarball/%s", repo, sha)
	response, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return readBody(response, path)
}

// PullCommitMessages are the messages of the commits on a pull request, oldest first.
func (c *Client) PullCommitMessages(ctx context.Context, repo hostile.Slug, number uint64) (hostile.Of[[]string], error) {
	items, err := c.all(ctx, pullPath(repo, number)+"/commits")
	if err != nil {
		return hostile.Of[[]string]{}, err
	}
	var messages []string
	for _, item := range items {
		var commit struct {
			Commit struct {
				Message *string `json:"message"`
			} `json:"commit"`
		}
		if json.Unmarshal(item, &commit) == nil && commit.Commit.Message != nil {
			messages = append(messages, *commit.Commit.Message)
		}
	}
	return hostile.New(messages), nil
}

func contentsPath(repo hostile.Slug, path string) string {
	return "/repos/" + repo.String() + "/contents/" + path
}

// Exists reports whether repo has a file or directory at path, which the caller writes.
func (c *Client) Exists(ctx context.Context, repo hostile.Slug, path string) (bool, error) {
	var ignored json.RawMessage
	err := c.getJSON(ctx, contentsPath(repo, path), &ignored)
	if notFound(err) {
		return false, nil
	}
	return err == nil, err
}

// File is the text of the file at path in repo, and whether repo has one there.
func (c *Client) File(ctx context.Context, repo hostile.Slug, path string) (hostile.Of[string], bool, error) {
	var found struct {
		Content string `json:"content"`
	}
	err := c.getJSON(ctx, contentsPath(repo, path), &found)
	if notFound(err) {
		return hostile.Of[string]{}, false, nil
	}
	if err != nil {
		return hostile.Of[string]{}, false, err
	}
	// GitHub wraps the base64 at 60 columns.
	decoded, _ := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(found.Content), ""))
	return hostile.New(strings.ToValidUTF8(string(decoded), "�")), true, nil
}

// RequiredChecks are the status contexts branch requires. A token that may not see branch
// protection reads as requiring none.
func (c *Client) RequiredChecks(ctx context.Context, repo, branch hostile.Slug) (hostile.Of[[]string], error) {
	var found struct {
		Protection struct {
			RequiredStatusChecks struct {
				Contexts []string `json:"contexts"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	if err := c.getJSON(ctx, "/repos/"+repo.String()+"/branches/"+branch.String(), &found); err != nil {
		return hostile.Of[[]string]{}, err
	}
	return hostile.New(found.Protection.RequiredStatusChecks.Contexts), nil
}

// Deliveries are the App's recent webhook deliveries, newest first.
func (c *Client) Deliveries(ctx context.Context) ([]hostile.Of[json.RawMessage], error) {
	return c.GetAll(ctx, "/app/hook/deliveries")
}
