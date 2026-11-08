package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/workflow"
)

// Ended is where a walk of a model ended, and what it decided there.
type Ended struct {
	// Step is the decide step whose rule matched, or the screen that stopped the run.
	Step string
	// Rule is the rule that matched, or empty when a screen stopped the run.
	Rule    string
	Decided workflow.Decided
}

// Execute implements workflow.Registered by walking the model. A screen that stops the run
// returns its *workflow.Halt.
func (e *Workflow) Execute(ctx context.Context, cx workflow.Cx, payload json.RawMessage) (workflow.Decided, error) {
	ended, err := e.Walk(ctx, cx, payload)
	return ended.Decided, err
}

// Walk runs the model's steps in order against cx, and stops at the first rule that matches
// or the first screen that stops the run. It says where the run ended, so a spec or a view can
// tell two endings with the same effects apart.
func (e *Workflow) Walk(ctx context.Context, cx workflow.Cx, payload json.RawMessage) (Ended, error) {
	event, err := e.plan.event.Parse(payload)
	if err != nil {
		return Ended{}, err
	}
	if _, err := cx.Record(ctx, modelRecord, e.digest); err != nil {
		return Ended{}, e.at("model", err)
	}
	w := &walk{e: e, cx: cx, facts: facts{model.EventFact: event}}
	for _, s := range e.plan.steps {
		ended, done, err := w.step(ctx, s)
		var halt *workflow.Halt
		switch {
		case errors.As(err, &halt):
			return Ended{Step: s.ID}, err
		case err != nil:
			return Ended{}, e.at(fmt.Sprintf("step %q", s.ID), err)
		case !done:
			continue
		}
		if ended.Decided.Links, err = w.subjects(e.model.Links, true); err != nil {
			return Ended{}, e.at("links", err)
		}
		return ended, nil
	}
	return Ended{}, e.at("flow", errors.New("the run passed its last step having decided nothing, which a checked model cannot do: run the model through check.Check"))
}

// modelRecord is where a run records the digest of the model it walked first, so its log names
// the version that decided. A replay walks the model the binary holds now, whatever the
// recording says.
const modelRecord = "model"

// digest is the model's digest, as a run records it.
func (e *Workflow) digest(context.Context) (json.RawMessage, error) {
	return json.Marshal(e.manifest.Digest)
}

// facts are what a run knows, by fact name.
type facts map[string]any

// walk is one run's walk of a model. The proof that a release cooled and the patch the policy
// accepted are kept beside the facts, never in them: neither has a stored form, and a replay
// makes both again from what was recorded.
type walk struct {
	e       *Workflow
	cx      workflow.Cx
	facts   facts
	proof   cooled.Cooled
	checked patch.Checked
}

// step takes one step. It reports whether the run ended there.
func (w *walk) step(ctx context.Context, s step) (Ended, bool, error) {
	var err error
	switch s.Kind {
	case model.Observe:
		err = w.observe(ctx, s)
	case model.Screen:
		err = w.screen(ctx, *s.Screen)
	case model.Hold:
		err = w.hold(ctx, *s.Hold)
	case model.Measure:
		err = w.measure(ctx, s)
	case model.Attempts:
		_, err = w.attempts(ctx)
	case model.CheckPatch:
		err = w.checkPatch(ctx, s)
	case model.Agent:
		err = w.agent(ctx, s)
	case model.Decide:
		return w.decide(ctx, s)
	default:
		err = fmt.Errorf("%q is not a kind of step", s.Kind)
	}
	return Ended{}, false, err
}

func (w *walk) observe(ctx context.Context, s step) error {
	args, err := w.values(ctx, s.Observe.Args)
	if err != nil {
		return err
	}
	from := reach{from: w.cx, part: s.part}
	raw, err := w.cx.Record(ctx, s.Observe.Fact, func(ctx context.Context) (json.RawMessage, error) {
		return s.part.Observe(ctx, from, args)
	})
	if err != nil {
		return err
	}
	value, err := vocab.Decode(s.part.Result, raw)
	if err != nil {
		return err
	}
	w.facts[s.Observe.Fact] = value
	return nil
}

func (w *walk) screen(ctx context.Context, s model.ScreenStep) error {
	text, err := typed[hostile.Of[string]](ctx, w, s.Text)
	if err != nil {
		return err
	}
	return w.cx.Screen(ctx, s.Question, text, s.Above)
}

func (w *walk) hold(ctx context.Context, h model.HoldStep) error {
	repo, err := typed[hostile.Slug](ctx, w, h.Repo)
	if err != nil {
		return err
	}
	sha, err := typed[hostile.Sha](ctx, w, h.Sha)
	if err != nil {
		return err
	}
	return w.cx.Hold(ctx, h.Context, repo, sha)
}

func (w *walk) measure(ctx context.Context, s step) error {
	args, err := w.values(ctx, s.Measure.Args)
	if err != nil {
		return err
	}
	from := reach{from: w.cx, part: s.part}
	age, proof, err := w.cx.Age(ctx, time.Duration(s.Measure.MinAge), func(ctx context.Context) (cooled.Published, error) {
		return s.part.Published(ctx, from, args)
	})
	if err != nil {
		return err
	}
	w.facts[model.AgeFact] = age
	w.proof = proof
	return nil
}

// attempts is the fact "attempts", read through Cx the first time anything needs it.
func (w *walk) attempts(ctx context.Context) (vocab.Attempts, error) {
	if known, ok := w.facts[model.AttemptsFact].(vocab.Attempts); ok {
		return known, nil
	}
	if w.cx == nil {
		return vocab.Attempts{}, errors.New("attempts are read only during a run")
	}
	tried, err := w.cx.Attempts(ctx)
	if err != nil {
		return vocab.Attempts{}, err
	}
	limit := w.e.model.Limits.Attempts
	attempts := vocab.Attempts{Used: tried.Used, Asked: tried.Asked, Spent: limit > 0 && tried.Used >= limit}
	w.facts[model.AttemptsFact] = attempts
	return attempts, nil
}

// checkPatch checks a patch by policy. It is a pure function of facts already recorded, so it
// is not recorded itself.
func (w *walk) checkPatch(ctx context.Context, s step) error {
	fix, err := typed[hostile.Of[patch.Patch]](ctx, w, s.CheckPatch.Patch)
	if err != nil {
		return err
	}
	base, err := typed[hostile.Sha](ctx, w, s.CheckPatch.Base)
	if err != nil {
		return err
	}
	policy, err := s.part.Policy()
	if err != nil {
		return err
	}
	checked, refused := policy.Check(fix, base)
	w.facts[model.CheckedFact] = vocab.OutcomeOf(refused)
	w.checked = checked
	return nil
}

func (w *walk) agent(ctx context.Context, s step) error {
	vars := agent.Vars{}
	for name, ref := range s.Agent.Vars {
		value, err := w.value(ctx, ref)
		if err != nil {
			return err
		}
		vars[name] = value
	}
	input, err := w.input(ctx, *s.Agent)
	if err != nil {
		return err
	}
	call, err := s.part.Call(vars, input, w.proof)
	if err != nil {
		return err
	}
	raw, err := w.cx.Agent(ctx, call)
	if err != nil {
		return err
	}
	answer, err := s.part.Answer(raw)
	if err != nil {
		return fmt.Errorf("agent %s: %w", s.Agent.Agent, err)
	}
	w.facts[s.Agent.Fact] = answer
	return nil
}

// checkoutField is where an agent's input holds the commit its harness checks out.
const checkoutField = "$checkout"

// checkout is a commit for an agent's harness to check out, as it reads one.
type checkout struct {
	Repo hostile.Slug `json:"repo"`
	Sha  hostile.Sha  `json:"sha"`
}

// input is what an agent step gives its agent: an object with each input under its name, and
// the commit to check out under checkoutField, or nil when there is neither. It is a struct
// made for the step, so each value keeps its own type and a worker's input is checked for
// taint field by field.
func (w *walk) input(ctx context.Context, a model.AgentStep) (any, error) {
	names := slices.Sorted(maps.Keys(a.Inputs))
	values := make([]any, 0, len(names)+1)
	for _, name := range names {
		value, err := w.value(ctx, a.Inputs[name])
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if a.Checkout != nil {
		repo, err := typed[hostile.Slug](ctx, w, a.Checkout.Repo)
		if err != nil {
			return nil, err
		}
		sha, err := typed[hostile.Sha](ctx, w, a.Checkout.Sha)
		if err != nil {
			return nil, err
		}
		names = append(names, checkoutField)
		values = append(values, checkout{Repo: repo, Sha: sha})
	}
	if len(names) == 0 {
		return nil, nil
	}
	fields := make([]reflect.StructField, len(names))
	for i, name := range names {
		if values[i] == nil {
			return nil, fmt.Errorf("the agent's input %q has no value", name)
		}
		fields[i] = reflect.StructField{
			Name: "F" + strconv.Itoa(i),
			Type: reflect.TypeOf(values[i]),
			Tag:  reflect.StructTag(`json:"` + name + `"`),
		}
	}
	input := reflect.New(reflect.StructOf(fields)).Elem()
	for i, value := range values {
		input.Field(i).Set(reflect.ValueOf(value))
	}
	return input.Interface(), nil
}

// decide tries the step's rules in order. The first that matches ends the run with its
// effects; if none does, the run goes on.
func (w *walk) decide(ctx context.Context, s step) (Ended, bool, error) {
	for _, r := range s.rules {
		matched := true
		if r.when != nil {
			var err error
			if matched, err = w.holds(ctx, *r.when); err != nil {
				return Ended{}, false, fmt.Errorf("rule %q: %w", r.ID, err)
			}
		}
		if !matched {
			continue
		}
		effects, err := w.fill(ctx, r.then)
		if err != nil {
			return Ended{}, false, fmt.Errorf("rule %q: %w", r.ID, err)
		}
		return Ended{Step: s.ID, Rule: r.ID, Decided: workflow.Decided{Effects: effects}}, true, nil
	}
	return Ended{}, false, nil
}

// holds evaluates a condition. Predicates are pure functions of facts and record nothing, so
// stopping at the first part that settles all or any changes nothing a replay sees.
func (w *walk) holds(ctx context.Context, c cond) (bool, error) {
	switch c.op {
	case opNot:
		held, err := w.holds(ctx, *c.not)
		return !held, err
	case opAll:
		for _, each := range c.all {
			if held, err := w.holds(ctx, each); err != nil || !held {
				return false, err
			}
		}
		return true, nil
	case opAny:
		for _, each := range c.any {
			if held, err := w.holds(ctx, each); err != nil || held {
				return held, err
			}
		}
		return false, nil
	}
	args, err := w.values(ctx, c.args)
	if err != nil {
		return false, err
	}
	return c.pred.Holds(args, c.is)
}

// fill makes effects from a rule's templates.
func (w *walk) fill(ctx context.Context, templates []template) ([]effect.Effect, error) {
	effects := []effect.Effect{}
	for _, t := range templates {
		made, err := w.effect(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.Kind(), err)
		}
		effects = append(effects, made...)
	}
	return effects, nil
}

// effect makes the effect a template describes. An Ask made once makes nothing, or an Await,
// when a person was already asked.
func (w *walk) effect(ctx context.Context, t template) ([]effect.Effect, error) {
	switch {
	case t.Attest != nil:
		repo, err := typed[hostile.Slug](ctx, w, t.Attest.Repo)
		if err != nil {
			return nil, err
		}
		sha, err := typed[hostile.Sha](ctx, w, t.Attest.Sha)
		if err != nil {
			return nil, err
		}
		return []effect.Effect{effect.Attest{Context: t.Attest.Context, State: t.Attest.State, Repo: repo, Sha: sha}}, nil
	case t.Await != nil:
		return []effect.Effect{effect.Await{Until: t.Await.Until}}, nil
	case t.File != nil:
		target, err := typed[hostile.Slug](ctx, w, t.File.Target)
		if err != nil {
			return nil, err
		}
		title, err := w.title(ctx, t.File.Title)
		if err != nil {
			return nil, err
		}
		body, err := w.body(ctx, t.File.Body, t.show)
		if err != nil {
			return nil, err
		}
		return []effect.Effect{effect.File{Title: title, Target: target, Body: body}}, nil
	case t.Ask != nil:
		return w.ask(ctx, *t.Ask, t.show)
	case t.Push != nil:
		return w.push(ctx, *t.Push)
	}
	return nil, errors.New("the template holds no effect")
}

func (w *walk) ask(ctx context.Context, a model.AskEffect, show *vocab.Entry) ([]effect.Effect, error) {
	if a.Once {
		attempts, err := w.attempts(ctx)
		if err != nil {
			return nil, err
		}
		if attempts.Asked {
			if a.ThenAwait == "" {
				return nil, nil
			}
			return []effect.Effect{effect.Await{Until: a.ThenAwait}}, nil
		}
	}
	target, err := typed[hostile.Slug](ctx, w, a.Target)
	if err != nil {
		return nil, err
	}
	var pull uint64
	if a.Pull != "" {
		if pull, err = typed[uint64](ctx, w, a.Pull); err != nil {
			return nil, err
		}
	}
	question, err := w.title(ctx, a.Title)
	if err != nil {
		return nil, err
	}
	body, err := w.body(ctx, a.Body, show)
	if err != nil {
		return nil, err
	}
	return []effect.Effect{effect.Ask{Question: question, Target: target, Pull: pull, Body: body}}, nil
}

// push pushes the patch the policy accepted. The model names it by the fact "checked"; the
// patch itself never was a fact.
func (w *walk) push(ctx context.Context, p model.PushEffect) ([]effect.Effect, error) {
	if w.checked.IsZero() {
		return nil, errors.New("it pushes a patch the policy did not accept: put the push behind a rule that the patch was accepted, or after rules for every other outcome")
	}
	repo, err := typed[hostile.Slug](ctx, w, p.Repo)
	if err != nil {
		return nil, err
	}
	pull, err := typed[uint64](ctx, w, p.Pull)
	if err != nil {
		return nil, err
	}
	branch, err := typed[hostile.Slug](ctx, w, p.Branch)
	if err != nil {
		return nil, err
	}
	return []effect.Effect{effect.Push{Repo: repo, Pull: pull, Branch: branch, Message: p.Message, Hold: p.Hold, Patch: w.checked}}, nil
}

// title fills a title's format with clean facts. Check accepts only clean values in a title,
// so the format never meets one it refuses.
func (w *walk) title(ctx context.Context, t model.Title) (hostile.Title, error) {
	values, err := w.values(ctx, t.Values)
	if err != nil {
		return hostile.Title{}, err
	}
	return t.Format.Fill(values)
}

// body is what an issue or question says: literal text, a hostile fact of text as it is, or a
// hostile fact shown through a map. All of it stays hostile to the tracker.
func (w *walk) body(ctx context.Context, b *model.Body, show *vocab.Entry) (hostile.Of[string], error) {
	switch {
	case b == nil:
		return hostile.New(""), nil
	case b.Showing == "":
		return hostile.New(b.Text), nil
	case show == nil:
		return typed[hostile.Of[string]](ctx, w, b.Showing)
	}
	value, err := w.value(ctx, b.Showing)
	if err != nil {
		return hostile.Of[string]{}, err
	}
	return show.Show(value)
}

// subjects are subjects made from facts. With known, a subject made from a fact the run never
// produced is left out, because the run ended before the step that produces it.
func (w *walk) subjects(subjects []model.Subject, known bool) ([]workflow.Subject, error) {
	var made []workflow.Subject
	for _, s := range subjects {
		if known && slices.ContainsFunc(s.Of, func(ref model.Ref) bool { _, ok := w.facts[ref.Fact()]; return !ok }) {
			continue
		}
		parts := make([]hostile.Slug, len(s.Of))
		for i, ref := range s.Of {
			var err error
			if parts[i], err = typed[hostile.Slug](context.Background(), w, ref); err != nil {
				return nil, err
			}
		}
		switch {
		case s.Kind == model.RepoSubject && len(parts) == 1:
			made = append(made, workflow.RepoSubject(parts[0]))
		case s.Kind == model.PackageSubject && len(parts) == 2:
			made = append(made, workflow.PackageSubject(parts[0], parts[1]))
		default:
			return nil, fmt.Errorf("a %q subject of %d parts is not a subject", s.Kind, len(parts))
		}
	}
	return made, nil
}

// key is the event's key fields as text, joined with ":".
func (e *Workflow) key(known facts) (string, error) {
	w := &walk{e: e, facts: known}
	parts := make([]string, len(e.model.Key))
	for i, ref := range e.model.Key {
		value, err := w.value(context.Background(), ref)
		if err != nil {
			return "", e.at("key", err)
		}
		if parts[i], err = keyText(value); err != nil {
			return "", e.at("key", fmt.Errorf("%s: %w", ref, err))
		}
	}
	return strings.Join(parts, ":"), nil
}

// keyText is a key field's value as text. Check accepts only these types in a key.
func keyText(value any) (string, error) {
	switch v := value.(type) {
	case hostile.Slug:
		return v.String(), nil
	case hostile.Sha:
		return v.String(), nil
	case bool:
		return strconv.FormatBool(v), nil
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64), nil
	}
	return "", fmt.Errorf("a %T has no text form for a key", value)
}

// value is the value ref names: a fact, or a field inside a clean one.
func (w *walk) value(ctx context.Context, ref model.Ref) (any, error) {
	root := ref.Fact()
	fact, ok := w.facts[root]
	if !ok && root == model.AttemptsFact {
		attempts, err := w.attempts(ctx)
		if err != nil {
			return nil, err
		}
		fact, ok = attempts, true
	}
	if !ok {
		return nil, fmt.Errorf("%s is read before any step produced it, which a checked model cannot do: run the model through check.Check", ref)
	}
	return vocab.Field(fact, ref.Path())
}

func (w *walk) values(ctx context.Context, refs []model.Ref) ([]any, error) {
	values := make([]any, len(refs))
	for i, ref := range refs {
		var err error
		if values[i], err = w.value(ctx, ref); err != nil {
			return nil, err
		}
	}
	return values, nil
}

// typed is the value ref names, as a T.
func typed[T any](ctx context.Context, w *walk, ref model.Ref) (T, error) {
	var zero T
	value, err := w.value(ctx, ref)
	if err != nil {
		return zero, err
	}
	t, ok := value.(T)
	if !ok {
		return zero, fmt.Errorf("%s is a %s where a %s goes, which a checked model cannot do: run the model through check.Check", ref, reflect.TypeOf(value), reflect.TypeFor[T]())
	}
	return t, nil
}
