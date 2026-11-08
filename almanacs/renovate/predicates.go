package renovate

import (
	"encoding/json"
	"errors"
	"path"
	"strings"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/vocab"
)

// The predicates: yes-or-no questions a workflow's conditions ask about its facts.
var (
	NeedsAttention = vocab.Predicate(Vocabulary, "renovate.needs_attention",
		"CI failed on a Renovate branch, or finished on a commit Almanac patched",
		needsAttention)
	NotActionable = vocab.Predicate(Vocabulary, "renovate.not_actionable",
		"Renovate has no open pull request here, it has a conflict, or its CI is still running",
		notActionable)
	Failing = vocab.Predicate(Vocabulary, "renovate.failing",
		"CI failed on the pull request's head",
		failing)
	RepairPassed = vocab.Predicate(Vocabulary, "renovate.repair_passed",
		"CI passed on a repair Almanac pushed and is holding",
		repairPassed)
	UndoesUpdate = vocab.Narrowing2(Vocabulary, "renovate.undoes_update",
		"the patch takes back a line the update added, outside a lockfile",
		undoesUpdate)

	IsSecurity = vocab.Predicate(Vocabulary, "renovate.is_security",
		"a Renovate branch that fixes a vulnerability, by its label or its name",
		isSecurity)
	Open = vocab.Predicate(Vocabulary, "renovate.open",
		"an open pull request by Renovate",
		isOpen)
	Tested = vocab.Predicate(Vocabulary, "renovate.tested",
		"something other than Almanac or a cooldown reported on the head",
		tested)
	Untestable = vocab.Predicate(Vocabulary, "renovate.untestable",
		"nothing is going to test the head: the repository has no workflows, or they stayed quiet for an hour",
		untestable)
	NamesFix = vocab.Predicate(Vocabulary, "renovate.names_the_fix",
		"an advisory names this update as its fix",
		namesFix)
	Readable = vocab.Predicate(Vocabulary, "renovate.readable",
		"every updated package's releases were found and read",
		readable)
	Truncated = vocab.Predicate(Vocabulary, "renovate.truncated",
		"the diff was cut to fit, so no reviewer can read all of it",
		truncated)
	AddsInstall = vocab.Predicate(Vocabulary, "renovate.adds_install_script",
		"a new release adds an install script",
		addsInstall)
	HidesFiles = vocab.Narrowing(Vocabulary, "renovate.hides_files",
		"the update changes files that the diff cannot show",
		hidesFiles)
)

func needsAttention(e BuildEvent) bool {
	renovate := strings.HasPrefix(e.Branch.String(), renovateBranch)
	return renovate && !e.Bookkeeping && (e.Failed || e.Patched && e.Finished)
}

func notActionable(p Pull) bool { return !p.Found || p.Conflicted || p.Running }
func failing(p Pull) bool       { return p.Failed }
func repairPassed(p Pull) bool  { return p.RepairPassed }

// undoesUpdate reports whether a patch takes back what the update changed. Green checks prove
// nothing then, because the pull request no longer updates anything.
func undoesUpdate(p hostile.Of[patch.Patch], added hostile.Of[[]Line]) bool {
	return hostile.Narrow(hostile.Zip(p, added), takesBack)
}

// takesBack reports whether the patch drops a line the update added outside a lockfile, which a
// repair may regenerate: only what declares a version has to survive.
func takesBack(pair hostile.Pair[patch.Patch, []Line]) bool {
	for _, line := range pair.Second {
		if !isLockfile(line.Path) && !pair.First.Keeps(line.Path, line.Line) {
			return true
		}
	}
	return false
}

func isLockfile(file string) bool {
	name := path.Base(file)
	return name == "go.sum" || strings.HasSuffix(name, ".lock") || strings.Contains(name, "-lock.") || strings.Contains(name, ".lock.")
}

// isSecurity is a security pull request, by its label or by Renovate's default branch name,
// because the label is often not configured.
func isSecurity(e SecurityEvent) bool {
	branch := e.Branch.String()
	return strings.HasPrefix(branch, renovateBranch) && (e.Labeled || strings.HasSuffix(branch, vulnerabilitySuffix))
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

// Verdict is what the reviewer concluded: the only part of its review that leaves taint.
type Verdict struct {
	hostile.Closed
	name string
}

// The reviewer's verdicts, as reviewer.md asks for them.
var (
	NoConcerns = Verdict{name: "NoConcerns"}
	Concerns   = Verdict{name: "Concerns"}
)

func (v Verdict) String() string { return v.name }

// UnmarshalJSON chooses among the declared verdicts, and refuses anything else.
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
	return errNotAVerdict
}

// MarshalJSON writes the verdict as the reviewer gave it.
func (v Verdict) MarshalJSON() ([]byte, error) { return json.Marshal(v.name) }

var errNotAVerdict = errors.New(`a verdict is "NoConcerns" or "Concerns"`)

// Review is the reviewer's answer.
type Review struct {
	Verdict Verdict `json:"verdict"`
	Notes   string  `json:"notes"`
}

// The reviewer's verdict, and the condition a workflow tests it with.
var (
	ReviewVerdict = vocab.Classifier(Vocabulary, "reviewer.verdict",
		"what the reviewer concluded about the update",
		verdictOf, map[string]Verdict{"no_concerns": NoConcerns, "concerns": Concerns})
	RaisedConcerns = ReviewVerdict.Is(Concerns)
)

func verdictOf(h hostile.Of[Review]) Verdict { return hostile.Classify(h, reviewVerdict) }
func reviewVerdict(r Review) Verdict         { return r.Verdict }

// The maps: how a hostile fact is shown to a person, as the body of a question.
var (
	PatchDiff = vocab.Map(Vocabulary, "renovate.patch_diff",
		"the patch, as a diff",
		patchDiff)
	ClosingWords = vocab.Map(Vocabulary, "renovate.closing_words",
		"what the fixer said when it finished",
		closingWords)
	ReviewNotes = vocab.Map(Vocabulary, "reviewer.notes",
		"the reviewer's notes",
		reviewNotes)
)

func patchDiff(h hostile.Of[patch.Patch]) hostile.Of[string]    { return hostile.Map(h, diffOf) }
func closingWords(h hostile.Of[patch.Patch]) hostile.Of[string] { return hostile.Map(h, summaryOf) }
func reviewNotes(h hostile.Of[Review]) hostile.Of[string]       { return hostile.Map(h, notesOf) }
func diffOf(p patch.Patch) string                               { return p.Diff }
func notesOf(r Review) string                                   { return r.Notes }

// summaryOf is what an agent said when it finished, for whoever is asked to take over: its
// summary when it gave one as asked, or else its whole answer.
func summaryOf(p patch.Patch) string {
	var answer map[string]json.RawMessage
	said := p.Answer
	if json.Unmarshal(p.Answer, &answer) == nil {
		if summary, ok := answer["summary"]; ok {
			said = summary
		}
	}
	var text string
	if json.Unmarshal(said, &text) == nil {
		return text
	}
	return string(said)
}
