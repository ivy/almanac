package check_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/check"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

// in is a model checked against the fixture's Renovate vocabulary.
func in(w model.Workflow) func() (*vocab.Vocabulary, model.Workflow) {
	return func() (*vocab.Vocabulary, model.Workflow) { return renovate.Vocabulary, w }
}

// Each entry is a Rust compile-fail case, named as its file is, and the model it becomes. The
// checker refuses each one when the binary starts, before any event arrives, and names the
// rule. docs/safety-rules.md says what every case became.
var _ = Describe("The checker, for the Rust compile-fail cases", func() {
	DescribeTable("refuses, when the binary starts,",
		func(build func() (*vocab.Vocabulary, model.Workflow), codes []diag.Code, because string) {
			got, message := refusal(build())
			Expect(got).To(Equal(codes))
			Expect(message).To(ContainSubstring(because))
		},
		Entry("fixer_needs_cooled: a fixer with no measure before it",
			in(fixerFirst()), []diag.Code{diag.ModelFixerBeforeMeasure}, "no earlier step measures the release's age"),
		Entry("fixer_needs_cooled: a fixer a run reaches whatever the age",
			in(fixerUncooled()), []diag.Code{diag.ModelFixerUncooled}, "a release whose age is young or unknown"),
		Entry("fixer_needs_cooled: a fixer a run reaches when nothing dates the release",
			in(fixerPastYoungOnly()), []diag.Code{diag.ModelFixerUncooled}, "a release whose age is unknown"),
		Entry("fixer_output_is_not_clean: a fixer's patch given to a worker",
			fixerOutputIsNotClean, []diag.Code{diag.ModelAgentInputHostile}, "gives the worker agent `counter` the input `patch`"),
		Entry("reader_output_to_worker: a reader's answer given to a worker",
			readerOutputToWorker, []diag.Code{diag.ModelAgentInputHostile}, "the input `review`"),
		Entry("raw_text_to_worker: hostile text given to a worker",
			rawTextToWorker, []diag.Code{diag.ModelAgentInputHostile}, "the input `text`"),
		Entry("key_on_untrusted_text: a key made of hostile text",
			keyOnUntrustedText, []diag.Code{diag.ModelHostileFact}, "uses `event.title` as a key, and it is hostile"),
		Entry("key_unknown_field: a key naming a field the event does not have",
			in(keyUnknownField()), []diag.Code{diag.ModelUnknownName}, "event.nope"),
		Entry("subject_on_untrusted_text: a subject made of hostile text",
			subjectOnUntrustedText, []diag.Code{diag.ModelHostileFact}, "uses `event.title` as part of a subject"),
		Entry("wrong_event_type: a step reading the event as another event's type",
			in(wrongEventType()), []diag.Code{diag.ModelWrongType}, "renovate.BuildEvent"),
		Entry("missing_decide: a flow that never decides",
			in(endsWithObserve()), []diag.Code{diag.ModelNoEnding}, "does not end with a decide"),
		Entry("missing_prompt_field: a prompt variable left unfilled",
			in(missingPromptField()), []diag.Code{diag.ModelPromptVars}, `the prompt's variable "repo" is not filled`),
		Entry("prompt_typo_in_variable: a prompt variable misspelled",
			in(promptTypoInVariable()), []diag.Code{diag.ModelPromptVars, diag.ModelPromptVars}, `"rpeo" is not a variable of the prompt`),
		Entry("prompt_rejects_hostile_text: a prompt variable filled with hostile text",
			in(promptRejectsHostileText()), []diag.Code{diag.ModelPromptVars}, "which is not clean"),
		Entry("prompt_rejects_plain_string: a prompt variable filled with a fact that holds text",
			in(promptFromPull()), []diag.Code{diag.ModelPromptVars}, "which is not clean"),
		Entry("title_from_untrusted: a title filled with hostile text",
			in(titleFromUntrusted()), []diag.Code{diag.ModelTitleNotClean}, "fills a title with `pull.text`"),
		Entry("title_names_a_variable: a title with a verb and no value for it",
			in(titleNamesAVariable()), []diag.Code{diag.ModelDeclaration}, "a title's format has 1 verbs and is given 0 values"),
		Entry("unknown_effect: a kind of effect that does not exist",
			in(unknownEffect()), []diag.Code{diag.ModelDeclaration}, "a kind that is not an effect"),
		Entry("scan_without_attribute: a scan the vocabulary does not hold",
			in(scanWithoutAttribute()), []diag.Code{diag.ModelUnknownName}, "no scan named `renovate.unregistered`"),
		Entry("untrusted_patch_to_push: a push of the agent's patch itself",
			in(untrustedPatchToPush()), []diag.Code{diag.ModelPushUnchecked}, "pushes `patch`"),
	)

	It("accepts a fixer past rules that end every run whose release has not cooled (ok_fixer_with_cooled)", func() {
		Expect(check.Check(renovate.Vocabulary, whole())).To(Succeed())
		Expect(check.Check(renovate.Vocabulary, fixerPastEachAge())).To(Succeed())
		Expect(check.Check(renovate.Vocabulary, fixerPastEitherAge())).To(Succeed())
	})

	It("accepts a push of the patch a check_patch step accepted (ok_checked_patch_to_push)", func() {
		Expect(check.Check(renovate.Vocabulary, whole())).To(Succeed())
	})
})
