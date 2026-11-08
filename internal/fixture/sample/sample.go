// Package sample is a small vocabulary for the sample workflows: triaging an application error
// from a source with no published schema, and judging a dependency update from a registry's
// evidence. Its observers read nothing, because no spec here runs a workflow.
package sample

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// Vocabulary holds every part below.
var Vocabulary = vocab.New()

// ---- Error triage -----------------------------------------------------------

// IssueEvent is an application error reported by an error tracker.
type IssueEvent struct {
	IssueID uint64             `json:"issue_id"`
	Repo    hostile.Slug       `json:"repo"`
	Message hostile.Of[string] `json:"message"`
}

func (IssueEvent) Kind() string        { return "sentry.issue" }
func (IssueEvent) AlsoKinds() []string { return nil }

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

var (
	Low  = Confidence{name: "low"}
	High = Confidence{name: "high"}
)

// UnmarshalJSON chooses among the declared confidences.
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
	return errors.New("not a confidence")
}

// Findings is what an investigator concluded.
type Findings struct {
	Confidence Confidence `json:"confidence"`
	Summary    string     `json:"summary"`
}

var (
	ErrorReported = vocab.Event[IssueEvent](Vocabulary, "sentry.issue", "an error tracker reported a new issue")
	Investigator  = vocab.Reader[Findings](Vocabulary, "investigates an application error",
		agent.NewQuarantined("investigator", agent.MustPrompt(agent.ParsePrompt("investigator.md", "Investigate this error."))))
	FindingsConfidence = vocab.Classifier(Vocabulary, "investigator.confidence", "how sure the investigator is", confidenceOf, map[string]Confidence{"low": Low, "high": High})
	Confident          = FindingsConfidence.Is(High)
	Summary            = vocab.Map(Vocabulary, "investigator.summary", "the investigator's summary", summary)
)

func confidenceOf(h hostile.Of[Findings]) Confidence    { return hostile.Classify(h, findingsConfidence) }
func findingsConfidence(f Findings) Confidence          { return f.Confidence }
func summary(h hostile.Of[Findings]) hostile.Of[string] { return hostile.Map(h, summaryOf) }
func summaryOf(f Findings) string                       { return f.Summary }

// ---- Dependency updates -----------------------------------------------------

// OpenedEvent is a pull request opened.
type OpenedEvent struct {
	Repo       hostile.Slug `json:"repo"`
	HeadBranch hostile.Slug `json:"head_branch"`
	Sha        hostile.Sha  `json:"sha"`
}

func (OpenedEvent) Kind() string        { return "github.pull_request.opened" }
func (OpenedEvent) AlsoKinds() []string { return nil }

func (e *OpenedEvent) Parse(p *wire.Payload) {
	e.Repo = wire.Field[hostile.Slug](p, "/repository/full_name")
	e.HeadBranch = wire.Field[hostile.Slug](p, "/pull_request/head/ref")
	e.Sha = wire.Field[hostile.Sha](p, "/pull_request/head/sha")
}

// CIState is how CI stands on a commit.
type CIState int

// The states of CI.
const (
	CIPending CIState = iota + 1
	CIGreen
	CIRed
)

// UpdateEvidence is what the registry and the forge say about an update. Its facts are typed;
// its diff stays hostile.
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

// Registry fetches both releases of a package and compares them.
type Registry interface {
	Evidence(ctx context.Context, opened OpenedEvent) (UpdateEvidence, error)
}

// Review is a reviewer's answer.
type Review struct {
	Verdict Verdict `json:"verdict"`
	Notes   string  `json:"notes"`
}

// Verdict is what a reviewer concluded.
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

var (
	PullRequestOpened = vocab.Event[OpenedEvent](Vocabulary, "github.pull_request_opened", "a pull request was opened")
	RegistryEvidence  = vocab.Observer(Vocabulary, "registry.evidence", "both releases of the updated package, compared", evidence, providers.Require[Registry]())
	TrustChecksPass   = vocab.Predicate(Vocabulary, "registry.trust_checks_pass", "provenance verified, no install script added, and the same publisher", trustChecksPass)
	CIRunning         = vocab.Predicate(Vocabulary, "registry.ci_pending", "CI is still running", ciIs(CIPending))
	CIPassed          = vocab.Predicate(Vocabulary, "registry.ci_green", "CI passed", ciIs(CIGreen))
	Reviewer          = vocab.Reader[Review](Vocabulary, "reviews a dependency update's evidence",
		agent.NewQuarantined("reviewer", agent.MustPrompt(agent.ParsePrompt("reviewer.md", "Review this update to {{ repo }}.", "repo"))))
	ReviewVerdict  = vocab.Classifier(Vocabulary, "reviewer.verdict", "the reviewer's verdict", verdictOf, map[string]Verdict{"no_concerns": NoConcerns, "concerns": Concerns})
	RaisedConcerns = ReviewVerdict.Is(Concerns)
	ReviewNotes    = vocab.Map(Vocabulary, "reviewer.notes", "the reviewer's notes", reviewNotes)
)

func evidence(ctx context.Context, from workflow.Lookup, opened OpenedEvent) (UpdateEvidence, error) {
	registry, err := workflow.Get[Registry](from)
	if err != nil {
		return UpdateEvidence{}, err
	}
	return registry.Evidence(ctx, opened)
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
