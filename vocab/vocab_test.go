package vocab_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/reveal"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

type Build struct {
	Repo   hostile.Slug       `json:"repo"`
	Branch hostile.Slug       `json:"branch"`
	Note   hostile.Of[string] `json:"note"`
	Nested *Inner             `json:"nested"`
	Plain  int
}

type Inner struct {
	Count int `json:"count"`
}

func (Build) Kind() string        { return "demo.build" }
func (Build) AlsoKinds() []string { return []string{"demo.status"} }
func (b *Build) Parse(p *wire.Payload) {
	b.Repo = wire.Field[hostile.Slug](p, "/repo")
	b.Note = p.Text("/note")
}

type Verdict struct {
	hostile.Closed
	name string
}

var (
	Fine = Verdict{name: "fine"}
	Bad  = Verdict{name: "bad"}
)

func (v *Verdict) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	for _, known := range []Verdict{Fine, Bad} {
		if known.name == name {
			*v = known
			return nil
		}
	}
	return errors.New("not a verdict")
}

type Review struct {
	Verdict Verdict `json:"verdict"`
	Notes   string  `json:"notes"`
}

type Count struct {
	N int `json:"n"`
}

type Forge interface{ Name() string }

func onMain(b Build) bool                           { return b.Branch.String() == "main" }
func mentionsFire(h hostile.Of[string]) bool        { return hostile.Narrow(h, hasFire) }
func hasFire(text string) bool                      { return text == "fire" }
func verdictOf(h hostile.Of[Review]) Verdict        { return hostile.Classify(h, reviewVerdict) }
func reviewVerdict(r Review) Verdict                { return r.Verdict }
func notes(h hostile.Of[Review]) hostile.Of[string] { return hostile.Map(h, notesOf) }
func notesOf(r Review) string                       { return r.Notes }

func readNote(_ context.Context, _ workflow.Lookup, b Build) (hostile.Of[string], error) {
	return b.Note, nil
}

func readPlain(context.Context, workflow.Lookup, Build) (string, error) { return "", nil }

func prompt(vars ...string) agent.Prompt {
	text := "Do it."
	if len(vars) > 0 {
		text = "Do it in {{ repo }}."
	}
	return agent.MustPrompt(agent.ParsePrompt("p.md", text, vars...))
}

var _ = Describe("A vocabulary", func() {
	var v *vocab.Vocabulary

	BeforeEach(func() { v = vocab.New() })

	codes := func() []diag.Code {
		var problems diag.Problems
		Expect(errors.As(v.Err(), &problems)).To(BeTrue(), "expected problems")
		return problems.Codes()
	}

	Describe("refuses", func() {
		It("two parts with one name", func() {
			vocab.Predicate(v, "demo.on_main", "on main", onMain)
			vocab.Narrowing(v, "demo.on_main", "mentions fire", mentionsFire)
			Expect(codes()).To(Equal([]diag.Code{diag.VocabDuplicate}))
			Expect(v.Err().Error()).To(ContainSubstring("demo.on_main"))
		})

		It("a part named like a built-in", func() {
			vocab.Predicate(v, "age.young", "young", func(cooled.Age) bool { return true })
			Expect(codes()).To(Equal([]diag.Code{diag.VocabDuplicate}))
		})

		It("a name that is not lowercase words joined by dots", func() {
			vocab.Predicate(v, "OnMain", "on main", onMain)
			vocab.Predicate(v, "nonamespace", "on main", onMain)
			Expect(codes()).To(Equal([]diag.Code{diag.VocabInvalid, diag.VocabInvalid}))
		})

		It("a part with no label", func() {
			vocab.Predicate(v, "demo.on_main", "", onMain)
			Expect(codes()).To(Equal([]diag.Code{diag.VocabInvalid}))
			Expect(v.Err().Error()).To(ContainSubstring("no label"))
		})

		It("an observer whose reading would hold text that is not hostile", func() {
			vocab.Observer(v, "demo.plain", "plain text", readPlain)
			Expect(codes()).To(Equal([]diag.Code{diag.VocabInvalid}))
			Expect(v.Err().Error()).To(ContainSubstring("could carry free text"))
		})

		It("a worker whose answer is not clean", func() {
			vocab.Worker[Review](v, "acts", agent.NewWorker("worker", prompt()))
			Expect(codes()).To(Equal([]diag.Code{diag.VocabInvalid}))
		})

		It("a classifier with no values, or two names for one value", func() {
			vocab.Classifier(v, "demo.none", "nothing", verdictOf, map[string]Verdict{})
			vocab.Classifier(v, "demo.twice", "twice", verdictOf, map[string]Verdict{"fine": Fine, "ok": Fine})
			Expect(codes()).To(Equal([]diag.Code{diag.VocabInvalid, diag.VocabInvalid}))
		})
	})

	It("holds the built-in parts", func() {
		Expect(v.Err()).To(Succeed())
		for _, name := range []string{"age.cooled", "age.young", "age.unknown", "attempts.spent", "attempts.asked", "checked.accepted", "checked.empty", "checked.refused", "patch.default"} {
			entry, ok := v.Lookup(name)
			Expect(ok).To(BeTrue(), name)
			Expect(entry.Label).NotTo(BeEmpty())
		}
	})

	Describe("an entry's signature", func() {
		It("says which parameters are hostile and what a part gives", func() {
			vocab.Observer(v, "demo.note", "the note", readNote, providers.Require[Forge]())
			entry, ok := v.Lookup("demo.note")
			Expect(ok).To(BeTrue())
			Expect(entry.Kind).To(Equal(vocab.ObserverKind))
			Expect(entry.Params).To(Equal([]reflect.Type{reflect.TypeFor[Build]()}))
			Expect(entry.Result).To(Equal(reflect.TypeFor[hostile.Of[string]]()))
			Expect(entry.Needs).To(Equal([]providers.Requirement{providers.Require[Forge]()}))
		})

		It("names an event apart from the kinds of delivery it is parsed from", func() {
			vocab.Event[Build](v, "demo.build_finished", "a build finished")
			entry, _ := v.Lookup("demo.build_finished")
			Expect(entry.Kinds).To(Equal([]string{"demo.build", "demo.status"}))
		})

		It("lists a classifier's values by name", func() {
			verdict := vocab.Classifier(v, "demo.verdict", "the verdict", verdictOf, map[string]Verdict{"fine": Fine, "bad": Bad})
			entry, _ := v.Lookup("demo.verdict")
			Expect(entry.Values).To(Equal([]string{"bad", "fine"}))
			Expect(verdict.Is(Bad).Is()).To(Equal("bad"))
			Expect(verdict.Is(Verdict{}).Is()).To(BeEmpty())
		})

		It("gives a worker's answer as clean, and a reader's as hostile", func() {
			vocab.Worker[Count](v, "counts", agent.NewWorker("counter", prompt()))
			vocab.Reader[Review](v, "reviews", agent.NewQuarantined("reviewer", prompt()))
			worker, _ := v.Lookup("counter")
			reader, _ := v.Lookup("reviewer")
			Expect(worker.Result).To(Equal(reflect.TypeFor[Count]()))
			Expect(reader.Result).To(Equal(reflect.TypeFor[hostile.Of[Review]]()))
			Expect(reader.Agent.Profile).To(Equal("quarantined"))
		})
	})

	Describe("an entry, called as the engine calls it", func() {
		It("parses an event", func() {
			vocab.Event[Build](v, "demo.build_finished", "a build finished")
			entry, _ := v.Lookup("demo.build_finished")
			event, err := entry.Parse(json.RawMessage(`{"repo":"octo/hello","note":"fire"}`))
			Expect(err).NotTo(HaveOccurred())
			Expect(event.(Build).Repo.String()).To(Equal("octo/hello"))
		})

		It("observes, as JSON ready to record, which decodes back with taint", func() {
			vocab.Observer(v, "demo.note", "the note", readNote)
			entry, _ := v.Lookup("demo.note")
			raw, err := entry.Observe(context.Background(), nil, []any{Build{Note: hostile.New("fire")}})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(Equal(`{"$untrusted":"fire"}`))
			value, err := vocab.Decode(entry.Result, raw)
			Expect(err).NotTo(HaveOccurred())
			Expect(reveal.Value(value.(hostile.Of[string]))).To(Equal("fire"))
		})

		It("refuses an argument of another type, without calling the part", func() {
			vocab.Observer(v, "demo.note", "the note", readNote)
			entry, _ := v.Lookup("demo.note")
			_, err := entry.Observe(context.Background(), nil, []any{"not a build"})
			Expect(err).To(MatchError(ContainSubstring("takes a vocab_test.Build")))
		})

		It("tests predicates, narrowing predicates and classifiers", func() {
			vocab.Predicate(v, "demo.on_main", "on main", onMain)
			vocab.Narrowing(v, "demo.fire", "mentions fire", mentionsFire)
			vocab.Classifier(v, "demo.verdict", "the verdict", verdictOf, map[string]Verdict{"fine": Fine, "bad": Bad})
			holds := func(name string, arg any, is string) bool {
				entry, _ := v.Lookup(name)
				ok, err := entry.Holds([]any{arg}, is)
				Expect(err).NotTo(HaveOccurred())
				return ok
			}
			Expect(holds("demo.on_main", Build{Branch: hostile.MustParseSlug("main")}, "")).To(BeTrue())
			Expect(holds("demo.fire", hostile.New("fire"), "")).To(BeTrue())
			Expect(holds("demo.fire", hostile.New("water"), "")).To(BeFalse())
			Expect(holds("demo.verdict", hostile.New(Review{Verdict: Bad}), "bad")).To(BeTrue())
			Expect(holds("demo.verdict", hostile.New(Review{Verdict: Bad}), "fine")).To(BeFalse())
		})

		It("shows a hostile fact as hostile text", func() {
			vocab.Map(v, "demo.notes", "the notes", notes)
			entry, _ := v.Lookup("demo.notes")
			shown, err := entry.Show(hostile.New(Review{Notes: "look"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(reveal.Value(shown)).To(Equal("look"))
		})

		It("calls a fixer only with a cooldown proof", func() {
			vocab.Fixer[patch.Patch](v, "fixes", agent.NewFixer("fixer", prompt("repo")))
			entry, _ := v.Lookup("fixer")
			_, err := entry.Call(agent.Vars{"repo": hostile.MustParseSlug("octo/hello")}, nil, cooled.Cooled{})
			Expect(err).To(HaveOccurred())
			proof, _ := cooled.Check(cooled.PublishedAt(time.Unix(0, 0)), time.Hour, time.Unix(7200, 0))
			call, err := entry.Call(agent.Vars{"repo": hostile.MustParseSlug("octo/hello")}, nil, proof)
			Expect(err).NotTo(HaveOccurred())
			Expect(call.Agent()).To(Equal("fixer"))
		})

		It("reads an agent's answer as the fact holds it", func() {
			vocab.Reader[Review](v, "reviews", agent.NewQuarantined("reviewer", prompt()))
			vocab.Worker[Count](v, "counts", agent.NewWorker("counter", prompt()))
			reader, _ := v.Lookup("reviewer")
			answer, err := reader.Answer(hostile.New(json.RawMessage(`{"verdict":"bad","notes":"no"}`)))
			Expect(err).NotTo(HaveOccurred())
			Expect(answer).To(BeAssignableToTypeOf(hostile.Of[Review]{}))
			worker, _ := v.Lookup("counter")
			answer, err = worker.Answer(hostile.New(json.RawMessage(`{"n":3}`)))
			Expect(err).NotTo(HaveOccurred())
			Expect(answer).To(Equal(Count{N: 3}))
		})

		It("refuses a call a part of another kind cannot answer", func() {
			vocab.Predicate(v, "demo.on_main", "on main", onMain)
			entry, _ := v.Lookup("demo.on_main")
			_, err := entry.Observe(context.Background(), nil, nil)
			Expect(err).To(MatchError(ContainSubstring("is a predicate, not a observer")))
			_, err = entry.Policy()
			Expect(err).To(HaveOccurred())
		})

		It("holds the default patch policy", func() {
			entry, _ := v.Lookup(vocab.DefaultPolicy.Name())
			policy, err := entry.Policy()
			Expect(err).NotTo(HaveOccurred())
			Expect(policy).To(Equal(patch.DefaultPolicy()))
		})
	})
})

var _ = Describe("A patch outcome", func() {
	It("names what the policy made of a patch", func() {
		Expect(vocab.OutcomeOf(nil)).To(Equal(vocab.PatchAccepted))
		Expect(vocab.OutcomeOf(patch.Empty)).To(Equal(vocab.PatchEmpty))
		Expect(vocab.OutcomeOf(patch.TooLarge)).To(Equal(vocab.PatchRefused))
	})

	It("is stored by name, and refuses any other", func() {
		text, err := vocab.PatchRefused.MarshalText()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(text)).To(Equal("refused"))
		var outcome vocab.PatchOutcome
		Expect(outcome.UnmarshalText([]byte("accepted"))).To(Succeed())
		Expect(outcome).To(Equal(vocab.PatchAccepted))
		Expect(outcome.UnmarshalText([]byte("maybe"))).NotTo(Succeed())
	})
})

var _ = Describe("A field path", func() {
	It("follows fields as their JSON names them, through pointers", func() {
		t, err := vocab.FieldType(reflect.TypeFor[Build](), []string{"nested", "count"})
		Expect(err).NotTo(HaveOccurred())
		Expect(t).To(Equal(reflect.TypeFor[int]()))
		t, err = vocab.FieldType(reflect.TypeFor[Build](), []string{"Plain"})
		Expect(err).NotTo(HaveOccurred())
		Expect(t).To(Equal(reflect.TypeFor[int]()))
		_, err = vocab.FieldType(reflect.TypeFor[Build](), []string{"Repo"})
		Expect(err).To(HaveOccurred())
	})

	It("reads a field's value, and a nil pointer's field as its zero value", func() {
		value, err := vocab.Field(Build{Repo: hostile.MustParseSlug("octo/hello")}, []string{"repo"})
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal(hostile.MustParseSlug("octo/hello")))
		value, err = vocab.Field(Build{}, []string{"nested", "count"})
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal(0))
	})

	It("cannot look inside a hostile value", func() {
		_, err := vocab.FieldType(reflect.TypeFor[hostile.Of[Review]](), []string{"notes"})
		var inside *vocab.InsideHostile
		Expect(errors.As(err, &inside)).To(BeTrue())
		_, err = vocab.Field(hostile.New(Review{}), []string{"notes"})
		Expect(errors.As(err, &inside)).To(BeTrue())
	})
})
