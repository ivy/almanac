package vocab_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// Titled is an event that keeps its title as plain text.
type Titled struct {
	Repo  hostile.Slug `json:"repo"`
	Title string       `json:"title"`
}

func (Titled) Kind() string           { return "demo.titled" }
func (Titled) AlsoKinds() []string    { return nil }
func (t *Titled) Parse(*wire.Payload) {}

// Noted is an answer that keeps a note as plain text, and Quoted one that keeps it hostile.
type (
	Noted  struct{ Note string }
	Quoted struct{ Note hostile.Of[string] }
)

func readPatch(context.Context, workflow.Lookup, Build) (patch.Patch, error) {
	return patch.Patch{}, nil
}

// Each entry is a Rust compile-fail case, named as its file is, that the vocabulary refuses
// when a binary starts. tools/hostilevet refuses the same Go when the package is built, and
// these are what still holds where the analyzer does not run.
var _ = Describe("The vocabulary, for the Rust compile-fail cases", func() {
	DescribeTable("refuses, when the binary starts,",
		func(register func(*vocab.Vocabulary), because string) {
			v := vocab.New()
			register(v)
			var problems diag.Problems
			Expect(errors.As(v.Err(), &problems)).To(BeTrue(), "the vocabulary accepted it")
			Expect(problems.Codes()).To(Equal([]diag.Code{diag.VocabInvalid}))
			Expect(v.Err().Error()).To(ContainSubstring(because))
		},
		Entry("string_in_observed: an event holding plain text",
			func(v *vocab.Vocabulary) { vocab.Event[Titled](v, "demo.titled", "a titled thing") },
			".Title is a string"),
		Entry("observe_raw_string, and so plain_string_to_worker: an observer reading plain text, which no worker can then be given",
			func(v *vocab.Vocabulary) { vocab.Observer(v, "demo.plain", "plain text", readPlain) },
			"string could carry free text"),
		Entry("patch_is_not_clean: an observer reading a patch as anything but hostile",
			func(v *vocab.Vocabulary) { vocab.Observer(v, "demo.patch", "a patch", readPatch) },
			"patch.Patch could carry free text"),
		Entry("worker_output_must_be_clean: a worker answering plain text",
			func(v *vocab.Vocabulary) { vocab.Worker[string](v, "acts", agent.NewWorker("worker", prompt())) },
			"string could carry free text"),
		Entry("string_in_clean: a worker answering a struct holding plain text",
			func(v *vocab.Vocabulary) { vocab.Worker[Noted](v, "acts", agent.NewWorker("worker", prompt())) },
			".Note is a string"),
		Entry("untrusted_in_clean: a worker answering a struct holding hostile text",
			func(v *vocab.Vocabulary) { vocab.Worker[Quoted](v, "acts", agent.NewWorker("worker", prompt())) },
			".Note is a hostile value"),
		Entry("cannot_read_without_prompt: an agent whose prompt was never parsed",
			func(v *vocab.Vocabulary) {
				vocab.Reader[Review](v, "reads", agent.NewQuarantined("reader", agent.Prompt{}))
			},
			"its prompt was never parsed"),
	)

	It("accepts each agent profile with a parsed prompt, and fixes the prompt when it is declared", func() {
		v := vocab.New()
		vocab.Reader[Review](v, "reads", agent.NewQuarantined("reader", prompt("repo")))
		vocab.Fixer[patch.Patch](v, "repairs", agent.NewFixer("fixer", prompt()))
		vocab.Worker[Count](v, "acts", agent.NewWorker("worker", prompt()))
		Expect(v.Err()).NotTo(HaveOccurred())
		reader, ok := v.Lookup("reader")
		Expect(ok).To(BeTrue())
		Expect(reader.Agent.Prompt).To(Equal("p.md"))
		Expect(reader.Agent.PromptVars).To(Equal([]string{"repo"}))
	})
})
