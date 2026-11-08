// Package model is a workflow as data: what triggers it, what it may do, and the steps a run
// takes, each step naming the vocabulary it uses. There is no code in a model. A condition is a
// tree of named predicates, an effect is a template filled from facts, and a run ends at the
// first decide rule that matches.
//
// A model is built by a front end, such as package dsl, refused or accepted by package check
// against a vocabulary, and run by the engine. Because it is data, the flow of a run, every way
// it can end, and the difference between two versions can be drawn without running it.
//
// [Canonical] is the stored form and [Digest] names a version. A model is written as JSON but
// never read back from it: a title's format is made only from a Go constant.
package model

import (
	"strings"
	"time"

	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
)

// Workflow is one workflow: its declarations, then its flow.
type Workflow struct {
	// Name names the workflow in the store, the manifest and every diagnostic.
	Name string `json:"name"`
	// Intent is what the workflow is for, in a sentence.
	Intent string `json:"intent"`
	// On is the event that triggers it, an event in the vocabulary, which says what kinds of
	// delivery it is parsed from.
	On string `json:"on"`
	// When is whether an event deserves a run at all, over the event's clean fields. Nil
	// wants every event.
	When *Cond `json:"when,omitempty"`
	// Key is the event fields whose values, joined with ":", allow one active run.
	Key []Ref `json:"key"`
	// Subjects are what the event is about, from the event.
	Subjects []Subject `json:"subjects,omitempty"`
	// Links are subjects only known once a run has gathered, from its facts.
	Links []Subject `json:"links,omitempty"`
	// May is every kind of effect the workflow may produce. Nothing else is applied.
	May    []effect.Kind `json:"may"`
	Limits Limits        `json:"limits"`
	// Scan is a scan in the vocabulary that finds current subjects, or empty for none.
	Scan string `json:"scan,omitempty"`
	// Facts are every fact a run can hold, in the order the flow first produces them.
	Facts []Fact `json:"facts"`
	// Flow is the steps of a run, in order. The last is a decide with an otherwise rule.
	Flow []Step `json:"flow"`
}

// Limits are the workflow's own budgets. Config can lower them and never raise them.
type Limits struct {
	RunsPerHour int `json:"runs_per_hour"`
	// Attempts is how many patches a key may have within the attempt window before
	// attempts.spent holds. Zero declares no cap, and attempts.spent may not be used.
	Attempts int `json:"attempts,omitempty"`
}

// Subject is a durable link between runs: a repository, or a package in one.
type Subject struct {
	Kind SubjectKind `json:"kind"`
	// Of is the subject's parts: a repository for a repo, a repository and a package for a
	// package.
	Of []Ref `json:"of"`
}

// SubjectKind is what a subject names.
type SubjectKind string

// The kinds of subject.
const (
	RepoSubject    SubjectKind = "repo"
	PackageSubject SubjectKind = "package"
)

// Fact is something a run knows: the event, an observation, an agent's answer, or a built-in
// fact. Type is the Go type the vocabulary gives it, as reflect names it. A hostile fact holds a
// value of Type that only a narrowing predicate, a map, an observer or an agent may read.
type Fact struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Hostile bool   `json:"hostile,omitempty"`
}

// The facts a run has without an observer of the vocabulary's own.
const (
	// EventFact is the event that triggered the run.
	EventFact = "event"
	// AttemptsFact is what was tried on the run's key before, produced by an attempts step,
	// or the first time anything reads it.
	AttemptsFact = "attempts"
	// AgeFact is how old a release is, produced by a measure step.
	AgeFact = "age"
	// CheckedFact is what the patch policy made of a patch, produced by a check_patch step.
	CheckedFact = "checked"
)

// Ref names a fact, or a field inside a clean one: "pull", "pull.head", "event.repo". A field
// is named as the fact's JSON names it.
type Ref string

// Fact is the fact a reference starts from.
func (r Ref) Fact() string {
	fact, _, _ := strings.Cut(string(r), ".")
	return fact
}

// Path is the fields a reference follows inside its fact, if any.
func (r Ref) Path() []string {
	_, rest, found := strings.Cut(string(r), ".")
	if !found {
		return nil
	}
	return strings.Split(rest, ".")
}

// StepKind is what a step does. There are exactly these.
type StepKind string

// The kinds of step.
const (
	// Observe reads from outside through an observer, recorded as a fact.
	Observe StepKind = "observe"
	// Screen asks a classifier a yes-or-no question about hostile text, and ends the run with
	// no effects unless the answer is likely yes.
	Screen StepKind = "screen"
	// Hold sets a pending status now, before the run ends. It is the only effect that applies
	// partway through a run.
	Hold StepKind = "hold"
	// Measure reads a release's publish time and measures its age against a cooldown,
	// producing the fact "age".
	Measure StepKind = "measure"
	// Attempts reads what was tried on the run's key before, producing the fact "attempts".
	Attempts StepKind = "attempts"
	// CheckPatch checks a patch against a patch policy, producing the fact "checked". Only a
	// checked patch can be pushed.
	CheckPatch StepKind = "check_patch"
	// Agent calls an agent from the vocabulary, recording its answer as a fact.
	Agent StepKind = "agent"
	// Decide tries its rules in order. The first that matches ends the run with its effects;
	// if none matches, the run goes on to the next step.
	Decide StepKind = "decide"
)

// Step is one step of a flow. Exactly the field its kind names is set.
type Step struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Kind  StepKind `json:"kind"`

	Observe    *ObserveStep    `json:"observe,omitempty"`
	Screen     *ScreenStep     `json:"screen,omitempty"`
	Hold       *HoldStep       `json:"hold,omitempty"`
	Measure    *MeasureStep    `json:"measure,omitempty"`
	CheckPatch *CheckPatchStep `json:"check_patch,omitempty"`
	Agent      *AgentStep      `json:"agent,omitempty"`
	Decide     []Rule          `json:"decide,omitempty"`
}

// ObserveStep records what an observer reads, from the facts it is given, as Fact.
type ObserveStep struct {
	Fact string `json:"fact"`
	From string `json:"from"`
	Args []Ref  `json:"args"`
}

// ScreenStep asks a classifier Question about Text, which is hostile text, and ends the run
// unless the probability of yes is above Above.
type ScreenStep struct {
	Question string  `json:"question"`
	Text     Ref     `json:"text"`
	Above    float64 `json:"above"`
}

// HoldStep sets the status Context pending on Sha in Repo, now.
type HoldStep struct {
	Context string `json:"context"`
	Repo    Ref    `json:"repo"`
	Sha     Ref    `json:"sha"`
}

// MeasureStep reads a publish time through From, a publish-time source in the vocabulary, and
// measures it against MinAge. It produces the fact "age": passed, young or unknown.
type MeasureStep struct {
	From   string   `json:"from"`
	Args   []Ref    `json:"args"`
	MinAge Duration `json:"min_age"`
}

// Duration is a length of time, written as Go writes one, such as "72h0m0s".
type Duration time.Duration

// MarshalText writes the duration as time.Duration does.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// CheckPatchStep checks the hostile patch Patch, made against Base, with Policy. It produces
// the fact "checked".
type CheckPatchStep struct {
	Patch  Ref    `json:"patch"`
	Base   Ref    `json:"base"`
	Policy string `json:"policy"`
}

// AgentStep calls Agent, an agent in the vocabulary, and records its answer as Fact.
type AgentStep struct {
	Fact  string `json:"fact"`
	Agent string `json:"agent"`
	// Inputs are what the agent reads, by the name it sees each under.
	Inputs map[string]Ref `json:"inputs,omitempty"`
	// Vars fill the agent's prompt. Each is clean.
	Vars map[string]Ref `json:"vars,omitempty"`
	// Checkout is a commit for the harness to check out for the agent, or nil.
	Checkout *Checkout `json:"checkout,omitempty"`
}

// Checkout is a commit in a repository.
type Checkout struct {
	Repo Ref `json:"repo"`
	Sha  Ref `json:"sha"`
}

// Rule is one way a decide can end a run: when When holds, with Then. A rule with no When is
// the otherwise rule, which always matches, so it comes last.
type Rule struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	When  *Cond    `json:"when,omitempty"`
	Then  []Effect `json:"then"`
}

// Otherwise reports whether r always matches.
func (r Rule) Otherwise() bool { return r.When == nil }

// Cond is a condition: a named predicate applied to facts, or not, all or any of other
// conditions. Exactly one of Pred, Not, All and Any is set.
type Cond struct {
	// Pred is a predicate in the vocabulary, applied to Args.
	Pred string `json:"pred,omitempty"`
	Args []Ref  `json:"args,omitempty"`
	// Is is the value a classifier must answer for the condition to hold. Empty for a
	// predicate that answers yes or no.
	Is  string `json:"is,omitempty"`
	Not *Cond  `json:"not,omitempty"`
	All []Cond `json:"all,omitempty"`
	Any []Cond `json:"any,omitempty"`
}

// Effect is a template for one effect, filled from facts when a rule matches. Exactly one
// field is set, named for its kind as effects are stored.
type Effect struct {
	Attest *AttestEffect `json:"Attest,omitempty"`
	Await  *AwaitEffect  `json:"Await,omitempty"`
	File   *FileEffect   `json:"File,omitempty"`
	Ask    *AskEffect    `json:"Ask,omitempty"`
	Push   *PushEffect   `json:"Push,omitempty"`
}

// Kind is the kind of effect e makes, or zero if it holds none.
func (e Effect) Kind() effect.Kind {
	switch {
	case e.Attest != nil:
		return effect.KindAttest
	case e.Await != nil:
		return effect.KindAwait
	case e.File != nil:
		return effect.KindFile
	case e.Ask != nil:
		return effect.KindAsk
	case e.Push != nil:
		return effect.KindPush
	}
	return 0
}

// AttestEffect sets the status Context to State on Sha in Repo.
type AttestEffect struct {
	Context string             `json:"context"`
	State   effect.AttestState `json:"state"`
	Repo    Ref                `json:"repo"`
	Sha     Ref                `json:"sha"`
}

// AwaitEffect waits, for the reason Until.
type AwaitEffect struct {
	Until string `json:"until"`
}

// FileEffect files work in the tracker for Target.
type FileEffect struct {
	Target Ref   `json:"target"`
	Title  Title `json:"title"`
	Body   *Body `json:"body,omitempty"`
}

// AskEffect asks a person, in the tracker for Target, about pull request Pull if it is set.
type AskEffect struct {
	Target Ref   `json:"target"`
	Pull   Ref   `json:"pull,omitempty"`
	Title  Title `json:"title"`
	Body   *Body `json:"body,omitempty"`
	// Once asks only if no person was asked about the key already.
	Once bool `json:"once,omitempty"`
	// ThenAwait is a reason to await instead, when Once holds back the question. Empty
	// produces nothing then.
	ThenAwait string `json:"then_await,omitempty"`
}

// PushEffect pushes the patch a check_patch step accepted, as Checked names it, to Branch, the
// head of pull request Pull in Repo.
type PushEffect struct {
	Checked Ref    `json:"checked"`
	Repo    Ref    `json:"repo"`
	Pull    Ref    `json:"pull"`
	Branch  Ref    `json:"branch"`
	Message string `json:"message"`
	// Hold is a status set pending on the new commit before the branch moves, or empty.
	Hold string `json:"hold,omitempty"`
}

// Title is a title made from a literal format and clean facts.
type Title struct {
	Format hostile.Format `json:"format"`
	Values []Ref          `json:"values,omitempty"`
}

// Body is what an issue or a question says: literal text, or a hostile fact shown through a
// map in the vocabulary, which keeps it hostile all the way to the tracker.
type Body struct {
	Text    string `json:"text,omitempty"`
	Showing Ref    `json:"showing,omitempty"`
	// Map turns the fact into text. Empty shows a fact that is already hostile text.
	Map string `json:"map,omitempty"`
}
