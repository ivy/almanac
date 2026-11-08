package compare

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/workflow"
)

// Action is something one version did, or would do, to a pull request.
type Action struct {
	// Kind is pass, fail or hold (almanac/trust set to success, failure or pending), push (a
	// repair commit), repair or review (a read-only Go run stopped where the fixer or another
	// agent would run), ask (a person asked), wait (nothing until something changes), file (an
	// issue) or status (a status other than almanac/trust).
	Kind string `json:"kind"`
	// Sha is the commit the action is about: a status's commit, the commit a repair is pushed
	// onto, or the commit a run's event named. It is empty where GitHub does not say.
	Sha string `json:"sha,omitempty"`
	// Commit is a Rust repair commit's own SHA.
	Commit string `json:"commit,omitempty"`
	// Run is the Go run that decided it.
	Run string `json:"run,omitempty"`
	// Key is the key a Rust repair commit's trailer, or a Rust question's marker, carries.
	Key string `json:"key,omitempty"`
	// Detail is what a Go question asks, what a wait is for, an issue's title, the agent a run
	// stopped at, or the author a Rust repair commit names when it is Almanac's.
	Detail string `json:"detail,omitempty"`
	// Says is the action in a sentence.
	Says string `json:"says"`
}

// Match is a Go action and the Rust actions that answer it.
type Match struct {
	Go   Action   `json:"go"`
	Rust []Action `json:"rust"`
}

// Verdict is what comparing one pull request's actions found.
type Verdict string

const (
	// Agreed is every Go action matched by a Rust one, and every Rust one by a Go one. Two
	// versions that both did nothing agree.
	Agreed Verdict = "agree"
	// Disagreed is at least one action on one side with no match on the other.
	Disagreed Verdict = "disagree"
	// RustWorking is a pull request whose head Almanac's status holds pending: the Rust
	// version is working on it now, and where its run will leave it is not on GitHub yet.
	// Nothing unmatched on it is counted as a disagreement.
	RustWorking Verdict = "rust-in-progress"
	// NotCompared is a pull request no Go run in the window was about.
	NotCompared Verdict = "not-compared"
)

var verdicts = []Verdict{Agreed, Disagreed, RustWorking, NotCompared}

// PullEffects is one pull request's actions on each side, matched.
type PullEffects struct {
	// Pull is the pull request, as owner/repo#number.
	Pull    string  `json:"pull"`
	Branch  string  `json:"branch"`
	Head    string  `json:"head"`
	Verdict Verdict `json:"verdict"`
	// Trust is what almanac/trust says on the head now: absent, pending, success or failure.
	Trust string `json:"trust"`
	// Runs are the Go runs about the pull request in the window, oldest first.
	Runs    []goRun  `json:"go_runs"`
	Matched []Match  `json:"matched"`
	GoOnly  []Action `json:"go_only"`
	// RustOnly are Rust actions with no Go action to match.
	RustOnly []Action `json:"rust_only"`
	// Waits are what the Go version would wait for. They leave nothing on GitHub to match.
	Waits []Action `json:"waits"`
	// Unseen are Rust actions on commits no Go run in the window was about, so the Go version
	// never had the chance to do the same.
	Unseen []Action `json:"unseen"`
}

// Effects is every open Renovate pull request's actions on each side, compared.
type Effects struct {
	// Since is the start of the window of Go runs compared, or null for every run.
	Since  *time.Time      `json:"since"`
	Totals map[Verdict]int `json:"totals"`
	Pulls  []PullEffects   `json:"pulls"`
}

// Disagrees reports whether any pull request's actions disagree.
func (e Effects) Disagrees() bool { return e.Totals[Disagreed] > 0 }

// EffectSources are what an effects comparison reads.
type EffectSources struct {
	// GitHub is read for the open pull requests and what the Rust version did. Build it with
	// github.ReadOnly: a comparison never writes.
	GitHub *github.Client
	// Store is the Go version's dry-run scope, such as almanac-renovate.dry-run.
	Store store.Store
	// Workflows are the Go version's workflows, which replay runs that recorded no effect.
	Workflows []workflow.Registered
	// Owner and Author are whose repositories to list, and who opens Renovate's pull requests.
	Owner, Author string
	// Since is the start of the window of Go runs to compare. The zero time takes every run.
	Since time.Time
}

// CompareEffects sets the Go version's dry-run decisions beside what the Rust version did, for
// every open Renovate pull request.
func CompareEffects(ctx context.Context, from EffectSources) (Effects, error) {
	runs, err := goDecisions(ctx, from.Store, from.Workflows, from.Since)
	if err != nil {
		return Effects{}, err
	}
	pulls, err := openPulls(ctx, from.GitHub, from.Owner, from.Author)
	if err != nil {
		return Effects{}, err
	}
	out := Effects{Totals: map[Verdict]int{}, Pulls: []PullEffects{}}
	for _, v := range verdicts {
		out.Totals[v] = 0
	}
	if !from.Since.IsZero() {
		since := from.Since.UTC()
		out.Since = &since
	}
	for _, pull := range pulls {
		var about []goRun
		for _, run := range runs {
			if run.repo == pull.repo.String() && run.branch == pull.branch.String() {
				about = append(about, run)
			}
		}
		compared, err := comparePull(ctx, from.GitHub, pull, about)
		if err != nil {
			return Effects{}, err
		}
		out.Totals[compared.Verdict]++
		out.Pulls = append(out.Pulls, compared)
	}
	return out, nil
}

// comparePull matches one pull request's Go actions against what the Rust version did to it.
func comparePull(ctx context.Context, client *github.Client, pull openPull, runs []goRun) (PullEffects, error) {
	rust := &rustSide{client: client, pull: pull, trust: map[string]renovate.Trust{}}
	head, err := rust.trustAt(ctx, pull.head.String())
	if err != nil {
		return PullEffects{}, err
	}
	out := PullEffects{
		Pull: pull.String(), Branch: pull.branch.String(), Head: pull.head.String(), Trust: head.String(),
		Runs: runs, Matched: []Match{}, GoOnly: []Action{}, RustOnly: []Action{}, Waits: []Action{}, Unseen: []Action{},
	}
	if out.Runs == nil {
		out.Runs = []goRun{}
	}

	// The commits the Go version looked at: every run's own, and every commit its effects name.
	var seen []string
	var goActions []Action
	look := func(sha string) {
		if sha != "" && !slices.Contains(seen, sha) {
			seen = append(seen, sha)
		}
	}
	for _, run := range runs {
		look(run.Sha)
		for _, a := range run.actions {
			look(a.Sha)
			if !slices.ContainsFunc(goActions, a.same) {
				goActions = append(goActions, a)
			}
		}
	}

	statuses, err := rust.statuses(ctx, seen)
	if err != nil {
		return PullEffects{}, err
	}
	repairs, err := rust.repairs(ctx)
	if err != nil {
		return PullEffects{}, err
	}
	asks, err := rust.asks(ctx)
	if err != nil {
		return PullEffects{}, err
	}
	var rustActions []Action
	rustActions = append(rustActions, statuses...)
	for _, r := range repairs {
		if r.Sha == "" || slices.Contains(seen, r.Sha) {
			rustActions = append(rustActions, r)
		} else {
			out.Unseen = append(out.Unseen, r)
		}
	}
	if len(runs) == 0 {
		out.Unseen = append(out.Unseen, rustActions...)
		out.Unseen = append(out.Unseen, asks...)
		out.Verdict = NotCompared
		out.say()
		return out, nil
	}
	rustActions = append(rustActions, asks...)

	used := make([]bool, len(rustActions))
	take := func(match func(Action) bool) []Action {
		var taken []Action
		for i, r := range rustActions {
			if !used[i] && match(r) {
				used[i] = true
				taken = append(taken, r)
			}
		}
		return taken
	}
	var stops []int
	for _, g := range goActions {
		var answered []Action
		switch g.Kind {
		case kindWait:
			out.Waits = append(out.Waits, g)
			continue
		case kindPass, kindFail, kindHold:
			answered = take(func(r Action) bool { return r.Kind == g.Kind && r.Sha == g.Sha })
		case kindPush:
			answered = take(func(r Action) bool { return r.Kind == kindPush && (r.Sha == g.Sha || r.Sha == "") })
		case kindRepair:
			// The fixer's work is past what a read-only run can see. Rust's repair commit on
			// this commit answers it, or the failed status and hold a repair that came to
			// nothing leaves.
			answered = take(func(r Action) bool {
				return (r.Kind == kindPush && (r.Sha == g.Sha || r.Sha == "")) ||
					((r.Kind == kindFail || r.Kind == kindHold) && r.Sha == g.Sha)
			})
		case kindReview:
			// The reviewer's verdict is past what a read-only run can see. Rust's final status
			// on this commit answers it, or its hold while it waits for tests.
			answered = take(func(r Action) bool {
				return (r.Kind == kindPass || r.Kind == kindFail || r.Kind == kindHold) && r.Sha == g.Sha
			})
		case kindAsk:
			answered = take(func(r Action) bool { return r.Kind == kindAsk })
		}
		if len(answered) == 0 {
			out.GoOnly = append(out.GoOnly, g)
			continue
		}
		if g.Kind == kindRepair || g.Kind == kindReview {
			stops = append(stops, len(out.Matched))
		}
		out.Matched = append(out.Matched, Match{Go: g, Rust: answered})
	}
	// A question the Rust version asked after its agent ran is past what a read-only run can
	// see, so the Go run that stopped there answers it.
	if len(stops) > 0 {
		last := stops[len(stops)-1]
		out.Matched[last].Rust = append(out.Matched[last].Rust, take(func(r Action) bool { return r.Kind == kindAsk })...)
	}
	for i, r := range rustActions {
		if !used[i] {
			out.RustOnly = append(out.RustOnly, r)
		}
	}

	switch {
	case head == renovate.TrustPending:
		out.Verdict = RustWorking
	case len(out.GoOnly) > 0 || len(out.RustOnly) > 0:
		out.Verdict = Disagreed
	default:
		out.Verdict = Agreed
	}
	out.say()
	return out, nil
}

// same reports whether two actions do the same thing to the same commit, so one counts once.
func (a Action) same(b Action) bool {
	return a.Kind == b.Kind && a.Sha == b.Sha && (a.Kind != kindWait || a.Detail == b.Detail)
}

// say fills in each action's sentence.
func (p *PullEffects) say() {
	goSays := func(actions []Action) {
		for i := range actions {
			actions[i].Says = goSentence(actions[i])
		}
	}
	rustSays := func(actions []Action) {
		for i := range actions {
			actions[i].Says = rustSentence(actions[i])
		}
	}
	for i := range p.Matched {
		p.Matched[i].Go.Says = goSentence(p.Matched[i].Go)
		rustSays(p.Matched[i].Rust)
	}
	goSays(p.GoOnly)
	goSays(p.Waits)
	rustSays(p.RustOnly)
	rustSays(p.Unseen)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func goSentence(a Action) string {
	on := " on " + short(a.Sha)
	switch a.Kind {
	case kindPass:
		return "would pass almanac/trust" + on
	case kindFail:
		return "would fail almanac/trust" + on
	case kindHold:
		return "would hold almanac/trust pending" + on
	case kindPush:
		return "would push a repair onto " + short(a.Sha)
	case kindRepair:
		return "would start a repair" + on
	case kindReview:
		return "would ask the " + a.Detail + " about" + on[3:]
	case kindAsk:
		return "would ask a person: " + a.Detail
	case kindWait:
		return "would wait for " + a.Detail
	case kindFile:
		return "would file an issue: " + a.Detail
	}
	return "would set " + a.Detail + on
}

func rustSentence(a Action) string {
	on := " on " + short(a.Sha)
	switch a.Kind {
	case kindPass:
		return "passed almanac/trust" + on
	case kindFail:
		return "failed almanac/trust" + on
	case kindHold:
		return "left almanac/trust pending" + on
	case kindPush:
		text := "pushed repair " + short(a.Commit)
		if a.Sha != "" {
			text += " onto " + short(a.Sha)
		}
		if a.Detail != "" {
			text += " as " + a.Detail
		}
		return text + ", Almanac-Key " + a.Key
	case kindAsk:
		return "asked a person, almanac:" + a.Key
	}
	return a.Kind
}

// RenderEffects is a comparison as people read it: each pull request's verdict, then each
// action, matched or not.
func RenderEffects(e Effects) string {
	var b strings.Builder
	for _, p := range e.Pulls {
		fmt.Fprintf(&b, "%-16s %s  %s  head %s, almanac/trust %s\n", p.Verdict, p.Pull, p.Branch, short(p.Head), p.Trust)
		for _, run := range p.Runs {
			line := fmt.Sprintf("    go run %s (%s) on %s: %s", run.ID, run.Workflow, short(run.Sha), run.Outcome)
			if run.Reason != "" {
				line += ": " + run.Reason
			}
			fmt.Fprintln(&b, line)
		}
		for _, m := range p.Matched {
			rust := make([]string, len(m.Rust))
			for i, r := range m.Rust {
				rust[i] = r.Says
			}
			fmt.Fprintf(&b, "    both       go %s  ~  rust %s\n", m.Go.Says, strings.Join(rust, "; "))
		}
		for _, a := range p.GoOnly {
			fmt.Fprintf(&b, "    go only    %s\n", a.Says)
		}
		for _, a := range p.RustOnly {
			fmt.Fprintf(&b, "    rust only  %s\n", a.Says)
		}
		for _, a := range p.Waits {
			fmt.Fprintf(&b, "    go waits   %s\n", a.Says)
		}
		for _, a := range p.Unseen {
			fmt.Fprintf(&b, "    unseen     rust %s, on a commit no Go run looked at\n", a.Says)
		}
		if p.Verdict == Agreed && len(p.Matched) == 0 {
			fmt.Fprintln(&b, "    neither version acted")
		}
	}
	window := "every Go run"
	if e.Since != nil {
		window = "Go runs since " + e.Since.Format(time.RFC3339)
	}
	parts := make([]string, len(verdicts))
	for i, v := range verdicts {
		parts[i] = fmt.Sprintf("%d %s", e.Totals[v], v)
	}
	fmt.Fprintf(&b, "%d open Renovate pull requests, against %s: %s\n", len(e.Pulls), window, strings.Join(parts, ", "))
	return b.String()
}
