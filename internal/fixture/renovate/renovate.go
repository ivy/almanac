// Package renovate is a small vocabulary shaped like the Renovate almanac's, for specs of the
// workflow model and the engine. Its parts carry real labels and real signatures. Its observers
// read through a GitHub a spec provides, such as a Fake, and its scans find nothing.
package renovate

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// Vocabulary holds every part below.
var Vocabulary = vocab.New()

// TrustContext is the status Almanac holds a pull request with.
const TrustContext = "almanac/trust"

// MinReleaseAge is how old a release must be before anything reads it.
const MinReleaseAge = 72 * time.Hour

// GitHub is what the observers read through. A spec provides one, such as a [Fake].
type GitHub interface {
	PullForBranch(ctx context.Context, build BuildEvent) (Pull, error)
	FailureLogs(ctx context.Context, pull Pull) (hostile.Of[string], error)
	UpdatedLines(ctx context.Context, pull Pull) (hostile.Of[[]Line], error)
	SecurityPull(ctx context.Context, opened SecurityEvent) (SecurityView, error)
	Advisory(ctx context.Context, text hostile.Of[PullText]) (Advisory, error)
	PackageDiff(ctx context.Context, text hostile.Of[PullText]) (PackageDiff, error)
	NewestRelease(ctx context.Context, text hostile.Of[PullText]) (cooled.Published, error)
}

var github = providers.Require[GitHub]()

// ---- Events -----------------------------------------------------------------

// BuildEvent is CI finishing on a branch.
type BuildEvent struct {
	Repo        hostile.Slug `json:"repo"`
	Branch      hostile.Slug `json:"branch"`
	Sha         hostile.Sha  `json:"sha"`
	Failed      bool         `json:"failed"`
	Finished    bool         `json:"finished"`
	Patched     bool         `json:"patched"`
	Bookkeeping bool         `json:"bookkeeping"`
}

func (BuildEvent) Kind() string        { return "github.check_suite.completed" }
func (BuildEvent) AlsoKinds() []string { return []string{"github.status"} }

func (e *BuildEvent) Parse(p *wire.Payload) {
	e.Repo = wire.Field[hostile.Slug](p, "/repo")
	e.Branch = wire.Field[hostile.Slug](p, "/branch")
	e.Sha = wire.Field[hostile.Sha](p, "/sha")
	e.Failed = wire.Field[bool](p, "/failed")
	e.Finished = wire.Field[bool](p, "/finished")
	e.Patched = wire.Field[bool](p, "/patched")
	e.Bookkeeping = wire.Field[bool](p, "/bookkeeping")
}

// SecurityEvent is a pull request opened or labelled.
type SecurityEvent struct {
	Repo    hostile.Slug `json:"repo"`
	Branch  hostile.Slug `json:"branch"`
	Sha     hostile.Sha  `json:"sha"`
	Number  uint64       `json:"number"`
	Labeled bool         `json:"labeled"`
}

func (SecurityEvent) Kind() string        { return "github.pull_request.opened" }
func (SecurityEvent) AlsoKinds() []string { return []string{"github.pull_request.labeled"} }

func (e *SecurityEvent) Parse(p *wire.Payload) {
	e.Repo = wire.Field[hostile.Slug](p, "/repo")
	e.Branch = wire.Field[hostile.Slug](p, "/branch")
	e.Sha = wire.Field[hostile.Sha](p, "/sha")
	e.Number = wire.Field[uint64](p, "/number")
	e.Labeled = wire.Field[bool](p, "/labeled")
}

var (
	BuildChanged = vocab.Event[BuildEvent](Vocabulary, "renovate.build_changed", "CI finished on a branch")
	SecurityPull = vocab.Event[SecurityEvent](Vocabulary, "renovate.security_pull", "a pull request was opened or labelled")
)

// ---- Facts ------------------------------------------------------------------

// PullText is the parts of a pull request that say what it updates.
type PullText struct {
	Body *string `json:"body"`
}

// Pull is what GitHub says about a Renovate pull request now.
type Pull struct {
	Found        bool                 `json:"found"`
	Repo         hostile.Slug         `json:"repo"`
	Number       uint64               `json:"number"`
	Head         hostile.Sha          `json:"head"`
	Conflicted   bool                 `json:"conflicted"`
	Failed       bool                 `json:"failed"`
	Running      bool                 `json:"running"`
	RepairPassed bool                 `json:"repair_passed"`
	Text         hostile.Of[PullText] `json:"text"`
}

// Line is a line an update added, and the file it is in.
type Line struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

// SecurityView is what GitHub says about a security pull request now.
type SecurityView struct {
	Found      bool                 `json:"found"`
	Head       hostile.Sha          `json:"head"`
	Tested     bool                 `json:"tested"`
	Untestable bool                 `json:"untestable"`
	Text       hostile.Of[PullText] `json:"text"`
}

// Advisory is whether an advisory names the update as its fix.
type Advisory struct {
	NamesTheFix bool `json:"names_the_fix"`
}

// PackageDiff is the difference between the two releases of an updated package.
type PackageDiff struct {
	Readable           bool               `json:"readable"`
	Truncated          bool               `json:"truncated"`
	InstallScriptAdded bool               `json:"install_script_added"`
	Unshown            hostile.Of[string] `json:"unshown"`
	Diff               hostile.Of[string] `json:"diff"`
}

// ---- Observers --------------------------------------------------------------

var (
	PullForBranch = vocab.Observer(Vocabulary, "renovate.pull_for_branch", "Renovate's open pull request on the event's branch", readPull, github)
	FailureLogs   = vocab.Observer(Vocabulary, "renovate.failure_logs", "the tail of each failed job's log", readLogs, github)
	UpdatedLines  = vocab.Observer(Vocabulary, "renovate.updated_lines", "the lines the update added", readLines, github)
	SecurityPR    = vocab.Observer(Vocabulary, "renovate.security_pull_view", "the security pull request as GitHub shows it now", readSecurity, github)
	NamesTheFix   = vocab.Observer(Vocabulary, "renovate.advisory", "whether an advisory names this update as its fix", readAdvisory, github)
	ReleaseDiff   = vocab.Observer(Vocabulary, "renovate.package_diff", "the difference between the old and new release", readDiff, github)
	NewestRelease = vocab.PublishTime(Vocabulary, "renovate.newest_release", "when the newest release the update installs was published", published, github)
)

func readPull(ctx context.Context, from workflow.Lookup, build BuildEvent) (Pull, error) {
	gh, err := workflow.Get[GitHub](from)
	if err != nil {
		return Pull{}, err
	}
	return gh.PullForBranch(ctx, build)
}

func readLogs(ctx context.Context, from workflow.Lookup, pull Pull) (hostile.Of[string], error) {
	gh, err := workflow.Get[GitHub](from)
	if err != nil {
		return hostile.Of[string]{}, err
	}
	return gh.FailureLogs(ctx, pull)
}

func readLines(ctx context.Context, from workflow.Lookup, pull Pull) (hostile.Of[[]Line], error) {
	gh, err := workflow.Get[GitHub](from)
	if err != nil {
		return hostile.Of[[]Line]{}, err
	}
	return gh.UpdatedLines(ctx, pull)
}

func readSecurity(ctx context.Context, from workflow.Lookup, opened SecurityEvent) (SecurityView, error) {
	gh, err := workflow.Get[GitHub](from)
	if err != nil {
		return SecurityView{}, err
	}
	return gh.SecurityPull(ctx, opened)
}

func readAdvisory(ctx context.Context, from workflow.Lookup, text hostile.Of[PullText]) (Advisory, error) {
	gh, err := workflow.Get[GitHub](from)
	if err != nil {
		return Advisory{}, err
	}
	return gh.Advisory(ctx, text)
}

func readDiff(ctx context.Context, from workflow.Lookup, text hostile.Of[PullText]) (PackageDiff, error) {
	gh, err := workflow.Get[GitHub](from)
	if err != nil {
		return PackageDiff{}, err
	}
	return gh.PackageDiff(ctx, text)
}

func published(ctx context.Context, from workflow.Lookup, text hostile.Of[PullText]) (cooled.Published, error) {
	gh, err := workflow.Get[GitHub](from)
	if err != nil {
		return cooled.Published{}, err
	}
	return gh.NewestRelease(ctx, text)
}

// ---- Predicates -------------------------------------------------------------

var (
	NeedsAttention = vocab.Predicate(Vocabulary, "renovate.needs_attention", "CI failed on a Renovate branch, or finished on a commit Almanac patched", needsAttention)
	NotActionable  = vocab.Predicate(Vocabulary, "renovate.not_actionable", "not found, conflicted, or CI still running", notActionable)
	Failing        = vocab.Predicate(Vocabulary, "renovate.failing", "CI failed on the head", failing)
	RepairPassed   = vocab.Predicate(Vocabulary, "renovate.repair_passed", "CI passed on a repair Almanac pushed", repairPassed)
	UndoesUpdate   = vocab.Narrowing2(Vocabulary, "renovate.undoes_update", "the patch takes back a line the update added", undoesUpdate)

	IsSecurity     = vocab.Predicate(Vocabulary, "renovate.is_security", "a Renovate branch for a vulnerability fix", isSecurity)
	Open           = vocab.Predicate(Vocabulary, "renovate.open", "an open pull request by Renovate", isOpen)
	Tested         = vocab.Predicate(Vocabulary, "renovate.tested", "something other than Almanac reported on the head", tested)
	Untestable     = vocab.Predicate(Vocabulary, "renovate.untestable", "nothing will ever test the head", untestable)
	NamesFix       = vocab.Predicate(Vocabulary, "renovate.names_the_fix", "an advisory names this update as its fix", namesFix)
	Readable       = vocab.Predicate(Vocabulary, "renovate.readable", "the package could be read", readable)
	Truncated      = vocab.Predicate(Vocabulary, "renovate.truncated", "the diff is too large to read in full", truncated)
	AddsInstall    = vocab.Predicate(Vocabulary, "renovate.adds_install_script", "the new release adds an install script", addsInstall)
	HidesFiles     = vocab.Narrowing(Vocabulary, "renovate.hides_files", "the diff leaves files out", hidesFiles)
	ReviewVerdict  = vocab.Classifier(Vocabulary, "reviewer.verdict", "the reviewer's verdict", verdictOf, map[string]Verdict{"no_concerns": NoConcerns, "concerns": Concerns})
	RaisedConcerns = ReviewVerdict.Is(Concerns)
)

func needsAttention(e BuildEvent) bool {
	return strings.HasPrefix(e.Branch.String(), "renovate/") && !e.Bookkeeping && (e.Failed || e.Patched && e.Finished)
}

func notActionable(p Pull) bool { return !p.Found || p.Conflicted || p.Running }
func failing(p Pull) bool       { return p.Failed }
func repairPassed(p Pull) bool  { return p.RepairPassed }

func undoesUpdate(p hostile.Of[patch.Patch], added hostile.Of[[]Line]) bool {
	return hostile.Narrow(hostile.Zip(p, added), takesBack)
}

// takesBack reports whether the patch drops a line the update added outside a lockfile, which a
// repair may regenerate.
func takesBack(pair hostile.Pair[patch.Patch, []Line]) bool {
	for _, line := range pair.Second {
		name := path.Base(line.Path)
		lockfile := name == "go.sum" || strings.HasSuffix(name, ".lock") || strings.Contains(name, "-lock.") || strings.Contains(name, ".lock.")
		if !lockfile && !pair.First.Keeps(line.Path, line.Text) {
			return true
		}
	}
	return false
}

func isSecurity(e SecurityEvent) bool {
	branch := e.Branch.String()
	return strings.HasPrefix(branch, "renovate/") && (e.Labeled || strings.HasSuffix(branch, "-vulnerability"))
}

func isOpen(v SecurityView) bool           { return v.Found }
func tested(v SecurityView) bool           { return v.Tested }
func untestable(v SecurityView) bool       { return v.Untestable }
func namesFix(a Advisory) bool             { return a.NamesTheFix }
func readable(d PackageDiff) bool          { return d.Readable }
func truncated(d PackageDiff) bool         { return d.Truncated }
func addsInstall(d PackageDiff) bool       { return d.InstallScriptAdded }
func hidesFiles(h hostile.Of[string]) bool { return hostile.Narrow(h, nonEmpty) }
func nonEmpty(text string) bool            { return text != "" }

// ---- The reviewer's verdict -------------------------------------------------

// Verdict is what a reviewer concluded: the only part of its review that leaves taint.
type Verdict struct {
	hostile.Closed
	name string
}

var (
	NoConcerns = Verdict{name: "no_concerns"}
	Concerns   = Verdict{name: "concerns"}
)

// UnmarshalJSON chooses among the declared verdicts.
func (v *Verdict) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	for _, known := range []Verdict{NoConcerns, Concerns} {
		if known.name == name {
			*v = known
			return nil
		}
	}
	return errors.New("not a verdict")
}

// Review is the reviewer's answer.
type Review struct {
	Verdict Verdict `json:"verdict"`
	Notes   string  `json:"notes"`
}

func verdictOf(h hostile.Of[Review]) Verdict { return hostile.Classify(h, reviewVerdict) }
func reviewVerdict(r Review) Verdict         { return r.Verdict }

// ---- Maps -------------------------------------------------------------------

var (
	PatchDiff    = vocab.Map(Vocabulary, "renovate.patch_diff", "the patch as a diff", patchDiff)
	ClosingWords = vocab.Map(Vocabulary, "renovate.closing_words", "what the agent said when it finished", closingWords)
	ReviewNotes  = vocab.Map(Vocabulary, "reviewer.notes", "the reviewer's notes", reviewNotes)
)

func patchDiff(h hostile.Of[patch.Patch]) hostile.Of[string]    { return hostile.Map(h, diffOf) }
func closingWords(h hostile.Of[patch.Patch]) hostile.Of[string] { return hostile.Map(h, summaryOf) }
func reviewNotes(h hostile.Of[Review]) hostile.Of[string]       { return hostile.Map(h, notesOf) }
func diffOf(p patch.Patch) string                               { return p.Diff }
func notesOf(r Review) string                                   { return r.Notes }

func summaryOf(p patch.Patch) string {
	var answer struct {
		Summary string `json:"summary"`
	}
	if json.Unmarshal(p.Answer, &answer) == nil && answer.Summary != "" {
		return answer.Summary
	}
	return string(p.Answer)
}

// ---- Agents and scans -------------------------------------------------------

var (
	Fixer = vocab.Fixer[patch.Patch](Vocabulary, "repairs a build in a sandbox",
		agent.NewFixer("fixer", agent.MustPrompt(agent.ParsePrompt("fixer.md", "Repair the build in {{ repo }}.", "repo"))))
	Reviewer = vocab.Reader[Review](Vocabulary, "reviews a security update's diff",
		agent.NewQuarantined("reviewer", agent.MustPrompt(agent.ParsePrompt("reviewer.md", "Review this update to {{ repo }}.", "repo"))))

	ScanFailing  = vocab.Scan(Vocabulary, "renovate.failing_pulls", "Renovate pull requests whose CI is red, or held after a repair", scanNothing, github)
	ScanSecurity = vocab.Scan(Vocabulary, "renovate.security_pulls", "security pull requests with no final verdict", scanNothing, github)
)

func scanNothing(context.Context, *workflow.Scanner) ([]workflow.Found, error) { return nil, nil }
