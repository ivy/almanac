package views

import (
	"errors"
	"maps"
	"reflect"
	"slices"

	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

// Hostile is a hostile fact, or a hostile field of a clean one, and every place a run reads
// it. The checker allows only the uses UseKind names.
type Hostile struct {
	Ref model.Ref `json:"ref"`
	// Type is the type the hostile value holds.
	Type string `json:"type"`
	// From is the step that produces the fact, or "event" for a field of the event.
	From string `json:"from"`
	// FromLabel is the producing step's label.
	FromLabel string `json:"from_label"`
	Uses      []Use  `json:"uses"`
}

// UseKind is how a run reads a hostile fact.
type UseKind string

// The ways a hostile fact may be read. Every other place in a model takes clean facts only.
const (
	// AsNarrowing is a narrowing predicate, which answers yes or no, or a classifier, which
	// answers one of a closed set.
	AsNarrowing UseKind = "narrowing"
	// AsMap is an effect's body, shown to a person through a map that keeps it hostile.
	AsMap UseKind = "map"
	// AsText is an effect's body, a fact that is already hostile text, shown to a person.
	AsText UseKind = "text"
	// AsAgent is an agent's input.
	AsAgent UseKind = "agent"
	// AsObserver is what an observer reads from outside with.
	AsObserver UseKind = "observer"
	// AsPublishTime is what a measure step reads a publish time with.
	AsPublishTime UseKind = "publish_time"
	// AsScreen is the text a screen's classifier reads.
	AsScreen UseKind = "screen"
	// AsPolicy is the patch a check_patch step checks by policy.
	AsPolicy UseKind = "policy"
)

// Use is one place a run reads a hostile fact.
type Use struct {
	Step string `json:"step"`
	// Rule is the rule whose condition or effect reads it, if any.
	Rule string  `json:"rule,omitempty"`
	As   UseKind `json:"as"`
	// Part is the vocabulary part that reads it, if any.
	Part string `json:"part,omitempty"`
	// Label says what reads it, in the vocabulary's words.
	Label string `json:"label"`
}

// hostiles finds every hostile fact in w and where it is read, in flow order. A fact's
// hostility comes from the Go type its vocabulary part gives it, as the checker finds it.
func hostiles(v *vocab.Vocabulary, w model.Workflow) []Hostile {
	f := flows{v: v, types: map[string]reflect.Type{}, from: map[string]model.Step{}, at: map[model.Ref]int{}}
	f.types[model.EventFact] = f.result(w.On)
	f.types[model.AttemptsFact] = reflect.TypeFor[vocab.Attempts]()
	f.from[model.EventFact] = model.Step{ID: model.EventFact, Label: "The event that started the run"}
	if w.When != nil {
		f.cond(model.Step{ID: "when"}, "", *w.When)
	}
	for _, step := range w.Flow {
		f.step(step)
	}
	return f.out
}

type flows struct {
	v     *vocab.Vocabulary
	types map[string]reflect.Type
	from  map[string]model.Step
	at    map[model.Ref]int
	out   []Hostile
}

func (f *flows) result(name string) reflect.Type {
	if entry, ok := f.v.Lookup(name); ok {
		return entry.Result
	}
	return nil
}

func (f *flows) label(name string) string {
	if entry, ok := f.v.Lookup(name); ok {
		return entry.Label
	}
	return name
}

// produce records the fact a step produces, listing it at once if it is hostile.
func (f *flows) produce(step model.Step, fact string, t reflect.Type) {
	f.types[fact], f.from[fact] = t, step
	f.find(model.Ref(fact))
}

func (f *flows) step(s model.Step) {
	at := s.ID
	switch s.Kind {
	case model.Observe:
		for _, arg := range s.Observe.Args {
			f.use(arg, Use{Step: at, As: AsObserver, Part: s.Observe.From, Label: f.label(s.Observe.From)})
		}
		f.produce(s, s.Observe.Fact, f.result(s.Observe.From))
	case model.Screen:
		f.use(s.Screen.Text, Use{Step: at, As: AsScreen, Label: s.Screen.Question})
	case model.Measure:
		for _, arg := range s.Measure.Args {
			f.use(arg, Use{Step: at, As: AsPublishTime, Part: s.Measure.From, Label: f.label(s.Measure.From)})
		}
		f.produce(s, model.AgeFact, reflect.TypeFor[cooled.Age]())
	case model.CheckPatch:
		f.use(s.CheckPatch.Patch, Use{Step: at, As: AsPolicy, Part: s.CheckPatch.Policy, Label: f.label(s.CheckPatch.Policy)})
		f.produce(s, model.CheckedFact, reflect.TypeFor[vocab.PatchOutcome]())
	case model.Agent:
		for _, name := range slices.Sorted(maps.Keys(s.Agent.Inputs)) {
			f.use(s.Agent.Inputs[name], Use{Step: at, As: AsAgent, Part: s.Agent.Agent, Label: "input “" + name + "” of " + s.Agent.Agent + ", which " + f.label(s.Agent.Agent)})
		}
		f.produce(s, s.Agent.Fact, f.result(s.Agent.Agent))
	case model.Decide:
		for _, rule := range s.Decide {
			if rule.When != nil {
				f.cond(s, rule.ID, *rule.When)
			}
			for _, e := range rule.Then {
				f.effect(s, rule.ID, e)
			}
		}
	}
}

func (f *flows) cond(s model.Step, rule string, c model.Cond) {
	if c.Pred != "" {
		label := f.label(c.Pred)
		if c.Is != "" {
			label += ": " + c.Is
		}
		for _, arg := range c.Args {
			f.use(arg, Use{Step: s.ID, Rule: rule, As: AsNarrowing, Part: c.Pred, Label: label})
		}
	}
	if c.Not != nil {
		f.cond(s, rule, *c.Not)
	}
	for _, each := range slices.Concat(c.All, c.Any) {
		f.cond(s, rule, each)
	}
}

func (f *flows) effect(s model.Step, rule string, e model.Effect) {
	var body *model.Body
	switch {
	case e.Ask != nil:
		body = e.Ask.Body
	case e.File != nil:
		body = e.File.Body
	}
	if body == nil || body.Showing == "" {
		return
	}
	use := Use{Step: s.ID, Rule: rule, As: AsText, Label: "shown as text to a person, in the " + e.Kind().String() + "'s body"}
	if body.Map != "" {
		use = Use{Step: s.ID, Rule: rule, As: AsMap, Part: body.Map, Label: "shown to a person, in the " + e.Kind().String() + "'s body, as " + f.label(body.Map)}
	}
	f.use(body.Showing, use)
}

// use records u against ref, if ref is hostile.
func (f *flows) use(ref model.Ref, u Use) {
	if i, ok := f.find(ref); ok {
		f.out[i].Uses = append(f.out[i].Uses, u)
	}
}

// find is ref's place in the list, adding it the first time it is seen, if ref is hostile.
func (f *flows) find(ref model.Ref) (int, bool) {
	if i, ok := f.at[ref]; ok {
		return i, true
	}
	held, ok := f.hostile(ref)
	if !ok {
		return 0, false
	}
	from := f.from[ref.Fact()]
	f.at[ref] = len(f.out)
	f.out = append(f.out, Hostile{Ref: ref, Type: held, From: from.ID, FromLabel: from.Label, Uses: []Use{}})
	return f.at[ref], true
}

// hostile reports whether ref names a hostile value, and the type it holds.
func (f *flows) hostile(ref model.Ref) (string, bool) {
	t := f.types[ref.Fact()]
	if t == nil {
		return "", false
	}
	field, err := vocab.FieldType(t, ref.Path())
	var inside *vocab.InsideHostile
	if errors.As(err, &inside) {
		return "", true
	}
	if err != nil {
		return "", false
	}
	held, ok := hostile.Held(field)
	if !ok {
		return "", false
	}
	return held.String(), true
}
