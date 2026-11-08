package main_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"

	"github.com/ivy/almanac/internal/pgtest"
	"github.com/ivy/almanac/simforge"
)

// The Rust specification is almanacs/renovate/tests/cli.rs and almanacs/renovate/tests/daemon.rs:
// the binary as an operator or an agent meets it, and as a service.

// green is a real check suite that finished green, as GitHub delivers it.
const green = "../../fixtures/check-suite-green.json"

// unreachable is an address nothing answers on, for a service a spec must never reach.
const unreachable = "http://127.0.0.1:1"

// launch runs almanac-renovate with args, its environment holding only env and an
// ALMANAC_CONFIG that names a file that does not exist.
func launch(env []string, args ...string) *gexec.Session {
	GinkgoHelper()
	cmd := exec.Command(binary, args...)
	cmd.Env = append([]string{"ALMANAC_CONFIG=" + filepath.Join(GinkgoT().TempDir(), "none.toml")}, env...)
	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	return session
}

// configured runs almanac-renovate with the credentials it needs to start, and config as its
// almanac.toml. GitHub, the registries and the advisory feed are addresses nothing answers on.
func configured(config string, args ...string) *gexec.Session {
	GinkgoHelper()
	path := filepath.Join(GinkgoT().TempDir(), "almanac.toml")
	Expect(os.WriteFile(path, []byte(config), 0o600)).To(Succeed())
	cmd := exec.Command(binary, args...)
	cmd.Env = []string{
		"ALMANAC_CONFIG=" + path, "GITHUB_TOKEN=t", "ANTHROPIC_API_KEY=k",
		"GITHUB_API_URL=" + unreachable, "ALMANAC_REGISTRY_URL=" + unreachable, "ALMANAC_ADVISORIES_URL=" + unreachable,
	}
	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	return session
}

// exited waits for session to exit with code.
func exited(session *gexec.Session, code int) {
	GinkgoHelper()
	Eventually(session).WithTimeout(60*time.Second).Should(gexec.Exit(code), string(session.Err.Contents()))
}

// decoded is output holding one JSON object.
func decoded(output []byte) map[string]any {
	GinkgoHelper()
	var value map[string]any
	Expect(json.Unmarshal(output, &value)).To(Succeed(), string(output))
	return value
}

// redFleet is one repository with a red Renovate pull request, which would otherwise reach an
// agent and post statuses.
func redFleet() *simforge.Forge {
	GinkgoHelper()
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	sim.AddRepo(simforge.PublicRepo("owner/repo"))
	pull := simforge.RenovatePull("renovate/tar-7.x", "Update tar")
	pull.Body = "| Package | Change |\n|---|---|\n"
	number := sim.AddPull("owner/repo", pull)
	sim.AddSuite("owner/repo", simforge.FinishedSuite(sim.Pull("owner/repo", number).HeadSHA, "renovate/tar-7.x", "failure", "boom"))
	return sim
}

// readOnly runs a read-only pass over sim, with no model credential at all.
func readOnly(sim *simforge.Forge, args ...string) *gexec.Session {
	GinkgoHelper()
	return launch([]string{
		"ALMANAC_READ_ONLY=1", "GITHUB_TOKEN=t", "GITHUB_API_URL=" + sim.URL(),
		"ALMANAC_OWNER=owner", "ALMANAC_RENOVATE_AUTHOR=" + simforge.Renovate,
		"ALMANAC_REGISTRY_URL=" + sim.URL(), "ALMANAC_ADVISORIES_URL=" + sim.URL(),
	}, append([]string{"--format", "json"}, args...)...)
}

var _ = Describe("almanac-renovate", func() {
	It("a bad config key stops the almanac and points at the line", func() {
		session := configured("github_token = \"abc\"\n", "check")
		exited(session, 1)
		text := string(session.Err.Contents())
		Expect(text).To(ContainSubstring("almanac.toml:1:1"))
		Expect(text).To(ContainSubstring("secrets come from the environment"))
		Expect(text).NotTo(ContainSubstring("abc"))
	})

	It("errors are JSON when asked", func() {
		session := configured("github_token = \"abc\"\n", "--format", "json", "check")
		exited(session, 1)
		Expect(decoded(session.Err.Contents())).To(HaveKeyWithValue("code", "almanac::config::secret"))
	})

	It("every verb answers in JSON when asked", func() {
		for _, args := range [][]string{
			{"--format", "json", "check"},
			{"--format", "json", "synth"},
			{"--format", "json", "explain", "almanac::usage"},
			{"outcomes", "build_failures", "--format", "json"},
			{"outcomes", "security_fixes", "--format", "json"},
		} {
			session := configured("", args...)
			exited(session, 0)
			Expect(json.Valid(session.Out.Contents())).To(BeTrue(), "%v: %s", args, session.Out.Contents())
		}
	})

	It("checks both workflows, and says it is a dry run unless told otherwise", func() {
		session := configured("", "--format", "json", "check")
		exited(session, 0)
		Expect(decoded(session.Out.Contents())).To(And(
			HaveKeyWithValue("almanac", "almanac-renovate"), HaveKeyWithValue("ok", true),
			HaveKeyWithValue("workflows", 2.0), HaveKeyWithValue("mode", "dry-run"),
		))
	})

	It("an OAuth token is enough to start", func() {
		session := launch([]string{"GITHUB_TOKEN=t", "CLAUDE_CODE_OAUTH_TOKEN=o"}, "--format", "json", "check")
		exited(session, 0)
	})

	It("names the model credential it lacks", func() {
		session := launch([]string{"GITHUB_TOKEN=t"}, "--format", "json", "check")
		exited(session, 1)
		Expect(decoded(session.Err.Contents())).To(HaveKeyWithValue("code", "almanac::provider::missing"))
	})

	It("an unknown verb says what exists", func() {
		session := configured("", "frobnicate")
		exited(session, 1)
		Expect(session.Err).To(gbytes.Say(`synth \| check`))

		session = configured("", "--format", "json", "frobnicate")
		exited(session, 1)
		Expect(decoded(session.Err.Contents())).To(HaveKeyWithValue("help", ContainSubstring("| explain CODE | report | outcomes [WORKFLOW] | views [WORKFLOW]")))
	})

	It("explain prints the long form", func() {
		session := configured("", "explain", "almanac::config::secret")
		exited(session, 0)
		Expect(session.Out).To(gbytes.Say("## What to do next"))
	})

	It("simulate goes through the production path", func() {
		session := configured("", "simulate", "github.check_suite.completed", green, "--dry-run")
		exited(session, 0)
		Expect(session.Out).To(gbytes.Say("simulate: ignored"))
	})

	It("backfill refuses a repository it cannot read", func() {
		session := configured("", "--format", "json", "backfill", "--repo", "not a slug")
		exited(session, 1)
		Expect(decoded(session.Err.Contents())).To(HaveKeyWithValue("code", "almanac::usage"))
	})

	It("a read-only pass writes nothing and calls no model", func() {
		sim := redFleet()
		session := readOnly(sim, "backfill", "--dry-run")
		exited(session, 0)
		Expect(decoded(session.Out.Contents())).To(HaveKeyWithValue("backfill", HaveKeyWithValue("found", 1.0)))
		Expect(sim.Writes()).To(BeEmpty(), "nothing reached the forge")
	})

	It("a read-only pass writes nothing even when told to go live", func() {
		sim := redFleet()
		session := launch([]string{
			"ALMANAC_READ_ONLY=1", "ALMANAC_MODE=live", "GITHUB_TOKEN=t", "GITHUB_API_URL=" + sim.URL(),
			"ALMANAC_OWNER=owner", "ALMANAC_RENOVATE_AUTHOR=" + simforge.Renovate,
			"ALMANAC_REGISTRY_URL=" + sim.URL(), "ALMANAC_ADVISORIES_URL=" + sim.URL(),
		}, "--format", "json", "backfill")
		exited(session, 0)
		Expect(decoded(session.Out.Contents())).To(HaveKeyWithValue("backfill", HaveKeyWithValue("found", 1.0)))
		Expect(sim.Writes()).To(BeEmpty(), "nothing reached the forge")
	})

	It("a backfill of a repository it can see finds its pull requests", func() {
		sim := redFleet()
		session := readOnly(sim, "backfill", "--repo", "owner/repo", "--dry-run")
		exited(session, 0)
		Expect(decoded(session.Out.Contents())).To(HaveKeyWithValue("backfill", HaveKeyWithValue("found", 1.0)))
	})

	// Finding nothing would read as all clear, for a repository nobody looked at.
	It("a backfill of a repository it cannot see fails", func() {
		sim := redFleet()
		session := readOnly(sim, "backfill", "--repo", "owner/unseen", "--dry-run")
		exited(session, 1)
		Expect(decoded(session.Err.Contents())).To(HaveKeyWithValue("code", "almanac::scan::failed"))
		Expect(string(session.Err.Contents())).To(ContainSubstring("owner/unseen"))
	})

	It("help is not an error", func() {
		session := configured("", "--help")
		exited(session, 0)
		Expect(session.Out).To(gbytes.Say("usage:"))
	})

	It("simulating a kind nothing handles says so", func() {
		session := configured("", "simulate", "no.such.kind", green, "--dry-run")
		exited(session, 0)
		Expect(session.Out).To(gbytes.Say("no workflow is triggered by"))
	})

	It("the report verb reads through the binary, and refuses without a token", func() {
		sim := simforge.Start()
		DeferCleanup(sim.Close)
		sim.AddRepo(simforge.PublicRepo("owner/alpha"))
		sim.AddRepo(simforge.PublicRepo("owner/beta"))
		sim.AddRepo(simforge.PublicRepo("owner/quiet"))
		sim.AddPull("owner/beta", simforge.RenovatePull("renovate/no-ci-1.x", "Update"))
		session := launch([]string{
			"GITHUB_TOKEN=t", "ALMANAC_OWNER=owner", "ALMANAC_RENOVATE_AUTHOR=" + simforge.Renovate,
			"GITHUB_API_URL=" + sim.URL(), "ALMANAC_REGISTRY_URL=" + unreachable,
		}, "--format", "json", "report")
		exited(session, 0)
		Expect(decoded(session.Out.Contents())).To(HaveKeyWithValue("repositories_scanned", 3.0))
		Expect(sim.Writes()).To(BeEmpty())

		session = launch([]string{"ALMANAC_OWNER=owner"}, "--format", "json", "report")
		exited(session, 1)
		Expect(decoded(session.Err.Contents())).To(HaveKeyWithValue("code", "almanac::config::env_missing"))
	})

	It("draws each workflow as a page, and refuses a workflow it does not have", func() {
		session := configured("", "views", "security_fixes")
		exited(session, 0)
		Expect(string(session.Out.Contents())).To(HavePrefix("<!doctype html>"))
		Expect(string(session.Out.Contents())).To(ContainSubstring("security_fixes"))

		session = configured("", "--format", "json", "views", "no_such_workflow")
		exited(session, 1)
		Expect(decoded(session.Err.Contents())).To(HaveKeyWithValue("code", "almanac::usage"))
	})

	Describe("as a service", func() {
		It("run needs a database", func() {
			session := configured("", "--format", "json", "run")
			exited(session, 1)
			Expect(session.Err).To(gbytes.Say(`almanac::config::env_missing`))
		})

		It("start-up fails with a diagnostic when a provider is missing", func() {
			session := launch([]string{"DATABASE_URL=postgres://unreachable.invalid/none"}, "--format", "json", "run")
			exited(session, 1)
			Expect(session.Err).To(gbytes.Say(`almanac::provider::missing`))
		})

		It("the daemon reports ready, answers health, and stops on SIGTERM", Label("db"), func() {
			sim := simforge.Start()
			DeferCleanup(sim.Close)
			socket := filepath.Join(GinkgoT().TempDir(), "notify")
			notify, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(notify.Close)
			received := func() string {
				GinkgoHelper()
				Expect(notify.SetReadDeadline(time.Now().Add(30 * time.Second))).To(Succeed())
				message := make([]byte, 64)
				n, err := notify.Read(message)
				Expect(err).NotTo(HaveOccurred())
				return string(message[:n])
			}
			address := freeAddress()
			session := launch([]string{
				"DATABASE_URL=" + pgtest.Database(), "GITHUB_TOKEN=t", "ANTHROPIC_API_KEY=k",
				"GITHUB_API_URL=" + sim.URL(), "ALMANAC_REGISTRY_URL=" + unreachable, "ALMANAC_ADVISORIES_URL=" + unreachable,
				"NOTIFY_SOCKET=" + socket, "ALMANAC_LISTEN=" + address,
			}, "run")
			DeferCleanup(session.Kill)

			Expect(received()).To(ContainSubstring("READY=1"))
			Eventually(func() (int, error) {
				response, err := http.Get("http://" + address + "/healthz")
				if err != nil {
					return 0, err
				}
				return response.StatusCode, response.Body.Close()
			}).WithTimeout(30 * time.Second).Should(Equal(http.StatusOK))
			session.Terminate()
			Eventually(session).WithTimeout(20*time.Second).Should(gexec.Exit(0), "it exits cleanly")
			Expect(received()).To(ContainSubstring("STOPPING=1"))
			Expect(session.Err).To(gbytes.Say(`"msg":"stopped cleanly"`))
		})
	})
})

// freeAddress is a loopback address nothing listens on.
func freeAddress() string {
	GinkgoHelper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	address := listener.Addr().String()
	Expect(listener.Close()).To(Succeed())
	return address
}
