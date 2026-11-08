package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/pflag"

	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/config"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/internal/fixture/sample"
	"github.com/ivy/almanac/internal/fixture/workflows"
	"github.com/ivy/almanac/internal/pgtest"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/postgres"
	"github.com/ivy/almanac/workflow"
)

var _ = Describe("An almanac binary's verbs", func() {
	var w *world
	BeforeEach(func() { w = newWorld() })

	Describe("synth", func() {
		It("prints each workflow's declaration, needing nothing set up", func() {
			a := w.almanac(holding, watching)
			a.Setup = func(context.Context, app.Setting) (*providers.Set, error) { return nil, errors.New("set up") }
			text := execute(a, "synth")
			Expect(text.code).To(Equal(0), text.stderr)
			Expect(text.stdout).To(ContainSubstring("\n  {\n    \"workflow\": \"holding\""), "pretty for a person")

			out := execute(a, "--format", "json", "synth")
			Expect(out.code).To(Equal(0), out.stderr)
			Expect(strings.Count(out.stdout, "\n")).To(Equal(1), "one line for a program")
			Expect(out.stdout).To(MatchJSON(`[` + manifest(holding) + `,` + manifest(watching) + `]`))
		})

		It("makes each model runnable, and shows its digest", func() {
			a := app.Almanac{Name: "almanac-spec", Vocabulary: sample.Vocabulary,
				Models: []model.Workflow{workflows.DependencyUpdates, workflows.ErrorTriage}}
			out := execute(a, "--format", "json", "synth")
			Expect(out.code).To(Equal(0), out.stderr)
			Expect(out.stdout).To(ContainSubstring(`"workflow":"dependency_updates"`))
			Expect(out.stdout).To(ContainSubstring(`"workflow":"error_triage"`))
			Expect(out.stdout).To(MatchRegexp(`"digest":"sha256:[0-9a-f]{64}"`))
		})

		It("refuses to start with a model the checker refuses, naming every problem", func() {
			a := app.Almanac{Name: "almanac-spec", Vocabulary: renovate.Vocabulary,
				Models: []model.Workflow{workflows.DependencyUpdates}}
			for _, verb := range []string{"synth", "check", "run"} {
				out := execute(a, "--format", "json", verb)
				Expect(out.code).To(Equal(1), verb)
				lines := strings.Split(strings.TrimSpace(out.stderr), "\n")
				Expect(len(lines)).To(BeNumerically(">", 1), "every problem, one per line: %s", out.stderr)
				for _, line := range lines {
					Expect(decoded(line)["code"]).To(HavePrefix("almanac::model::"), line)
				}
			}
			text := execute(a, "synth")
			Expect(text.stderr).To(ContainSubstring("dependency_updates"))
		})
	})

	Describe("check", func() {
		It("confirms every provider is set up, and prints the mode and limits", func() {
			text := execute(w.almanac(holding), "check")
			Expect(text.code).To(Equal(0), text.stderr)
			Expect(text.stdout).To(Equal("almanac-spec: ok, 1 workflow(s), dry-run, limits: max_chain_depth=3 runs_per_hour_cap=none " +
				"overall_runs_per_hour_cap=none concurrency=4 scan_interval_minutes=15\n"))

			out := execute(w.almanac(holding), "--format", "json", "check")
			Expect(out.code).To(Equal(0), out.stderr)
			Expect(out.stdout).To(MatchJSON(`{"almanac": "almanac-spec", "ok": true, "workflows": 1, "mode": "dry-run", "read_only": false,
				"limits": {"max_chain_depth": 3, "runs_per_hour_cap": null, "overall_runs_per_hour_cap": null, "concurrency": 4, "scan_interval_minutes": 15}}`))
		})

		It("is live only when ALMANAC_MODE is live, and read-only whatever the mode once ALMANAC_READ_ONLY is set", func() {
			setenv("ALMANAC_MODE", "live")
			Expect(decoded(execute(w.almanac(holding), "--format", "json", "check").stdout)).To(
				And(HaveKeyWithValue("mode", "live"), HaveKeyWithValue("read_only", false)))
			setenv("ALMANAC_READ_ONLY", "true")
			Expect(decoded(execute(w.almanac(holding), "--format", "json", "check").stdout)).To(
				And(HaveKeyWithValue("mode", "dry-run"), HaveKeyWithValue("read_only", true)))
			Expect(execute(w.almanac(holding), "check").stdout).To(ContainSubstring(", read-only dry-run, "))
		})

		It("starts from the limits the binary declares, which config only tightens", func() {
			a := w.almanac(holding)
			a.Limits = config.Limits{MaxChainDepth: 2, Concurrency: 2, ScanInterval: config.DefaultLimits().ScanInterval}
			configure("max_chain_depth = 5\nconcurrency = 1\nruns_per_hour_cap = 7\n")
			Expect(decoded(execute(a, "--format", "json", "check").stdout)["limits"]).To(Equal(map[string]any{
				"max_chain_depth": 2.0, "runs_per_hour_cap": 7.0, "overall_runs_per_hour_cap": nil,
				"concurrency": 1.0, "scan_interval_minutes": 15.0,
			}))
		})

		DescribeTable("the scan interval can be lengthened, and never shortened",
			func(minutes string, want float64) {
				setenv("ALMANAC_SCAN_INTERVAL_MINUTES", minutes)
				limits := decoded(execute(w.almanac(holding), "--format", "json", "check").stdout)["limits"]
				Expect(limits).To(HaveKeyWithValue("scan_interval_minutes", want))
			},
			Entry("lengthened to an hour", "60", 60.0),
			Entry("not shortened to five minutes", "5", 15.0),
		)

		It("names a missing provider, and exits 1", func() {
			w.fleet = nil
			text := execute(w.almanac(watching), "check")
			Expect(text.code).To(Equal(1))
			Expect(text.stdout).To(BeEmpty())
			Expect(text.stderr).To(HavePrefix("almanac::provider::missing\n"))
			Expect(text.stderr).To(ContainSubstring("watching needs a provider for app_test.fleetScanner"))

			out := execute(w.almanac(watching), "--format", "json", "check")
			Expect(out.code).To(Equal(1))
			Expect(decoded(out.stderr)).To(HaveKeyWithValue("code", "almanac::provider::missing"))
		})

		It("in live mode needs a forge and a tracker", func() {
			setenv("ALMANAC_MODE", "live")
			a := w.almanac(holding)
			a.Setup = func(context.Context, app.Setting) (*providers.Set, error) {
				return providers.New().UseHarness(almanactest.Replying(okReply)), nil
			}
			out := execute(a, "--format", "json", "check")
			Expect(out.code).To(Equal(1))
			Expect(decoded(out.stderr)["code"]).To(Equal("almanac::provider::missing"))
		})

		It("hands Setup the name, the config and the switches", func() {
			setenv("ALMANAC_READ_ONLY", "1")
			configure("concurrency = 2\n")
			var got app.Setting
			a := w.almanac(holding)
			a.Setup = func(ctx context.Context, s app.Setting) (*providers.Set, error) {
				got = s
				return w.setup(ctx, s)
			}
			Expect(execute(a, "check").code).To(Equal(0))
			Expect(got.Name).To(Equal("almanac-spec"))
			concurrency, _ := got.Config.Concurrency.Value()
			Expect(concurrency).To(Equal(2))
			Expect(got.Switches.ReadOnly).To(BeTrue())
			Expect(got.Switches.Mode).To(Equal(effect.DryRun))
		})
	})

	Describe("config", func() {
		It("a bad config key stops the almanac and points at the line", func() {
			configure("github_token = \"abc\"\n")
			out := execute(w.almanac(holding), "check")
			Expect(out.code).To(Equal(1))
			Expect(out.stderr).To(ContainSubstring("almanac.toml:1:1"))
			Expect(out.stderr).To(ContainSubstring("secrets come from the environment"))
			Expect(out.stderr).NotTo(ContainSubstring("abc"))
		})

		It("errors are JSON when asked", func() {
			configure("github_token = \"abc\"\n")
			out := execute(w.almanac(holding), "--format", "json", "check")
			Expect(out.code).To(Equal(1))
			Expect(decoded(out.stderr)).To(HaveKeyWithValue("code", "almanac::config::secret"))
		})
	})

	Describe("explain", func() {
		It("prints the long form", func() {
			out := execute(w.almanac(holding), "explain", "almanac::config::secret")
			Expect(out.code).To(Equal(0))
			Expect(out.stdout).To(HavePrefix("# "))
			Expect(out.stdout).To(ContainSubstring("## What to do next"))
		})

		It("answers in JSON when asked", func() {
			out := execute(w.almanac(holding), "--format", "json", "explain", "almanac::usage")
			Expect(out.code).To(Equal(0))
			Expect(decoded(out.stdout)).To(And(HaveKeyWithValue("code", "almanac::usage"), HaveKey("page")))
		})

		It("explains its own refusal of a code no error has", func() {
			out := execute(w.almanac(holding), "--format", "json", "explain", "almanac::nonsense")
			Expect(out.code).To(Equal(1))
			Expect(decoded(out.stderr)).To(HaveKeyWithValue("code", "almanac::explain::unknown_code"))
		})
	})

	Describe("the command line", func() {
		It("every verb answers in JSON when asked", func() {
			for _, args := range [][]string{
				{"--format", "json", "check"},
				{"--format", "json", "synth"},
				{"--format", "json", "explain", "almanac::usage"},
				{"check", "--format=json"},
			} {
				out := execute(w.almanac(holding), args...)
				Expect(out.code).To(Equal(0), "%v: %s", args, out.stderr)
				Expect(strings.TrimSpace(out.stdout)).To(Or(HavePrefix("{"), HavePrefix("[")))
				Expect(strings.Count(out.stdout, "\n")).To(Equal(1))
			}
		})

		It("an unknown verb says what exists", func() {
			out := execute(w.almanac(holding), "frobnicate")
			Expect(out.code).To(Equal(1))
			Expect(out.stderr).To(HavePrefix("almanac::usage\n"))
			Expect(out.stderr).To(ContainSubstring("not a command: frobnicate"))
			Expect(out.stderr).To(ContainSubstring("synth | check"))

			out = execute(w.almanac(holding), "--format", "json", "frobnicate")
			Expect(decoded(out.stderr)).To(And(HaveKeyWithValue("code", "almanac::usage"),
				HaveKeyWithValue("message", "not a command: frobnicate")))
		})

		DescribeTable("a command line that is not one is a usage error",
			func(args ...string) {
				out := execute(w.almanac(holding), append([]string{"--format", "json"}, args...)...)
				Expect(out.code).To(Equal(1))
				Expect(decoded(out.stderr)).To(HaveKeyWithValue("code", "almanac::usage"))
			},
			Entry("no verb"),
			Entry("a missing argument", "simulate", opened),
			Entry("an extra argument", "check", "now"),
			Entry("an unknown flag", "check", "--quickly"),
			Entry("a flag of another verb", "check", "--repo", "owner/repo"),
		)

		It("refuses a format it does not write", func() {
			out := execute(w.almanac(holding), "--format", "xml", "check")
			Expect(out.code).To(Equal(1))
			Expect(out.stderr).To(HavePrefix("almanac::usage\n"))
			Expect(out.stderr).To(ContainSubstring(`--format is human or json, not "xml"`))
		})

		It("help is not an error", func() {
			out := execute(w.almanac(holding), "--help")
			Expect(out.code).To(Equal(0), out.stderr)
			Expect(out.stdout).To(HavePrefix("usage: almanac-spec [--format json] synth | check | run [--dry-run] | " +
				"backfill [--repo OWNER/NAME] [--dry-run] | simulate KIND FILE [--dry-run] | explain CODE\n"))

			out = execute(w.almanac(holding), "simulate", "--help")
			Expect(out.code).To(Equal(0), out.stderr)
			Expect(out.stdout).To(HavePrefix("usage: almanac-spec simulate KIND FILE\n"))
			Expect(out.stdout).To(ContainSubstring("--dry-run"))
		})
	})

	Describe("an almanac's own verb", func() {
		var a app.Almanac
		BeforeEach(func() {
			a = w.almanac(holding)
			a.Verbs = []app.Verb{{
				Name: "report", Args: "[OWNER]", Short: "Report on every pull request.",
				Run: func(_ context.Context, s app.Setting, args []string) (app.Said, error) {
					if len(args) > 0 && args[0] == "nobody" {
						return app.Said{}, diag.MissingEnv("ALMANAC_OWNER", "the account to report on")
					}
					return app.Said{Human: "3 pull requests for " + s.Name, JSON: map[string]any{"pulls": 3, "args": args}}, nil
				},
			}}
		})

		It("is listed, and says what it has to say for people and for programs", func() {
			Expect(execute(a, "--help").stdout).To(ContainSubstring("| explain CODE | report [OWNER]\n"))
			Expect(execute(a, "report").stdout).To(Equal("3 pull requests for almanac-spec\n"))
			Expect(execute(a, "--format", "json", "report", "owner").stdout).To(MatchJSON(`{"pulls": 3, "args": ["owner"]}`))
		})

		It("prints its error as every verb does", func() {
			out := execute(a, "--format", "json", "report", "nobody")
			Expect(out.code).To(Equal(1))
			Expect(decoded(out.stderr)).To(HaveKeyWithValue("code", "almanac::config::env_missing"))
		})

		It("refuses a flag it does not declare", func() {
			out := execute(a, "report", "--since", "1h")
			Expect(out.code).To(Equal(1))
			Expect(out.stderr).To(ContainSubstring("--since"))
		})
	})

	Describe("an almanac's own verb with flags", func() {
		var a app.Almanac
		BeforeEach(func() {
			a = w.almanac(holding)
			a.Verbs = []app.Verb{{
				Name: "compare", Args: "--rust FILE [--since DURATION]", Short: "Compare two reports.",
				Flags: func(flags *pflag.FlagSet) {
					flags.String("rust", "", "the Rust report")
					flags.Duration("since", 0, "how far back to look")
				},
				Run: func(_ context.Context, s app.Setting, args []string) (app.Said, error) {
					rust, err := s.Flags.GetString("rust")
					if err != nil {
						return app.Said{}, err
					}
					since, err := s.Flags.GetDuration("since")
					if err != nil {
						return app.Said{}, err
					}
					return app.Said{Human: rust + " " + since.String(), JSON: map[string]any{"rust": rust, "since": since.String(), "args": args}}, nil
				},
			}}
		})

		It("reads them from the command line, wherever --format goes", func() {
			Expect(execute(a, "compare", "--rust", "r.json").stdout).To(Equal("r.json 0s\n"))
			Expect(execute(a, "compare", "--since", "2h", "--format", "json", "--rust=r.json", "extra").stdout).
				To(MatchJSON(`{"rust": "r.json", "since": "2h0m0s", "args": ["extra"]}`))
		})

		It("lists them in the verb's help", func() {
			Expect(execute(a, "compare", "--help").stdout).To(ContainSubstring("--since duration"))
		})

		It("refuses a value the flag cannot take", func() {
			out := execute(a, "compare", "--since", "soon")
			Expect(out.code).To(Equal(1))
			Expect(out.stderr).To(ContainSubstring("soon"))
		})
	})

	Describe("a verb opening the almanac's store", func() {
		It("needs DATABASE_URL", func(ctx SpecContext) {
			GinkgoT().Setenv("DATABASE_URL", "")
			_, _, err := app.Setting{Name: "almanac-spec"}.OpenStore(ctx, effect.DryRun)
			var refused *diag.Error
			Expect(errors.As(err, &refused)).To(BeTrue())
			Expect(refused.Code).To(Equal(diag.ConfigEnvMissing))
			Expect(refused.Message).To(ContainSubstring("DATABASE_URL"))
		})

		It("reads the scope of the mode it is given, and changes nothing", Label("db"), func(ctx SpecContext) {
			url := pgtest.Database()
			GinkgoT().Setenv("DATABASE_URL", url)
			pool, err := pgxpool.New(ctx, url)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(pool.Close)

			s := app.Setting{Name: "almanac-spec"}
			st, done, err := s.OpenStore(ctx, effect.DryRun)
			Expect(err).NotTo(HaveOccurred())
			_, err = st.RunsSince(ctx, time.Time{})
			Expect(err).To(HaveOccurred(), "an unmigrated database is not brought up to date")
			done()

			Expect(postgres.Migrate(ctx, pool)).To(Succeed())
			dry := postgres.New(pool, effect.DryRun.Scope("almanac-spec"))
			_, err = dry.Admit(ctx, "t", "d1", store.Queue(store.Event{Workflow: "w", Key: "k", Payload: json.RawMessage("1")}))
			Expect(err).NotTo(HaveOccurred())
			_, err = dry.Claim(ctx, store.ClaimRequest{Budgets: []store.Budget{{Workflow: "w", Limit: 1}}, Window: time.Hour, Now: time.Now()})
			Expect(err).NotTo(HaveOccurred())

			for mode, runs := range map[effect.Mode]int{effect.DryRun: 1, effect.Live: 0} {
				st, done, err := s.OpenStore(ctx, mode)
				Expect(err).NotTo(HaveOccurred())
				found, err := st.RunsSince(ctx, time.Time{})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(HaveLen(runs), mode.String())
				done()
			}
		})
	})
})

// manifest is w's declaration as JSON.
func manifest(w workflow.Registered) string {
	GinkgoHelper()
	text, err := json.Marshal(w.Manifest())
	Expect(err).NotTo(HaveOccurred())
	return string(text)
}
