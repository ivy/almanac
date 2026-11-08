package claude_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/harness/claude"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/sandbox"
	"github.com/ivy/almanac/sandbox/fake"
)

// These drive the harness with a shell script in place of the CLI, so they
// need nothing installed. They port crates/claude/tests/harness.rs.
var _ = Describe("The Claude harness, with a scripted CLI", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	readWith := func(ctx context.Context, config claude.Config) (json.RawMessage, error) {
		return read(ctx, newHarness(config), "Review this.", map[string]string{"diff": "x"})
	}

	It("an answer is typed JSON", func(ctx SpecContext) {
		out, err := readWith(ctx, fakeCLI(dir, answers(`{"verdict": "ok"}`)))
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(MatchJSON(`{"verdict": "ok"}`))
	})

	It("output that does not parse is a failure, not a crash", func(ctx SpecContext) {
		_, err := readWith(ctx, fakeCLI(dir, answers("I think this looks fine.")))
		Expect(errors.Is(err, claude.ErrNotJSON)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("not JSON"))
		Expect(err.Error()).NotTo(ContainSubstring("looks fine"), "the model's text stays out of the error")
	})

	It("a session that ends without a result is a failure", func(ctx SpecContext) {
		_, err := readWith(ctx, fakeCLI(dir, "echo not-json-at-all"))
		Expect(errors.Is(err, claude.ErrFailed)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("without a result"))
	})

	It("a session over its token budget is killed and reported", func(ctx SpecContext) {
		event := jsonLine(map[string]any{"type": "assistant", "message": map[string]any{
			"usage": map[string]any{"input_tokens": 60, "output_tokens": 50},
		}})
		config := fakeCLI(dir, "echo '"+event+"'\nsleep 30")
		config.TokenBudget = 100
		started := time.Now()
		_, err := readWith(ctx, config)
		Expect(errors.Is(err, claude.ErrKilled)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("110 tokens of a 100 budget"))
		Expect(time.Since(started)).To(BeNumerically("<", 10*time.Second))
	})

	It("cached tokens count against the budget", func(ctx SpecContext) {
		usage := map[string]any{"input_tokens": 1, "output_tokens": 1, "cache_read_input_tokens": 200}
		event := jsonLine(map[string]any{"type": "assistant", "message": map[string]any{"usage": usage}})
		config := fakeCLI(dir, "echo '"+event+"'\nsleep 30")
		config.TokenBudget = 100
		_, err := readWith(ctx, config)
		Expect(err).To(MatchError(ContainSubstring("202 tokens of a 100 budget")))
	})

	It("a message split across events is counted once", func(ctx SpecContext) {
		event := jsonLine(map[string]any{"type": "assistant", "message": map[string]any{
			"id": "m1", "usage": map[string]any{"input_tokens": 60},
		}})
		done := jsonLine(map[string]any{"type": "result", "is_error": false, "result": "{}"})
		config := fakeCLI(dir, "echo '"+event+"'\necho '"+event+"'\necho '"+done+"'")
		config.TokenBudget = 100
		out, err := readWith(ctx, config)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(MatchJSON(`{}`))
	})

	It("a session over its time limit is killed and reported", func(ctx SpecContext) {
		config := fakeCLI(dir, "sleep 30")
		config.Timeout = 200 * time.Millisecond
		started := time.Now()
		_, err := readWith(ctx, config)
		Expect(errors.Is(err, claude.ErrKilled)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("time limit"))
		Expect(time.Since(started)).To(BeNumerically("<", 10*time.Second))
	})

	It("kills what the CLI started when it kills the CLI", func(ctx SpecContext) {
		pidFile := filepath.Join(dir, "child")
		config := fakeCLI(dir, "sleep 30 &\necho $! > "+pidFile+"\nwait")
		config.Timeout = 500 * time.Millisecond
		_, err := readWith(ctx, config)
		Expect(errors.Is(err, claude.ErrKilled)).To(BeTrue(), "%v", err)
		pid, err := os.ReadFile(pidFile)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			_, err := os.Stat("/proc/" + strings.TrimSpace(string(pid)))
			return os.IsNotExist(err)
		}).Should(BeTrue(), "the CLI's child outlived it")
	})

	It("a failed session is reported with its reason", func(ctx SpecContext) {
		event := jsonLine(map[string]any{"type": "result", "is_error": true, "subtype": "error_max_turns"})
		_, err := readWith(ctx, fakeCLI(dir, "echo '"+event+"'"))
		Expect(errors.Is(err, claude.ErrFailed)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("error_max_turns"))
	})

	It("a session that may write is refused without a sandbox", func(ctx SpecContext) {
		_, err := repair(ctx, newHarness(claude.DefaultConfig(claude.APIKey("key"))), "Fix.", map[string]any{})
		Expect(errors.Is(err, claude.ErrRefused)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("needs a sandbox"))
	})

	It("a session with egress or secrets is refused", func(ctx SpecContext) {
		err := worker(ctx, newHarness(claude.DefaultConfig(claude.APIKey("key"))))
		Expect(errors.Is(err, claude.ErrRefused)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("no egress and no secrets"))
	})

	It("refuses a session that did not come through a recorded call", func(ctx SpecContext) {
		h := newHarness(fakeCLI(dir, answers("{}")))
		_, err := h.Run(ctx, agent.Pass{}, "Review this.", agent.Grant{}, json.RawMessage(`{}`))
		Expect(errors.Is(err, claude.ErrRefused)).To(BeTrue(), "%v", err)
		Expect(filepath.Join(dir, "stdin")).NotTo(BeAnExistingFile(), "the CLI ran")
	})

	It("runs the CLI with an environment built from nothing", func(ctx SpecContext) {
		GinkgoT().Setenv("ALMANAC_CANARY_SECRET", "canary")
		GinkgoT().Setenv("ANTHROPIC_AUTH_TOKEN", "operator-token")
		config := fakeCLI(dir, answers("{}"))
		config.BaseURL = "http://127.0.0.1:9"
		_, err := readWith(ctx, config)
		Expect(err).NotTo(HaveOccurred())
		env, err := os.ReadFile(filepath.Join(dir, "env"))
		Expect(err).NotTo(HaveOccurred())
		var names []string
		for line := range strings.Lines(string(env)) {
			name, _, _ := strings.Cut(line, "=")
			names = append(names, name)
		}
		// The shell running the script adds PWD, SHLVL and _ itself.
		Expect(names).To(ConsistOf("PATH", "HOME", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "PWD", "SHLVL", "_"))
		Expect(string(env)).To(ContainSubstring("ANTHROPIC_API_KEY=key\n"))
		Expect(string(env)).NotTo(ContainSubstring("canary"))
		Expect(string(env)).NotTo(ContainSubstring("operator-token"))
	})

	It("runs the CLI in an empty home that it removes afterwards", func(ctx SpecContext) {
		config := fakeCLI(dir, "ls -A > "+filepath.Join(dir, "listing")+"\n"+answers("{}"))
		_, err := readWith(ctx, config)
		Expect(err).NotTo(HaveOccurred())
		env, err := os.ReadFile(filepath.Join(dir, "env"))
		Expect(err).NotTo(HaveOccurred())
		var home, pwd string
		for line := range strings.Lines(string(env)) {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "HOME="); ok {
				home = value
			}
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "PWD="); ok {
				pwd = value
			}
		}
		Expect(home).NotTo(BeEmpty())
		Expect(pwd).To(Equal(home))
		Expect(os.ReadFile(filepath.Join(dir, "listing"))).To(BeEmpty())
		Expect(home).NotTo(BeADirectory())
	})

	Context("in a sandbox", func() {
		var sb *fake.Sandbox

		BeforeEach(func() {
			sb = fake.Answering(func(command string) sandbox.Output {
				if strings.Contains(command, "--binary") {
					return sandbox.Output{Stdout: "diff --git a/src b/src\n"}
				}
				return sandbox.Output{}
			})
		})

		sandboxed := func(config claude.Config) *claude.Harness {
			return newHarness(config).WithSandbox(sb, sandbox.AgentSpec("image"))
		}

		It("a sandboxed session returns the patch and destroys its sandbox", func(ctx SpecContext) {
			out, err := repair(ctx, sandboxed(fakeCLI(dir, answers("{}"))), "Fix.", map[string]any{})
			Expect(err).NotTo(HaveOccurred())
			var p patch.Patch
			Expect(json.Unmarshal(out, &p)).To(Succeed())
			Expect(p.Diff).To(Equal("diff --git a/src b/src\n"))
			Expect(p.Changes).To(BeEmpty())
			Expect(sb.Alive()).To(Equal(0))
		})

		It("a sandbox is destroyed when its session fails", func(ctx SpecContext) {
			_, err := repair(ctx, sandboxed(fakeCLI(dir, "exit 1")), "Fix.", map[string]any{})
			Expect(err).To(HaveOccurred())
			Expect(sb.Alive()).To(Equal(0))
		})

		It("destroys its sandbox when the run is cancelled", func(ctx SpecContext) {
			cancelled, cancel := context.WithCancel(ctx)
			config := fakeCLI(dir, "sleep 30")
			h := sandboxed(config)
			time.AfterFunc(200*time.Millisecond, cancel)
			_, err := repair(cancelled, h, "Fix.", map[string]any{})
			Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "%v", err)
			Expect(sb.Alive()).To(Equal(0))
		})

		It("the checkout is uploaded and never sent to the model", func(ctx SpecContext) {
			input := map[string]any{"logs": "boom", claude.WorkspaceField: "dGFy"}
			_, err := repair(ctx, sandboxed(fakeCLI(dir, answers("{}"))), "Fix.", input)
			Expect(err).NotTo(HaveOccurred())
			sent, err := os.ReadFile(filepath.Join(dir, "stdin"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(sent)).To(ContainSubstring("boom"))
			Expect(string(sent)).NotTo(ContainSubstring("dGFy"))
			Expect(string(sent)).NotTo(ContainSubstring(claude.WorkspaceField))
		})

		It("keeps a prose answer as text beside the patch", func(ctx SpecContext) {
			out, err := repair(ctx, sandboxed(fakeCLI(dir, answers("The build passes now."))), "Fix.", map[string]any{})
			Expect(err).NotTo(HaveOccurred())
			var answer claude.Answer
			Expect(json.Unmarshal(out, &answer)).To(Succeed())
			Expect(answer.Answer).To(MatchJSON(`"The build passes now."`))
			Expect(answer.Verified).To(BeNil())
		})

		It("refuses a diff the sandbox returned only the end of", func(ctx SpecContext) {
			sb = fake.Answering(func(command string) sandbox.Output {
				return sandbox.Output{Truncated: strings.Contains(command, "--binary")}
			})
			_, err := repair(ctx, sandboxed(fakeCLI(dir, answers("{}"))), "Fix.", map[string]any{})
			Expect(errors.Is(err, claude.ErrSandbox)).To(BeTrue(), "%v", err)
			Expect(err.Error()).To(ContainSubstring("cannot be read whole"))
			Expect(sb.Alive()).To(Equal(0))
		})

		It("reads what the agent changed from git, with content in base64", func(ctx SpecContext) {
			blob := strings.Repeat("ab", 20)
			listing := ":100644 100755 " + strings.Repeat("0", 40) + " " + blob + " M\x00run.sh\x00" +
				":100644 000000 " + blob + " " + strings.Repeat("0", 40) + " D\x00gone\x00" +
				":000000 120000 " + strings.Repeat("0", 40) + " " + blob + " A\x00link\x00"
			sb = fake.Answering(func(command string) sandbox.Output {
				switch {
				case strings.Contains(command, "--raw"):
					return sandbox.Output{Stdout: listing}
				case strings.HasPrefix(command, "git cat-file blob "+blob):
					return sandbox.Output{Stdout: "ZWNobwo="}
				}
				return sandbox.Output{}
			})
			out, err := repair(ctx, sandboxed(fakeCLI(dir, answers("{}"))), "Fix.", map[string]any{})
			Expect(err).NotTo(HaveOccurred())
			var p patch.Patch
			Expect(json.Unmarshal(out, &p)).To(Succeed())
			Expect(p.Changes).To(Equal([]patch.Change{
				{Op: patch.Write, Path: "run.sh", Content: "ZWNobwo=", Executable: true},
				{Op: patch.Delete, Path: "gone"},
				{Op: patch.Link, Path: "link"},
			}))
		})

		It("refuses a change list it cannot read", func(ctx SpecContext) {
			sb = fake.Answering(func(command string) sandbox.Output {
				if strings.Contains(command, "--raw") {
					return sandbox.Output{Stdout: ":100644 100644 x; rm -rf / M\x00path\x00"}
				}
				return sandbox.Output{}
			})
			_, err := repair(ctx, sandboxed(fakeCLI(dir, answers("{}"))), "Fix.", map[string]any{})
			Expect(err).To(MatchError(ContainSubstring("unreadable")))
			for _, command := range sb.Commands() {
				Expect(command).NotTo(ContainSubstring("rm -rf"))
			}
		})

		It("records how the repository's own checks ended", func(ctx SpecContext) {
			sb = fake.Answering(func(command string) sandbox.Output {
				if command == "make test" {
					return sandbox.Output{ExitCode: 2, Stdout: "ok\n", Stderr: "FAIL\n"}
				}
				return sandbox.Output{}
			})
			config := fakeCLI(dir, answers("{}"))
			config.Verify = "make test"
			out, err := repair(ctx, sandboxed(config), "Fix.", map[string]any{})
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(MatchJSON(`{"answer": {}, "changes": [], "diff": "", "verified": {"exit_code": 2, "tail": "ok\nFAIL"},
				"usage": {"tokens": 0, "cost_usd": null, "turns": null}}`))
			commands := sb.Commands()
			Expect(commands[len(commands)-1]).To(Equal("make test"), "the checks run after the patch is read")
		})

		It("checks out a commit after its dependencies, with the caches set in both sandboxes", func(ctx SpecContext) {
			checkouts := &oneCommit{tarball: []byte("gzipped source")}
			h := sandboxed(fakeCLI(dir, answers("{}"))).
				WithCheckouts(checkouts).
				WithFetching(sandbox.FetchSpec("image"))
			sha := strings.Repeat("a", 40)
			input := map[string]any{"logs": "boom", claude.CheckoutField: claude.Checkout{
				Repo: hostile.MustParseSlug("owner/repo"), Sha: hostile.MustParseSha(sha),
			}}
			_, err := repair(ctx, h, "Fix.", input)
			Expect(err).NotTo(HaveOccurred())
			Expect(checkouts.asked).To(Equal([]string{"owner/repo@" + sha}))

			specs := sb.Specs()
			Expect(specs).To(HaveLen(2))
			Expect(specs[0].Purpose).To(Equal(sandbox.Agent))
			Expect(specs[1].Purpose).To(Equal(sandbox.Fetch))
			for _, spec := range specs {
				for name, value := range claude.CacheEnv() {
					Expect(spec.Env).To(HaveKeyWithValue(name, value))
				}
			}

			var inAgent []string
			for _, call := range sb.Calls() {
				if call.Sandbox == "fake-1" {
					inAgent = append(inAgent, call.Line)
				}
			}
			Expect(inAgent[0]).To(HavePrefix("tar -xf .dependencies.tar"), "dependencies go in first")
			Expect(inAgent[1]).To(HavePrefix("tar -xzf .almanac/source.tar.gz"), "then the source")
			Expect(inAgent[2]).To(HavePrefix("git init"), "then the baseline")
			Expect(sb.Alive()).To(Equal(0))

			sent, err := os.ReadFile(filepath.Join(dir, "stdin"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(sent)).NotTo(ContainSubstring(sha))
		})

		It("refuses a checkout that names no full commit hash", func(ctx SpecContext) {
			h := sandboxed(fakeCLI(dir, answers("{}"))).WithCheckouts(&oneCommit{})
			input := map[string]any{claude.CheckoutField: map[string]string{"repo": "owner/repo", "sha": "abc"}}
			_, err := repair(ctx, h, "Fix.", input)
			Expect(errors.Is(err, claude.ErrRefused)).To(BeTrue(), "%v", err)
			Expect(sb.Alive()).To(Equal(0))
		})
	})

	It("refuses a workspace or checkout in a session with no sandbox tools", func(ctx SpecContext) {
		h := newHarness(fakeCLI(dir, answers("{}")))
		_, err := read(ctx, h, "Review this.", map[string]any{claude.WorkspaceField: "dGFy"})
		Expect(errors.Is(err, claude.ErrRefused)).To(BeTrue(), "%v", err)
		Expect(filepath.Join(dir, "stdin")).NotTo(BeAnExistingFile(), "the CLI ran")
	})
})

type oneCommit struct {
	tarball []byte
	asked   []string
}

func (o *oneCommit) Tarball(_ context.Context, repo hostile.Slug, sha hostile.Sha) ([]byte, error) {
	o.asked = append(o.asked, repo.String()+"@"+sha.String())
	return o.tarball, nil
}
