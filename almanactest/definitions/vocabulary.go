// Package definitions holds the sample workflows, written as models in the DSL over a small
// vocabulary of their own. ErrorTriage files a bug for an application error, from a source
// with no published webhook schema. DependencyUpdates judges a dependency update from a
// package registry's evidence and a reviewer that can only veto. They are what the runtime's
// behaviour is specified against, end to end, and FakeRegistry stands in for the registry.
package definitions

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/engine"
	"github.com/ivy/almanac/github/events"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// Vocabulary holds every part the sample workflows are built from.
var Vocabulary = vocab.New()

// Runnable is m, checked against the vocabulary and run by the engine.
func Runnable(m model.Workflow) (*engine.Workflow, error) { return engine.New(Vocabulary, m) }

// embedded holds the agents' instructions.
//
//go:embed prompts/investigator.md prompts/reviewer.md
var embedded embed.FS

// prompts names each prompt by its file, as the manifest shows it.
var prompts = must(fs.Sub(embedded, "prompts"))

func must(fsys fs.FS, err error) fs.FS {
	if err != nil {
		panic(err)
	}
	return fsys
}

// ---- Error triage -----------------------------------------------------------

// IssueEvent is an application error an error tracker reported. The tracker publishes no
// webhook schema, so the event is written by hand.
type IssueEvent struct {
	IssueID uint64             `json:"issue_id"`
	Repo    hostile.Slug       `json:"repo"`
	Message hostile.Of[string] `json:"message"`
}

// Kind is a new issue in the error tracker.
func (IssueEvent) Kind() string { return "sentry.issue" }

// AlsoKinds is none: an issue arrives one way.
func (IssueEvent) AlsoKinds() []string { return nil }

// Parse reads the issue. Its message is the one part an attacker can write.
func (e *IssueEvent) Parse(p *wire.Payload) {
	e.IssueID = wire.Field[uint64](p, "/issue_id")
	e.Repo = wire.Field[hostile.Slug](p, "/repo")
	e.Message = p.Text("/message")
}

// Confidence is how sure an investigator is of its findings.
type Confidence struct {
	hostile.Closed
	name string
}

// The confidences, as investigator.md asks for them.
var (
	Low  = Confidence{name: "Low"}
	High = Confidence{name: "High"}
)

func (c Confidence) String() string { return c.name }

// UnmarshalJSON chooses among the declared confidences, and refuses anything else.
func (c *Confidence) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	for _, known := range []Confidence{Low, High} {
		if known.name == name {
			*c = known
			return nil
		}
	}
	return errNotAConfidence
}

// MarshalJSON writes the confidence as the investigator gave it.
func (c Confidence) MarshalJSON() ([]byte, error) { return json.Marshal(c.name) }

var errNotAConfidence = errors.New(`a confidence is "High" or "Low"`)

// Findings is what an investigator concluded. Only its confidence leaves taint.
type Findings struct {
	Confidence Confidence `json:"confidence"`
	Summary    string     `json:"summary"`
}

// The events, the investigator, and what a workflow may make of its findings.
var (
	ErrorReported = vocab.Event[IssueEvent](Vocabulary, "sentry.issue",
		"an error tracker reported a new issue")
	Investigator = vocab.Reader[Findings](Vocabulary,
		"reads an application error's report and says how sure it is of the cause",
		agent.NewQuarantined("investigator", agent.MustPrompt(agent.LoadPrompt(prompts, "investigator.md"))))
	FindingsConfidence = vocab.Classifier(Vocabulary, "investigator.confidence",
		"how sure the investigator is of the cause",
		confidenceOf, map[string]Confidence{"low": Low, "high": High})
	Confident = FindingsConfidence.Is(High)
	Summary   = vocab.Map(Vocabulary, "investigator.summary",
		"the investigator's summary",
		summary)
)

func confidenceOf(h hostile.Of[Findings]) Confidence    { return hostile.Classify(h, findingsConfidence) }
func findingsConfidence(f Findings) Confidence          { return f.Confidence }
func summary(h hostile.Of[Findings]) hostile.Of[string] { return hostile.Map(h, summaryOf) }
func summaryOf(f Findings) string                       { return f.Summary }

// ---- Dependency updates -----------------------------------------------------

// CIState is how CI stands on a commit.
type CIState int

// The states of CI.
const (
	CIPending CIState = iota + 1
	CIGreen
	CIRed
)

// UpdateEvidence is what the registry and the forge say about an update. Its facts are
// typed; its diff stays hostile.
type UpdateEvidence struct {
	Repo               hostile.Slug       `json:"repo"`
	Sha                hostile.Sha        `json:"sha"`
	Package            hostile.Slug       `json:"package"`
	InstallScriptAdded bool               `json:"install_script_added"`
	PublisherChanged   bool               `json:"publisher_changed"`
	ProvenanceVerified bool               `json:"provenance_verified"`
	CI                 CIState            `json:"ci"`
	Diff               hostile.Of[string] `json:"diff"`
}

// Registry fetches both releases of the package a pull request updates, and compares them.
type Registry interface {
	Evidence(ctx context.Context, pr events.PullRequestOpened) (UpdateEvidence, error)
}

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

// The event, the registry's evidence and what may be tested of it, and the reviewer.
var (
	PullRequestOpened = vocab.Event[events.PullRequestOpened](Vocabulary, "github.pull_request_opened",
		"a pull request was opened")
	RegistryEvidence = vocab.Observer(Vocabulary, "registry.evidence",
		"both releases of the updated package, compared",
		readEvidence, providers.Require[Registry]())
	TrustChecksPass = vocab.Predicate(Vocabulary, "registry.trust_checks_pass",
		"provenance is verified, no install script was added, and the publisher is the same",
		trustChecksPass)
	CIRunning = vocab.Predicate(Vocabulary, "registry.ci_pending", "CI is still running", ciIs(CIPending))
	CIPassed  = vocab.Predicate(Vocabulary, "registry.ci_green", "CI passed", ciIs(CIGreen))
	Reviewer  = vocab.Reader[Review](Vocabulary,
		"reads the diff between a package's two releases and says whether it raises concerns",
		agent.NewQuarantined("reviewer", agent.MustPrompt(agent.LoadPrompt(prompts, "reviewer.md", "repo"))))
	ReviewVerdict = vocab.Classifier(Vocabulary, "reviewer.verdict",
		"what the reviewer concluded about the update",
		verdictOf, map[string]Verdict{"no_concerns": NoConcerns, "concerns": Concerns})
	RaisedConcerns = ReviewVerdict.Is(Concerns)
	ReviewNotes    = vocab.Map(Vocabulary, "reviewer.notes",
		"the reviewer's notes",
		reviewNotes)
)

func readEvidence(ctx context.Context, from workflow.Lookup, pr events.PullRequestOpened) (UpdateEvidence, error) {
	registry, err := workflow.Get[Registry](from)
	if err != nil {
		return UpdateEvidence{}, err
	}
	return registry.Evidence(ctx, pr)
}

func trustChecksPass(e UpdateEvidence) bool {
	return e.ProvenanceVerified && !e.InstallScriptAdded && !e.PublisherChanged
}

func ciIs(want CIState) func(UpdateEvidence) bool {
	return func(e UpdateEvidence) bool { return e.CI == want }
}

func verdictOf(h hostile.Of[Review]) Verdict              { return hostile.Classify(h, reviewVerdict) }
func reviewVerdict(r Review) Verdict                      { return r.Verdict }
func reviewNotes(h hostile.Of[Review]) hostile.Of[string] { return hostile.Map(h, notesOf) }
func notesOf(r Review) string                             { return r.Notes }
