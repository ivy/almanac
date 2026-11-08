package claude_test

import (
	"errors"
	"fmt"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/harness/claude"
)

var _ = Describe("The CLI's command line", func() {
	It("removes every built-in tool and skips the operator's settings with an API key", func() {
		config := claude.DefaultConfig(claude.APIKey("key"))
		Expect(claude.Args(config, "THE PROMPT", "")).To(Equal([]string{
			"-p", "--tools", "", "--strict-mcp-config", "--bare",
			"--no-session-persistence", "--verbose",
			"--output-format", "stream-json",
			"--max-turns", "60",
			"--system-prompt", "THE PROMPT",
			"--max-budget-usd", "6",
		}))
	})

	It("replaces --bare with what stands in for it when authenticating with a token", func() {
		config := claude.DefaultConfig(claude.OAuthToken("token"))
		config.Model = "sonnet"
		Expect(claude.Args(config, "P", "CONFIG")).To(Equal([]string{
			"-p", "--tools", "", "--strict-mcp-config", "--setting-sources=", "--disable-slash-commands",
			"--no-session-persistence", "--verbose",
			"--output-format", "stream-json",
			"--max-turns", "60",
			"--system-prompt", "P",
			"--model", "sonnet",
			"--mcp-config", "CONFIG",
			"--allowedTools", "mcp__sandbox__exec", "mcp__sandbox__read_file", "mcp__sandbox__write_file",
			"--max-budget-usd", "6",
		}))
	})

	It("builds the environment from nothing but PATH, HOME and the credential", func() {
		config := claude.DefaultConfig(claude.APIKey("key"))
		Expect(claude.Environ(config, "/h")).To(Equal([]string{
			"PATH=" + os.Getenv("PATH"), "HOME=/h", "ANTHROPIC_API_KEY=key",
		}))
		config = claude.DefaultConfig(claude.OAuthToken("token"))
		config.BaseURL = "http://127.0.0.1:1"
		Expect(claude.Environ(config, "/h")).To(Equal([]string{
			"PATH=" + os.Getenv("PATH"), "HOME=/h",
			"CLAUDE_CODE_OAUTH_TOKEN=token",
			"ENABLE_TOOL_SEARCH=false",
			"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
			"ANTHROPIC_BASE_URL=http://127.0.0.1:1",
		}))
	})
})

var _ = Describe("A harness config", func() {
	It("caps a session's spend at six dollars by default", func() {
		Expect(claude.DefaultConfig(claude.APIKey("k")).MaxBudgetUSD).To(Equal(6.0))
	})

	DescribeTable("refuses a limit left unset",
		func(change func(*claude.Config), says string) {
			config := claude.DefaultConfig(claude.APIKey("key"))
			change(&config)
			_, err := claude.New(config)
			Expect(errors.Is(err, claude.ErrMisconfigured)).To(BeTrue(), "%v", err)
			Expect(err.Error()).To(ContainSubstring(says))
		},
		Entry("no binary", func(c *claude.Config) { c.Binary = "" }, "binary"),
		Entry("no credential", func(c *claude.Config) { c.Credential = claude.Credential{} }, "credential"),
		Entry("no token budget", func(c *claude.Config) { c.TokenBudget = 0 }, "token budget"),
		Entry("no time limit", func(c *claude.Config) { c.Timeout = 0 }, "time limit"),
		Entry("no turn limit", func(c *claude.Config) { c.MaxTurns = 0 }, "turn limit"),
		Entry("no spend limit", func(c *claude.Config) { c.MaxBudgetUSD = 0 }, "spend limit"),
	)

	It("never prints a credential's secret", func() {
		for _, credential := range []claude.Credential{claude.APIKey("sk-secret"), claude.OAuthToken("sk-secret")} {
			config := claude.DefaultConfig(credential)
			for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
				Expect(fmt.Sprintf(format, config)).NotTo(ContainSubstring("sk-secret"))
			}
		}
	})
})

var _ = Describe("An agent's answer", func() {
	DescribeTable("is JSON alone or in a code fence",
		func(text, want string) {
			answer, err := claude.ParseAnswer(text)
			Expect(err).NotTo(HaveOccurred())
			Expect(answer).To(MatchJSON(want))
		},
		Entry("alone", ` {"a": 1} `, `{"a": 1}`),
		Entry("in a json fence", "```json\n{\"a\": 1}\n```", `{"a": 1}`),
		Entry("in a plain fence", "```\n[1]\n```", `[1]`),
	)

	DescribeTable("is refused otherwise",
		func(text string) {
			_, err := claude.ParseAnswer(text)
			Expect(errors.Is(err, claude.ErrNotJSON)).To(BeTrue(), "%v", err)
		},
		Entry("prose", "Looks fine."),
		Entry("JSON after prose", "Here: {\"a\": 1}"),
		Entry("an unclosed fence", "```json\n{\"a\": 1}"),
		Entry("two values", `{} {}`),
	)
})
