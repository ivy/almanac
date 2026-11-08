package agent_test

import (
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
)

// prompts stands for an almanac's embedded prompts/ directory, which is all LoadPrompt can read.
var prompts = fstest.MapFS{"reviewer.md": {Data: []byte("Review {{ repo }}.")}}

// Each entry is a Rust compile-fail case, named as its file is. Rust checked a prompt when the
// workflow compiled; Go checks it when the prompt is parsed, and an almanac declares its
// prompts as package variables with MustPrompt, so a bad one stops the binary as it starts.
var _ = Describe("Prompts, for the Rust compile-fail cases", func() {
	DescribeTable("refuses, when the binary starts,",
		func(load func() (agent.Prompt, error), because string) {
			_, err := load()
			Expect(err).To(MatchError(ContainSubstring(because)))
			Expect(func() { agent.MustPrompt(load()) }).To(Panic())
		},
		Entry("prompt_field_never_used: a declared variable the prompt never uses",
			func() (agent.Prompt, error) { return agent.ParsePrompt("inline", "Review this", "repo") },
			"never uses {{ repo }}"),
		Entry("prompt_malformed_variable: a variable that is not a name",
			func() (agent.Prompt, error) {
				return agent.ParsePrompt("inline", "Review {{ repo-name }}", "repo_name")
			},
			"line 1: {{ repo-name }} is not a variable name"),
		Entry("prompt_missing_file: a prompt file that is not there",
			func() (agent.Prompt, error) { return agent.LoadPrompt(prompts, "does-not-exist.md") },
			"does-not-exist.md cannot be read"),
		Entry("prompt_outside_prompts_dir: a path out of the prompts directory",
			func() (agent.Prompt, error) { return agent.LoadPrompt(prompts, "../Cargo.toml") },
			"../Cargo.toml cannot be read"),
		Entry("prompt_typo_in_variable: a variable spelled unlike its declaration",
			func() (agent.Prompt, error) { return agent.ParsePrompt("inline", "Review {{ rpeo }}", "repo") },
			"uses {{ rpeo }}, which is not declared"),
		Entry("prompt_unclosed_variable: a variable left open",
			func() (agent.Prompt, error) { return agent.ParsePrompt("inline", "line one\nReview {{ repo", "repo") },
			"line 2: unclosed {{"),
		Entry("prompt_variable_has_no_field: a variable that is not declared",
			func() (agent.Prompt, error) { return agent.ParsePrompt("inline", "Review {{ repo }}") },
			"uses {{ repo }}, which is not declared"),
	)

	It("takes a prompt's name from any expression, but reads only the embedded files, before any event (prompt_is_not_a_runtime_value)", func() {
		name := "reviewer" + ".md"
		prompt, err := agent.LoadPrompt(prompts, name, "repo")
		Expect(err).NotTo(HaveOccurred())

		reviewer := agent.NewQuarantined("reviewer", prompt)
		Expect(reviewer.Info().Prompt).To(Equal("reviewer.md"))
		Expect(reviewer.Info().PromptVars).To(Equal([]string{"repo"}))
	})
})
