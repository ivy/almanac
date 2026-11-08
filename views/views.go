// Package views draws a workflow from its model, so that a person, or a reviewer of a change to
// a workflow, can see what it does without reading its code. Every view is a walk over the
// model and the labels its vocabulary gives each part. None runs the workflow or reads a fact's
// value, so nothing a run reads, hostile or not, can reach a view.
//
// Outcomes lists every way a run can end, with the conditions on the path to it and its
// effects: what the workflow can do to a repository. Diff compares the outcomes of two
// versions. Page draws both, with the flow and where hostile facts go, as one HTML page.
package views

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ivy/almanac/check"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

// Summary is what a workflow can do, worked out from its model and vocabulary alone. It is the
// outcomes view, and the form an old version is kept in for a diff: a model is never read back
// from JSON, so a summary is.
type Summary struct {
	Workflow string `json:"workflow"`
	Intent   string `json:"intent"`
	// Digest names the version summarised, as model.Digest does.
	Digest string `json:"digest"`
	On     Part   `json:"on"`
	// When is the condition an event must meet to start a run, or nil for every event.
	When   *Test         `json:"when,omitempty"`
	Key    []model.Ref   `json:"key"`
	May    []effect.Kind `json:"may"`
	Limits model.Limits  `json:"limits"`
	Scan   *Part         `json:"scan,omitempty"`
	Steps  []Step        `json:"steps"`
	// Endings are every way a run can end, in flow order, one for each of model.Endings.
	Endings []Outcome `json:"endings"`
	// Hostile are the hostile facts a run can hold, and every place each is read.
	Hostile []Hostile `json:"hostile"`
}

// Part is a part of the vocabulary, by name and label.
type Part struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// Step is one step of the flow.
type Step struct {
	ID    string         `json:"id"`
	Kind  model.StepKind `json:"kind"`
	Label string         `json:"label"`
	// Does is what the step does, in the vocabulary's words.
	Does string `json:"does"`
	// Produces is the fact the step records, if any.
	Produces string `json:"produces,omitempty"`
}

// Outcome is one way a run can end.
type Outcome struct {
	Step      string `json:"step"`
	StepLabel string `json:"step_label"`
	// Rule is the rule that matched, or empty for a screen that stops the run.
	Rule  string `json:"rule,omitempty"`
	Label string `json:"label"`
	// When is the ending's own condition. Nil for an otherwise rule, which always matches.
	When *Test `json:"when,omitempty"`
	// Path is every condition a run meets on the way to this ending, in the order it meets
	// them: the workflow's When, each screen it passes, and each earlier rule that did not
	// match.
	Path []Test `json:"path"`
	// Through are the steps a run passes before the ending's step, by ID.
	Through []string `json:"through"`
	// Agents are the agents a run calls on the way, by name.
	Agents []string `json:"agents,omitempty"`
	// Along are effects applied partway, by hold steps the run passes.
	Along []Effect `json:"along,omitempty"`
	// Effects are what the run produces when it ends here.
	Effects []Effect `json:"effects"`
}

// ID names the ending within its workflow: "step/rule", or the step for a screen.
func (o Outcome) ID() string {
	if o.Rule == "" {
		return o.Step
	}
	return o.Step + "/" + o.Rule
}

// Test is a condition on the way to an ending: one that holds, or one that must not.
type Test struct {
	// At is where the condition is tested: "when", a screen's step, or "step/rule".
	At    string `json:"at"`
	Holds bool   `json:"holds"`
	// Text is the condition in the vocabulary's words.
	Text string `json:"text"`
	// Cond is the condition as the model writes it, or nil for a screen.
	Cond *model.Cond `json:"cond,omitempty"`
}

// String is the test as a reader meets it: "if …" or "unless …".
func (t Test) String() string {
	if t.Holds {
		return "if " + t.Text
	}
	if t.Cond != nil && compound(*t.Cond) {
		return "unless (" + t.Text + ")"
	}
	return "unless " + t.Text
}

// Effect is an effect a run produces, in words.
type Effect struct {
	Kind effect.Kind `json:"kind"`
	Text string      `json:"text"`
	// Brief is the effect in a few words, as the flow labels an ending.
	Brief string `json:"brief"`
}

// Outcomes summarises w: every way a run can end, the conditions on the path to each and its
// effects, the steps, and where hostile facts go. A model the checker refuses is not
// summarised, because a view of it would describe a workflow that cannot run.
func Outcomes(v *vocab.Vocabulary, w model.Workflow) (Summary, error) {
	if err := check.Check(v, w); err != nil {
		return Summary{}, err
	}
	digest, err := model.Digest(w)
	if err != nil {
		return Summary{}, err
	}
	n := newNamer(v, w)
	s := Summary{
		Workflow: w.Name,
		Intent:   w.Intent,
		Digest:   digest,
		On:       n.part(w.On),
		Key:      slices.Clone(w.Key),
		May:      slices.Clone(w.May),
		Limits:   w.Limits,
		Hostile:  hostiles(v, w),
	}
	if w.When != nil {
		t := n.test("when", *w.When, true)
		s.When = &t
	}
	if w.Scan != "" {
		scan := n.part(w.Scan)
		s.Scan = &scan
	}
	for i, step := range w.Flow {
		s.Steps = append(s.Steps, n.step(step, i == len(w.Flow)-1))
	}
	s.Endings = n.endings(w)
	return s, nil
}

// namer puts a model's parts into words, from the labels its vocabulary gives them.
type namer struct {
	v *vocab.Vocabulary
	w model.Workflow
	// cooldown is the measure step's minimum age, which every age predicate is measured
	// against.
	cooldown string
}

func newNamer(v *vocab.Vocabulary, w model.Workflow) namer {
	n := namer{v: v, w: w}
	for _, step := range w.Flow {
		if step.Measure != nil {
			n.cooldown = duration(time.Duration(step.Measure.MinAge))
		}
	}
	return n
}

func (n namer) part(name string) Part {
	return Part{Name: name, Label: n.label(name)}
}

// label is the label the vocabulary gives name. A checked model names only registered parts.
func (n namer) label(name string) string {
	if entry, ok := n.v.Lookup(name); ok {
		return entry.Label
	}
	return name
}

// endings follows model.Endings, adding what a run meets on the way to each.
func (n namer) endings(w model.Workflow) []Outcome {
	var (
		out     []Outcome
		path    []Test
		through []string
		agents  []string
		along   []Effect
	)
	if w.When != nil {
		path = append(path, n.test("when", *w.When, true))
	}
	for _, step := range w.Flow {
		reached := func() Outcome {
			return Outcome{
				Step:      step.ID,
				StepLabel: step.Label,
				Path:      slices.Clone(path),
				Through:   slices.Clone(through),
				Agents:    slices.Clone(agents),
				Along:     slices.Clone(along),
				Effects:   []Effect{},
			}
		}
		switch step.Kind {
		case model.Screen:
			o := reached()
			o.Label = step.Label
			stop := Test{At: step.ID, Holds: false, Text: n.screen(*step.Screen)}
			o.When = &stop
			out = append(out, o)
			path = append(path, Test{At: step.ID, Holds: true, Text: stop.Text})
		case model.Decide:
			for _, rule := range step.Decide {
				o := reached()
				o.Rule = rule.ID
				o.Label = rule.Label
				if rule.When != nil {
					t := n.test(step.ID+"/"+rule.ID, *rule.When, true)
					o.When = &t
					path = append(path, n.test(step.ID+"/"+rule.ID, *rule.When, false))
				}
				for _, e := range rule.Then {
					o.Effects = append(o.Effects, n.effect(e))
				}
				out = append(out, o)
			}
		case model.Agent:
			agents = append(agents, step.Agent.Agent)
		case model.Hold:
			along = append(along, n.hold(*step.Hold))
		}
		through = append(through, step.ID)
	}
	return out
}

// test is cond tested at at, holding or not. A leading negation is folded into whether the
// test holds, so a reader meets "unless X" rather than "unless not X".
func (n namer) test(at string, cond model.Cond, holds bool) Test {
	for cond.Not != nil {
		cond, holds = *cond.Not, !holds
	}
	return Test{At: at, Holds: holds, Text: n.cond(cond), Cond: &cond}
}

func (n namer) cond(c model.Cond) string {
	switch {
	case c.Not != nil:
		if compound(*c.Not) {
			return "not " + n.nested(*c.Not)
		}
		return "not (" + n.cond(*c.Not) + ")"
	case len(c.All) > 0:
		return n.join(c.All, " and ")
	case len(c.Any) > 0:
		return n.join(c.Any, " or ")
	}
	text := n.label(c.Pred)
	if c.Is != "" {
		text += ": " + strings.ReplaceAll(c.Is, "_", " ")
	}
	switch c.Pred {
	case vocab.Spent.Name():
		text += fmt.Sprintf(" (%d allowed)", n.w.Limits.Attempts)
	case vocab.Cooled.Name(), vocab.Young.Name():
		text += " (" + n.cooldown + ")"
	}
	return text
}

func (n namer) join(cs []model.Cond, with string) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = n.nested(c)
	}
	return strings.Join(parts, with)
}

// nested is a condition inside another, in parentheses if it is itself a combination.
func (n namer) nested(c model.Cond) string {
	if compound(c) {
		return "(" + n.cond(c) + ")"
	}
	return n.cond(c)
}

// compound reports whether c combines several conditions.
func compound(c model.Cond) bool { return len(c.All)+len(c.Any) > 1 }

func (n namer) screen(s model.ScreenStep) string {
	return fmt.Sprintf("a classifier finds “%s” likely of %s, above %g", s.Question, s.Text, s.Above)
}

func (n namer) step(s model.Step, last bool) Step {
	out := Step{ID: s.ID, Kind: s.Kind, Label: s.Label}
	switch s.Kind {
	case model.Observe:
		out.Does, out.Produces = "reads "+n.label(s.Observe.From), s.Observe.Fact
	case model.Screen:
		out.Does = fmt.Sprintf("asks a classifier “%s” about %s, and stops the run unless it is likely, above %g", s.Screen.Question, s.Screen.Text, s.Screen.Above)
	case model.Hold:
		out.Does = "sets " + strings.TrimPrefix(n.hold(*s.Hold).Text, "set ") + ", now"
	case model.Measure:
		out.Does = fmt.Sprintf("reads %s, and measures it against a cooldown of %s", n.label(s.Measure.From), duration(time.Duration(s.Measure.MinAge)))
		out.Produces = model.AgeFact
	case model.Attempts:
		out.Does = fmt.Sprintf("reads what was tried on the run's key, against a cap of %d", n.w.Limits.Attempts)
		out.Produces = model.AttemptsFact
	case model.CheckPatch:
		out.Does = fmt.Sprintf("checks %s, made against %s, with %s", s.CheckPatch.Patch, s.CheckPatch.Base, n.label(s.CheckPatch.Policy))
		out.Produces = model.CheckedFact
	case model.Agent:
		out.Does = fmt.Sprintf("calls the agent %s, which %s", s.Agent.Agent, n.label(s.Agent.Agent))
		out.Produces = s.Agent.Fact
	case model.Decide:
		out.Does = fmt.Sprintf("tries %d %s in order, and the first that matches ends the run", len(s.Decide), plural(len(s.Decide), "rule", "rules"))
		if !last {
			out.Does += "; if none matches, the run goes on"
		}
	}
	return out
}

func (n namer) hold(h model.HoldStep) Effect {
	return Effect{
		Kind:  effect.KindAttest,
		Text:  fmt.Sprintf("set %s pending on %s in %s", h.Context, h.Sha, h.Repo),
		Brief: h.Context + " pending",
	}
}

func (n namer) effect(e model.Effect) Effect {
	out := Effect{Kind: e.Kind()}
	switch {
	case e.Attest != nil:
		a := e.Attest
		state := strings.ToLower(a.State.String())
		out.Text = fmt.Sprintf("set %s to %s on %s in %s", a.Context, state, a.Sha, a.Repo)
		out.Brief = a.Context + " " + state
	case e.Await != nil:
		out.Text, out.Brief = "wait for "+e.Await.Until, "await"
	case e.File != nil:
		out.Text = fmt.Sprintf("file an issue in %s: “%s”%s", e.File.Target, title(e.File.Title), n.body(e.File.Body))
		out.Brief = "file an issue"
	case e.Ask != nil:
		a := e.Ask
		about := ""
		if a.Pull != "" {
			about = fmt.Sprintf(" about pull request %s", a.Pull)
		}
		out.Text = fmt.Sprintf("ask a person in %s%s: “%s”%s", a.Target, about, title(a.Title), n.body(a.Body))
		out.Brief = "ask a person"
		if a.Once {
			out.Brief += " once"
			if a.ThenAwait != "" {
				out.Text += "; if one was already asked, wait for " + a.ThenAwait
			} else {
				out.Text += "; nothing, if one was already asked"
			}
		}
	case e.Push != nil:
		p := e.Push
		out.Text = fmt.Sprintf("push the checked patch to %s, the head of pull request %s in %s, as “%s”", p.Branch, p.Pull, p.Repo, p.Message)
		if p.Hold != "" {
			out.Text += fmt.Sprintf(", holding %s pending on the new commit", p.Hold)
		}
		out.Brief = "push the patch"
	}
	return out
}

func (n namer) body(b *model.Body) string {
	switch {
	case b == nil:
		return ""
	case b.Text != "":
		return fmt.Sprintf(", saying “%s”", b.Text)
	case b.Map != "":
		return fmt.Sprintf(", showing %s (%s)", n.label(b.Map), b.Showing)
	default:
		return fmt.Sprintf(", showing %s as text", b.Showing)
	}
}

// title is a title's format with each value it takes named in braces, as "{pull.number}".
func title(t model.Title) string {
	format := t.Format.String()
	var out strings.Builder
	next := 0
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			out.WriteByte(format[i])
			continue
		}
		if i+1 < len(format) && format[i+1] == '%' {
			out.WriteByte('%')
			i++
			continue
		}
		// Skip the verb's flags and width, then the verb itself.
		for i+1 < len(format) && strings.IndexByte("+-# 0123456789.", format[i+1]) >= 0 {
			i++
		}
		i++
		if next < len(t.Values) {
			out.WriteString("{" + string(t.Values[next]) + "}")
		}
		next++
	}
	return out.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// duration is d as a person writes it: "72h" rather than "72h0m0s".
func duration(d time.Duration) string {
	switch {
	case d == 0:
		return "no cooldown"
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}
