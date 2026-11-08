package app_test

import (
	"net"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"

	"github.com/ivy/almanac/internal/pgtest"
)

// The Rust specification is almanacs/renovate/tests/cli.rs and almanacs/renovate/tests/daemon.rs,
// run here against almanac-demo, an almanac binary over the sample workflow models.

// launch runs almanac-demo with args and nothing in its environment but env and a config file
// that does not exist.
func launch(env []string, args ...string) *gexec.Session {
	GinkgoHelper()
	cmd := exec.Command(demo, args...)
	cmd.Env = append([]string{"ALMANAC_CONFIG=" + filepath.Join(GinkgoT().TempDir(), "none.toml")}, env...)
	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	return session
}

var _ = Describe("An almanac binary", func() {
	It("help is not an error", func() {
		session := launch(nil, "--help")
		Eventually(session).Should(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say(`^usage: almanac-demo \[--format json\] synth \| check \| run`))
		Expect(session.Out).To(gbytes.Say(`hello \[NAME\]`))
	})

	It("an unknown verb says what exists, and exits 1", func() {
		session := launch(nil, "frobnicate")
		Eventually(session).Should(gexec.Exit(1))
		Expect(session.Err).To(gbytes.Say(`almanac::usage`))
		Expect(session.Err).To(gbytes.Say(`synth \| check`))
	})

	It("checks the models it was built with, and names a provider it lacks", func() {
		session := launch(nil, "--format", "json", "check")
		Eventually(session).Should(gexec.Exit(1))
		Expect(decoded(string(session.Err.Contents()))).To(And(HaveKeyWithValue("code", "almanac::provider::missing"),
			HaveKeyWithValue("message", "dependency_updates needs a provider for sample.Registry")))

		session = launch([]string{"DEMO_REGISTRY=green"}, "--format", "json", "check")
		Eventually(session).Should(gexec.Exit(0))
		Expect(decoded(string(session.Out.Contents()))).To(And(HaveKeyWithValue("ok", true), HaveKeyWithValue("workflows", 2.0)))
	})

	It("simulate sends a fixture through a model, and a dry run applies nothing", func() {
		session := launch([]string{"DEMO_REGISTRY=green", "ALMANAC_MODE=live"}, "simulate", opened, fixture(pull(1)), "--dry-run")
		Eventually(session).Should(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say(`^simulate: queued\nran dependency_updates \(\S+\): recorded would-apply\n`))
	})

	It("runs its own verbs", func() {
		session := launch(nil, "--format", "json", "hello", "operator")
		Eventually(session).Should(gexec.Exit(0))
		Expect(session.Out.Contents()).To(MatchJSON(`{"hello": "operator", "from": "almanac-demo"}`))
	})

	It("run needs a database", func() {
		session := launch([]string{"DEMO_REGISTRY=green"}, "--format", "json", "run")
		Eventually(session).Should(gexec.Exit(1))
		Expect(session.Err).To(gbytes.Say(`almanac::config::env_missing`))
	})

	It("start-up fails with a diagnostic when a provider is missing", func() {
		session := launch([]string{"DATABASE_URL=postgres://unreachable.invalid/none"}, "--format", "json", "run")
		Eventually(session).Should(gexec.Exit(1))
		Expect(session.Err).To(gbytes.Say(`almanac::provider::missing`))
	})

	It("the daemon reports ready, answers health, and stops on SIGTERM", Label("db"), func() {
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
			"DATABASE_URL=" + pgtest.Database(),
			"DEMO_REGISTRY=green",
			"NOTIFY_SOCKET=" + socket,
			"ALMANAC_LISTEN=" + address,
		}, "run")
		DeferCleanup(session.Kill)

		Expect(received()).To(ContainSubstring("READY=1"))
		healthy(address)
		session.Terminate()
		Eventually(session).WithTimeout(20*time.Second).Should(gexec.Exit(0), "it exits cleanly")
		Expect(received()).To(ContainSubstring("STOPPING=1"))
		Expect(session.Err).To(gbytes.Say(`"msg":"stopped cleanly"`))
	})
})
