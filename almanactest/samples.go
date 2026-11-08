package almanactest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/almanactest/definitions"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// Beside the sample workflows, the events and workflows below are written by hand as
// workflow.Coded values. They mirror the workflows the Rust tests in crates/definitions/tests
// write for themselves, which reach parts of the runtime a model cannot: an agent no step
// names, a write in gather, a scan over a fleet.

// injection is text an attacker controls, which must never reach a guard, a prompt or a log.
const injection = definitions.Injection

// pullRequest is a pull request opened, as a webhook delivers it.
type pullRequest struct {
	Number uint64
	Repo   hostile.Slug
	Branch hostile.Slug
	Head   hostile.Sha
	Title  hostile.Of[string]
	Labels hostile.Of[[]string]
}

func (pullRequest) Kind() string        { return "github.pull_request.opened" }
func (pullRequest) AlsoKinds() []string { return nil }
func (pullRequest) Operation() string   { return "pulls/get" }

func (pullRequest) Projections() []wire.Projection {
	return []wire.Projection{
		{From: "/number", To: "/number"},
		{From: "/base/repo/full_name", To: "/repository/full_name"},
		{From: "/head/ref", To: "/pull_request/head/ref"},
		{From: "/head/sha", To: "/pull_request/head/sha"},
		{From: "/title", To: "/pull_request/title"},
		{From: "/labels", To: "/pull_request/labels"},
	}
}

func (e *pullRequest) Parse(p *wire.Payload) {
	e.Number = wire.Field[uint64](p, "/number")
	e.Repo = wire.Field[hostile.Slug](p, "/repository/full_name")
	e.Branch = wire.Field[hostile.Slug](p, "/pull_request/head/ref")
	e.Head = wire.Field[hostile.Sha](p, "/pull_request/head/sha")
	e.Title = p.Text("/pull_request/title")
	e.Labels = p.TextList("/pull_request/labels", "/name")
}

// checkSuite is CI finishing on a commit.
type checkSuite struct {
	Suite uint64
	Repo  hostile.Slug
	Head  hostile.Sha
}

func (checkSuite) Kind() string        { return "github.check_suite.completed" }
func (checkSuite) AlsoKinds() []string { return nil }

func (e *checkSuite) Parse(p *wire.Payload) {
	e.Suite = wire.Field[uint64](p, "/check_suite/id")
	e.Repo = wire.Field[hostile.Slug](p, "/repository/full_name")
	e.Head = wire.Field[hostile.Sha](p, "/check_suite/head_sha")
}

// commitStatus is a status set on a commit.
type commitStatus struct {
	Repo hostile.Slug
	Sha  hostile.Sha
}

func (commitStatus) Kind() string        { return "github.status" }
func (commitStatus) AlsoKinds() []string { return nil }

func (e *commitStatus) Parse(p *wire.Payload) {
	e.Repo = wire.Field[hostile.Slug](p, "/repository/full_name")
	e.Sha = wire.Field[hostile.Sha](p, "/sha")
}

// Payloads, as each source sends them.

const (
	head        = "0123456789abcdef0123456789abcdef01234567"
	me          = "almanac[bot]"
	renovateBot = "renovate[bot]"
)

func pullPayload(repo string, sender string) json.RawMessage {
	return mustJSON(map[string]any{
		"number":     1,
		"sender":     map[string]any{"login": sender, "type": "Bot"},
		"repository": map[string]any{"full_name": repo},
		"pull_request": map[string]any{
			"state":  "open",
			"draft":  false,
			"title":  "chore(deps): bump",
			"body":   injection,
			"user":   map[string]any{"login": sender},
			"head":   map[string]any{"ref": "renovate/ts", "sha": head},
			"labels": []any{map[string]any{"name": "dependencies"}},
		},
	})
}

func suitePayload(repo string, suite int, sender string) json.RawMessage {
	return mustJSON(map[string]any{
		"check_suite": map[string]any{"id": suite, "head_sha": head, "conclusion": "failure"},
		"repository":  map[string]any{"full_name": repo},
		"sender":      map[string]any{"login": sender},
	})
}

func statusPayload(repo string, sender string) json.RawMessage {
	return mustJSON(map[string]any{
		"sha":        head,
		"state":      "success",
		"context":    "almanac/trust",
		"repository": map[string]any{"full_name": repo},
		"sender":     map[string]any{"login": sender},
	})
}

func reportPayload(repo string) json.RawMessage {
	return mustJSON(map[string]any{"issue_id": 7, "repo": repo, "message": injection})
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// The sample workflows are the models in almanactest/definitions, run by the engine, so the
// scenarios specify the runtime against the workflows Almanac ships as examples.
var (
	dependencyUpdates = runnable(definitions.DependencyUpdates)
	errorTriage       = runnable(definitions.ErrorTriage)
)

func runnable(m model.Workflow) workflow.Registered {
	w, err := definitions.Runnable(m)
	if err != nil {
		panic(err)
	}
	return w
}

var (
	prompt   = agent.MustPrompt(agent.ParsePrompt("inline", "Review the update to {{ repo }}.", "repo"))
	reviewer = agent.NewQuarantined("reviewer", prompt)
	repairer = agent.NewFixer("repairer", prompt)
	lodash   = definitions.Lodash
)

// review is what the reviewer answers. It stays hostile.
type review struct {
	Verdict string `json:"verdict"`
	Notes   string `json:"notes"`
}

func noConcerns() string {
	return string(mustJSON(map[string]any{"verdict": "NoConcerns", "notes": injection}))
}

// hidesItsAgent calls an agent it never declared.
var hidesItsAgent = workflow.Code[pullRequest](workflow.Coded[pullRequest, struct{}]{
	Name:        "hides-its-agent",
	Intent:      "Read a pull request.",
	May:         []effect.Kind{effect.KindAttest},
	RunsPerHour: 1,
	Key:         func(pr pullRequest) string { return pr.Repo.String() },
	Gather: func(ctx context.Context, cx workflow.Cx, pr pullRequest) (struct{}, error) {
		_, err := workflow.Read[review](ctx, cx, reviewer, agent.Vars{"repo": pr.Repo}, pr.Number)
		return struct{}{}, err
	},
	Decide: func(struct{}) []effect.Effect { return nil },
})

// cooldownGate waits until a release is old enough, whatever any status says.
var cooldownGate = workflow.Code[pullRequest](workflow.Coded[pullRequest, cooled.Age]{
	Name:        "cooldown-gate",
	Intent:      "Wait for a release to cool.",
	May:         []effect.Kind{effect.KindAwait, effect.KindAsk},
	RunsPerHour: 9,
	Requires:    []providers.Requirement{providers.Require[cooled.Published]()},
	Key:         func(pr pullRequest) string { return pr.Repo.String() },
	Gather: func(ctx context.Context, cx workflow.Cx, _ pullRequest) (cooled.Age, error) {
		published, err := workflow.Get[cooled.Published](cx)
		if err != nil {
			return 0, err
		}
		age, _, err := cx.Age(ctx, 72*time.Hour, func(context.Context) (cooled.Published, error) { return published, nil })
		return age, err
	},
	Decide: func(age cooled.Age) []effect.Effect {
		if age == cooled.Passed {
			return nil
		}
		return []effect.Effect{effect.Await{Until: "the release to cool"}}
	},
})

// longAgo is a publish time long past any cooldown.
var longAgo = cooled.PublishedAt(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))

// repaired is what a repair gathered: the pull request, and the fixer's patch.
type repaired struct {
	PR    pullRequest
	Patch hostile.Of[patch.Patch]
}

func diffOf(p patch.Patch) string { return p.Diff }

// repairBuild asks a fixer to repair a pull request, and pushes the patch only if the policy
// accepts it, to the pull request's own branch.
var repairBuild = workflow.Code[pullRequest](workflow.Coded[pullRequest, repaired]{
	Name:        "repair-build",
	Intent:      "Repair a broken update.",
	May:         []effect.Kind{effect.KindPush, effect.KindAsk},
	RunsPerHour: 9,
	Agents:      []agent.Info{repairer.Info()},
	Key:         func(pr pullRequest) string { return pr.Repo.String() },
	Gather: func(ctx context.Context, cx workflow.Cx, pr pullRequest) (repaired, error) {
		age, proof, err := cx.Age(ctx, 24*time.Hour, func(context.Context) (cooled.Published, error) { return longAgo, nil })
		if err != nil {
			return repaired{}, err
		}
		if age != cooled.Passed {
			return repaired{}, workflow.Stop("not cooled")
		}
		fix, err := workflow.Repair[patch.Patch](ctx, cx, repairer, agent.Vars{"repo": pr.Repo}, pr.Title, proof)
		return repaired{PR: pr, Patch: fix}, err
	},
	Decide: func(r repaired) []effect.Effect {
		checked, err := patch.DefaultPolicy().Check(r.Patch, r.PR.Head)
		if err != nil {
			return []effect.Effect{effect.Ask{
				Question: hostile.Titlef("A repair was refused by the patch policy"),
				Target:   r.PR.Repo,
				Body:     hostile.Map(r.Patch, diffOf),
			}}
		}
		return []effect.Effect{effect.Push{
			Repo:    r.PR.Repo,
			Pull:    r.PR.Number,
			Branch:  r.PR.Branch,
			Message: "fix: repair the build after this update",
			Patch:   checked,
		}}
	},
})

// attemptCap is how many repairs repairPerRepo pushes before it asks a person.
const attemptCap = 3

// repairLoop is what repairPerRepo gathered.
type repairLoop struct {
	Suite    checkSuite
	Attempts workflow.Attempts
	Patch    *hostile.Of[patch.Patch]
}

// repairPerRepo repairs red CI, keyed per repository so one key spans a whole repair loop.
// After attemptCap pushes it asks a person, once.
var repairPerRepo = workflow.Code[checkSuite](workflow.Coded[checkSuite, repairLoop]{
	Name:        "repair-per-repo",
	Intent:      "Repair a red build.",
	May:         []effect.Kind{effect.KindPush, effect.KindAsk},
	RunsPerHour: 1000,
	Agents:      []agent.Info{repairer.Info()},
	Key:         func(e checkSuite) string { return e.Repo.String() },
	Gather: func(ctx context.Context, cx workflow.Cx, e checkSuite) (repairLoop, error) {
		attempts, err := cx.Attempts(ctx)
		if err != nil || attempts.Used >= attemptCap {
			return repairLoop{Suite: e, Attempts: attempts}, err
		}
		_, proof, err := cx.Age(ctx, 0, func(context.Context) (cooled.Published, error) { return longAgo, nil })
		if err != nil {
			return repairLoop{}, err
		}
		fix, err := workflow.Repair[patch.Patch](ctx, cx, repairer, agent.Vars{"repo": e.Repo}, e.Suite, proof)
		return repairLoop{Suite: e, Attempts: attempts, Patch: &fix}, err
	},
	Decide: func(r repairLoop) []effect.Effect {
		switch {
		case r.Patch != nil:
			checked, err := patch.DefaultPolicy().Check(*r.Patch, r.Suite.Head)
			if err != nil {
				return nil
			}
			return []effect.Effect{effect.Push{
				Repo: r.Suite.Repo, Pull: 1, Branch: hostile.MustParseSlug("renovate/tar-7.x"),
				Message: "fix: repair the build", Patch: checked,
			}}
		case r.Attempts.Asked:
			return nil
		default:
			return []effect.Effect{effect.Ask{
				Question: hostile.Titlef("Repairs did not fix this build"),
				Target:   r.Suite.Repo,
				Body:     hostile.New(""),
			}}
		}
	},
})

// aRepair is a fixer's answer that the default policy accepts.
func aRepair() string {
	return `{"changes": [{"Write": {"path": "src/main.rs", "content": "eA==", "executable": false}}], "diff": "the diff"}`
}

// notesStatus notes a status and waits.
var notesStatus = workflow.Code[commitStatus](workflow.Coded[commitStatus, struct{}]{
	Name:        "notes-status",
	Intent:      "Note a status.",
	May:         []effect.Kind{effect.KindAwait},
	RunsPerHour: 1000,
	Key:         func(e commitStatus) string { return e.Repo.String() },
	Gather:      func(context.Context, workflow.Cx, commitStatus) (struct{}, error) { return struct{}{}, nil },
	Decide:      func(struct{}) []effect.Effect { return []effect.Effect{effect.Await{Until: "nothing"}} },
})

// holdsThenDecides holds a pull request's status while it works, then attests and asks.
var holdsThenDecides = workflow.Code[pullRequest](workflow.Coded[pullRequest, pullRequest]{
	Name:        "holds-then-decides",
	Intent:      "Hold a pull request while it is checked.",
	May:         []effect.Kind{effect.KindAttest, effect.KindAsk},
	RunsPerHour: 100,
	Key:         func(pr pullRequest) string { return pr.Repo.String() },
	Gather: func(ctx context.Context, cx workflow.Cx, pr pullRequest) (pullRequest, error) {
		return pr, cx.Hold(ctx, "almanac/review", pr.Repo, pr.Head)
	},
	Decide: func(pr pullRequest) []effect.Effect {
		return []effect.Effect{
			effect.Attest{Context: "almanac/review", State: effect.Success, Repo: pr.Repo, Sha: pr.Head},
			effect.Ask{Question: hostile.Titlef("Was this review right?"), Target: pr.Repo, Body: pr.Title},
		}
	},
})

// readsWithAnAgent asks the reviewer about red CI.
var readsWithAnAgent = workflow.Code[checkSuite](workflow.Coded[checkSuite, struct{}]{
	Name:        "reads-with-an-agent",
	Intent:      "Read why CI is red.",
	May:         []effect.Kind{effect.KindAwait},
	RunsPerHour: 100,
	Agents:      []agent.Info{reviewer.Info()},
	Key:         func(e checkSuite) string { return e.Repo.String() },
	Gather: func(ctx context.Context, cx workflow.Cx, e checkSuite) (struct{}, error) {
		_, err := workflow.Read[review](ctx, cx, reviewer, agent.Vars{"repo": e.Repo}, e.Suite)
		return struct{}{}, err
	},
	Decide: func(struct{}) []effect.Effect { return []effect.Effect{effect.Await{Until: "a person"}} },
})

// writesDirectly reaches for the forge in gather, where only effects should write.
var writesDirectly = workflow.Code[commitStatus](workflow.Coded[commitStatus, struct{}]{
	Name:        "writes-directly",
	Intent:      "Post a status without deciding to.",
	May:         []effect.Kind{effect.KindAttest},
	RunsPerHour: 100,
	Requires:    []providers.Requirement{providers.Require[effect.Forge]()},
	Key:         func(e commitStatus) string { return e.Repo.String() },
	Gather: func(ctx context.Context, cx workflow.Cx, e commitStatus) (struct{}, error) {
		forge, err := workflow.Get[effect.Forge](cx)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, forge.Attest(ctx, "direct", effect.Attest{Context: "almanac/direct", State: effect.Success, Repo: e.Repo, Sha: e.Sha})
	},
	Decide: func(struct{}) []effect.Effect { return nil },
})

// fleet is the repositories and Renovate pull requests a scan can see, as a forge's REST API
// lists them.
type fleet struct {
	mu    sync.Mutex
	repos []hostile.Slug
	pulls map[hostile.Slug][]listed
	gate  chan struct{}
	lists int
}

// listed is a pull request as the REST API lists it.
type listed struct {
	Number uint64
	Branch string
	Head   string
	Labels []string
}

// newFleet is a fleet of one repository, owner/repo, with pulls labelled pull requests.
func newFleet(pulls int) *fleet {
	f := &fleet{pulls: map[hostile.Slug][]listed{}}
	for i := range pulls {
		f.add("owner/repo", listed{Branch: fmt.Sprintf("renovate/p%d", i), Head: head, Labels: []string{"dependencies"}})
	}
	return f
}

func (f *fleet) add(repo string, pull listed) {
	f.mu.Lock()
	defer f.mu.Unlock()
	slug := hostile.MustParseSlug(repo)
	if !slices.Contains(f.repos, slug) {
		f.repos = append(f.repos, slug)
	}
	pull.Number = uint64(len(f.pulls[slug]) + 1)
	f.pulls[slug] = append(f.pulls[slug], pull)
}

// moveBranch puts a new commit at the head of a pull request.
func (f *fleet) moveBranch(repo string, number uint64, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls[hostile.MustParseSlug(repo)][number-1].Head = sha
}

// gated makes each listing wait until release lets one through.
func (f *fleet) gated() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = make(chan struct{})
}

func (f *fleet) release() { f.gate <- struct{}{} }

func (f *fleet) listings() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists
}

// repositories lists what the scan can see.
func (f *fleet) repositories(ctx context.Context) ([]hostile.Slug, error) {
	f.mu.Lock()
	f.lists++
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.repos), nil
}

// rest is a pull request as the REST API returns it.
func (f *fleet) rest(repo hostile.Slug, pull listed) json.RawMessage {
	labels := make([]any, len(pull.Labels))
	for i, name := range pull.Labels {
		labels[i] = map[string]any{"name": name}
	}
	return mustJSON(map[string]any{
		"number": pull.Number,
		"title":  "Update",
		"body":   injection,
		"base":   map[string]any{"repo": map[string]any{"full_name": repo.String()}},
		"head":   map[string]any{"ref": pull.Branch, "sha": pull.Head},
		"labels": labels,
		"user":   map[string]any{"login": renovateBot},
	})
}

// webhook is the delivery a forge would send for a pull request being opened.
func (f *fleet) webhook(repo string, number uint64) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	pull := f.pulls[hostile.MustParseSlug(repo)][number-1]
	labels := make([]any, len(pull.Labels))
	for i, name := range pull.Labels {
		labels[i] = map[string]any{"name": name}
	}
	return mustJSON(map[string]any{
		"action":     "opened",
		"number":     pull.Number,
		"repository": map[string]any{"full_name": repo},
		"sender":     map[string]any{"login": renovateBot},
		"pull_request": map[string]any{
			"title":  "Update",
			"body":   injection,
			"head":   map[string]any{"ref": pull.Branch, "sha": pull.Head},
			"labels": labels,
		},
	})
}

// scan lists every pull request in the repositories the scanner narrows to.
func (f *fleet) scan(ctx context.Context, scanner *workflow.Scanner) ([]workflow.Found, error) {
	visible, err := f.repositories(ctx)
	if err != nil {
		return nil, err
	}
	repos, err := scanner.Narrow(visible)
	if err != nil {
		return nil, err
	}
	var found []workflow.Found
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, repo := range repos {
		for _, pull := range f.pulls[repo] {
			event, err := workflow.ScannedPull[pullRequest](repo, pull.Number, hostile.MustParseSha(pull.Head), f.rest(repo, pull))
			if err != nil {
				return nil, err
			}
			found = append(found, event)
		}
	}
	return found, nil
}

func isLabelled(labels []string) bool { return slices.Contains(labels, "dependencies") }

// watcher notes whether a pull request is labelled, and scans for every open one.
func watcher(f *fleet) workflow.Registered {
	return workflow.Code[pullRequest](workflow.Coded[pullRequest, bool]{
		Name:        "watcher",
		Intent:      "Note what a pull request looks like.",
		May:         []effect.Kind{effect.KindAwait, effect.KindAsk},
		RunsPerHour: 100,
		Key:         func(pr pullRequest) string { return pr.Repo.String() + ":" + pr.Branch.String() },
		Gather: func(_ context.Context, _ workflow.Cx, pr pullRequest) (bool, error) {
			return hostile.Narrow(pr.Labels, isLabelled), nil
		},
		Decide: func(labelled bool) []effect.Effect {
			if labelled {
				return []effect.Effect{effect.Await{Until: "labeled"}}
			}
			return []effect.Effect{effect.Await{Until: "unlabeled"}}
		},
		Scan: f.scan,
	})
}
