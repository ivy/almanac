package config_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/config"
	"github.com/ivy/almanac/diag"
)

// refused parses text that must fail, and returns its diagnostic.
func refused(text string) *diag.Error {
	_, err := config.Parse("almanac.toml", text)
	var e *diag.Error
	Expect(errors.As(err, &e)).To(BeTrue(), "%v", err)
	return e
}

// lines joins the lines of an expected rendering.
func lines(l ...string) string { return strings.Join(l, "\n") + "\n" }

var _ = Describe("Config diagnostics", func() {
	It("a secret in config points at its line and says where it belongs", func() {
		e := refused("github_token = \"abc\"\n")
		Expect(e.Code).To(Equal(diag.ConfigSecret))
		Expect(diag.Render(e, diag.Human)).To(Equal(lines(
			"almanac::config::secret",
			"",
			"  × `github_token` looks like a secret, and secrets come from the environment",
			"   ╭─[almanac.toml:1:1]",
			" 1 │ github_token = <redacted>",
			"   · ──────┬─────",
			"   ·       ╰── move this to the environment",
			"   ╰────",
			"  why: Config files get committed and copied. Secrets come only from the environment, so that a",
			"       config file is always safe to share.",
			"  help: remove it from the file and set it in the environment; rotate it if it was committed",
		)))
	})

	DescribeTable("a secret is never printed by the error about it",
		func(text, key string) {
			e := refused(text)
			Expect(e.Code).To(Equal(diag.ConfigSecret))
			Expect(e.Message).To(ContainSubstring("`" + key + "`"))
			for _, rendered := range []string{diag.Render(e, diag.Human), diag.Render(e, diag.JSON), e.Error()} {
				Expect(rendered).NotTo(ContainSubstring("hunter2"))
			}
		},
		Entry("a key", "listen = \"x\"\napi_key = \"hunter2\"\n", "api_key"),
		Entry("a key in a table", "listen = \"x\"\n[credentials]\ngithub = \"hunter2\"\n", "credentials.github"),
		Entry("a dotted key", "github.token = \"hunter2\"\n", "github.token"),
		Entry("a quoted key", "\"API Key\" = \"hunter2\"\n", "API Key"),
		Entry("a key in an inline table", "github = { token = \"hunter2\" }\nlisten = \"x\"\n", "github.token"),
		Entry("a value over several lines",
			"listen = \"x\"\nprivate_key = '''\nhunter2\nhunter2\n'''\nconcurrency = 1\n", "private_key"),
		Entry("a file that does not parse", "api_key = hunter2\nlisten = \n", "api_key"),
	)

	It("an unknown key is named and lists what is accepted", func() {
		e := refused("listen = \"127.0.0.1:1\"\nmax_runs = 3\n")
		Expect(e.Code).To(Equal(diag.ConfigUnknownKey))
		Expect(diag.Render(e, diag.Human)).To(Equal(lines(
			"almanac::config::unknown_key",
			"",
			"  × unknown config key `max_runs`",
			"   ╭─[almanac.toml:2:1]",
			" 1 │ listen = \"127.0.0.1:1\"",
			" 2 │ max_runs = 3",
			"   · ────┬───",
			"   ·     ╰── Almanac does not read this",
			"   ╰────",
			"  why: Config can only tighten limits. Behaviour lives in the binary, so there is no key for a",
			"       capability, an effect, or a workflow. A misspelled limit would otherwise be ignored and leave",
			"       you with a wider limit than you meant.",
			"  help: config can only tighten limits: the keys are listen, max_chain_depth, runs_per_hour_cap,",
			"        overall_runs_per_hour_cap, concurrency, and scan_interval_minutes",
		)))
	})

	It("a table is an unknown key, and is marked where it opens", func() {
		e := refused("listen = \"x\"\n[workflows]\nenabled = 1\n")
		Expect(e.Code).To(Equal(diag.ConfigUnknownKey))
		Expect(e.Message).To(ContainSubstring("`workflows`"))
		Expect(diag.Render(e, diag.Human)).To(ContainSubstring("╭─[almanac.toml:2:2]"))
	})

	It("a wrong type points at the value", func() {
		e := refused("concurrency = \"many\"\n")
		Expect(e.Code).To(Equal(diag.ConfigParse))
		Expect(diag.Render(e, diag.Human)).To(Equal(lines(
			"almanac::config::parse",
			"",
			"  × config file is not valid: `concurrency` takes a whole number, not a string",
			"   ╭─[almanac.toml:1:15]",
			" 1 │ concurrency = \"many\"",
			"   ·               ───┬──",
			"   ·                  ╰── here",
			"   ╰────",
			"  why: An almanac refuses to start on a config it cannot understand, because guessing could widen a",
			"       limit.",
			"  help: fix the marked line; run `explain almanac::config::parse` for the accepted keys",
		)))
	})

	DescribeTable("a value out of range points at the value",
		func(text, message, at string) {
			e := refused(text)
			Expect(e.Code).To(Equal(diag.ConfigParse))
			Expect(e.Message).To(ContainSubstring(message))
			Expect(diag.Render(e, diag.Human)).To(ContainSubstring(at))
		},
		Entry("negative", "max_chain_depth = -1 # none\n", "-1 is negative", "almanac.toml:1:19"),
		Entry("too large", "runs_per_hour_cap = 99999999999\n", "no larger than", "almanac.toml:1:21"),
		Entry("a fraction", "concurrency = 2.5\n", "not a fraction", "almanac.toml:1:15"),
	)

	It("invalid TOML points at where it stops making sense", func() {
		e := refused("listen = \"x\"\nconcurrency = \n")
		Expect(e.Code).To(Equal(diag.ConfigParse))
		Expect(diag.Render(e, diag.Human)).To(ContainSubstring("╭─[almanac.toml:2:15]"))
	})

	It("json output carries code, message, help and spans", func() {
		var out map[string]any
		Expect(json.Unmarshal([]byte(diag.Render(refused("github_token = \"abc\"\n"), diag.JSON)), &out)).To(Succeed())
		Expect(out["code"]).To(Equal("almanac::config::secret"))
		Expect(out["message"]).To(ContainSubstring("environment"))
		Expect(out["help"]).NotTo(BeEmpty())
		span := out["labels"].([]any)[0].(map[string]any)["span"].(map[string]any)
		Expect(span["offset"]).To(BeEquivalentTo(0))
		Expect(span["length"]).To(BeEquivalentTo(12))
		Expect(span["line"]).To(BeEquivalentTo(1))
		Expect(span["column"]).To(BeEquivalentTo(1))
	})
})

var _ = Describe("Loading config", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		GinkgoT().Chdir(dir)
		for _, name := range []string{
			"ALMANAC_CONFIG", "ALMANAC_LISTEN", "ALMANAC_MAX_CHAIN_DEPTH", "ALMANAC_RUNS_PER_HOUR_CAP",
			"ALMANAC_OVERALL_RUNS_PER_HOUR_CAP", "ALMANAC_CONCURRENCY", "ALMANAC_SCAN_INTERVAL_MINUTES",
		} {
			GinkgoT().Setenv(name, "")
			Expect(os.Unsetenv(name)).To(Succeed())
		}
	})

	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		Expect(os.WriteFile(path, []byte(text), 0o600)).To(Succeed())
		return path
	}

	It("reads every key from almanac.toml in the working directory", func() {
		write("almanac.toml", lines(
			`listen = "127.0.0.1:9"`,
			"max_chain_depth = 2",
			"runs_per_hour_cap = 10",
			"overall_runs_per_hour_cap = 30",
			"concurrency = 1",
			"scan_interval_minutes = 60",
		))
		c, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Listen).To(Equal("127.0.0.1:9"))
		Expect(c.Tighten(config.DefaultLimits())).To(Equal(config.Limits{
			MaxChainDepth:      2,
			RunsPerHour:        config.CapAt(10),
			OverallRunsPerHour: config.CapAt(30),
			Concurrency:        1,
			ScanInterval:       time.Hour,
		}))
	})

	It("reads the file ALMANAC_CONFIG names", func() {
		GinkgoT().Setenv("ALMANAC_CONFIG", write("elsewhere.toml", "concurrency = 2\n"))
		c, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Concurrency).To(Equal(config.CapAt(2)))
	})

	It("runs on the binary's limits when there is no file", func() {
		GinkgoT().Setenv("ALMANAC_CONFIG", filepath.Join(dir, "missing.toml"))
		c, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(c).To(Equal(config.Config{}))
		Expect(c.Tighten(config.DefaultLimits())).To(Equal(config.DefaultLimits()))
	})

	It("refuses a file that exists and cannot be read", func() {
		GinkgoT().Setenv("ALMANAC_CONFIG", dir)
		_, err := config.Load()
		var e *diag.Error
		Expect(errors.As(err, &e)).To(BeTrue())
		Expect(e.Code).To(Equal(diag.ConfigRead))
		Expect(e.Cause).To(HaveOccurred())
	})

	It("lets ALMANAC_* variables win over the file", func() {
		write("almanac.toml", "concurrency = 2\nlisten = \"127.0.0.1:1\"\n")
		GinkgoT().Setenv("ALMANAC_CONCURRENCY", "3")
		GinkgoT().Setenv("ALMANAC_LISTEN", "127.0.0.1:2")
		GinkgoT().Setenv("ALMANAC_SCAN_INTERVAL_MINUTES", "30")
		c, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Concurrency).To(Equal(config.CapAt(3)))
		Expect(c.Listen).To(Equal("127.0.0.1:2"))
		Expect(c.ScanInterval).To(Equal(30 * time.Minute))
	})

	DescribeTable("refuses a variable that is not a whole number, naming it",
		func(value string) {
			GinkgoT().Setenv("ALMANAC_MAX_CHAIN_DEPTH", value)
			_, err := config.Load()
			var e *diag.Error
			Expect(errors.As(err, &e)).To(BeTrue())
			Expect(e.Code).To(Equal(diag.ConfigEnvValue))
			Expect(e.Message).To(Equal("ALMANAC_MAX_CHAIN_DEPTH must be a whole number"))
		},
		Entry("a word", "many"),
		Entry("negative", "-1"),
		Entry("empty", ""),
		Entry("too large", "99999999999"),
	)

	It("can never widen what the binary declares", func() {
		write("almanac.toml", lines(
			"max_chain_depth = 50",
			"concurrency = 64",
			"scan_interval_minutes = 1",
		))
		c, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Tighten(config.DefaultLimits())).To(Equal(config.DefaultLimits()))
	})
})
