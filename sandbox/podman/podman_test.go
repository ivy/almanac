package podman_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/sandbox"
	"github.com/ivy/almanac/sandbox/podman"
)

// image is built by `make go-test-sandbox`, as for the Rust suite.
const image = "localhost/almanac-fixer:test"

// limit is the time limit for a command that should finish at once.
const limit = 20 * time.Second

// These specs run real containers. `make go-test-sandbox` builds the image and
// runs them.
var _ = Describe("A Podman sandbox", Label("sandbox"), func() {
	var p *podman.Sandbox

	BeforeEach(func() {
		p = podman.New()
	})

	started := func(ctx context.Context, spec sandbox.Spec) sandbox.ID {
		id, err := p.Create(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) { Expect(p.Destroy(ctx, id)).To(Succeed()) })
		return id
	}

	run := func(ctx context.Context, id sandbox.ID, command string) sandbox.Output {
		out, err := p.Exec(ctx, id, command, limit)
		Expect(err).NotTo(HaveOccurred())
		return out
	}

	It("lets a session write only inside its checkout and private scratch", func(ctx SpecContext) {
		id := started(ctx, sandbox.AgentSpec(image))
		Expect(run(ctx, id, "echo x > /work/file").ExitCode).To(Equal(0))
		Expect(run(ctx, id, "echo x > /tmp/scratch").ExitCode).To(Equal(0))
		for _, outside := range []string{"/etc/x", "/x", "/usr/x", "/var/tmp/x", "/root/x"} {
			Expect(run(ctx, id, "echo x > "+outside).ExitCode).NotTo(Equal(0), "%s was writable", outside)
		}
	})

	It("does not share scratch space between sandboxes", func(ctx SpecContext) {
		first := started(ctx, sandbox.AgentSpec(image))
		run(ctx, first, "echo secret > /tmp/left-behind")
		second := started(ctx, sandbox.AgentSpec(image))
		out := run(ctx, second, "cat /tmp/left-behind")
		Expect(out.ExitCode).NotTo(Equal(0), "%+v", out)
	})

	It("gives a session no network and no credentials", func(ctx SpecContext) {
		// Credentials in the host's environment must not follow the sandbox in.
		for name, value := range map[string]string{"ANTHROPIC_API_KEY": "sk-host-key", "GITHUB_TOKEN": "host-token"} {
			DeferCleanup(os.Setenv, name, os.Getenv(name))
			Expect(os.Setenv(name, value)).To(Succeed())
		}
		id := started(ctx, sandbox.AgentSpec(image))

		out := run(ctx, id, "wget -q -T 3 -O- http://1.1.1.1/")
		Expect(out.ExitCode).NotTo(Equal(0), "%+v", out)
		Expect(out.Stderr).To(ContainSubstring("unreachable"))

		env := strings.ToLower(run(ctx, id, "env").Stdout)
		Expect(env).NotTo(ContainSubstring("anthropic"))
		Expect(env).NotTo(ContainSubstring("token"))
		Expect(env).NotTo(ContainSubstring("host-key"))

		// Nothing from the host is mounted, so its home does not exist inside.
		home, err := os.UserHomeDir()
		Expect(err).NotTo(HaveOccurred())
		Expect(run(ctx, id, "test -e '"+home+"'").ExitCode).NotTo(Equal(0))
		Expect(run(ctx, id, "echo $HOME").Stdout).To(Equal("/work/.tmp\n"))
	})

	It("runs a session with no privilege", func(ctx SpecContext) {
		id := started(ctx, sandbox.AgentSpec(image))
		Expect(run(ctx, id, "grep CapEff /proc/self/status").Stdout).To(ContainSubstring("0000000000000000"))
		// A user other than root has no effective capabilities anyway. An empty
		// bounding set shows none could be regained either.
		Expect(run(ctx, id, "grep CapBnd /proc/self/status").Stdout).To(ContainSubstring("0000000000000000"))
		Expect(run(ctx, id, "grep NoNewPrivs /proc/self/status").Stdout).To(ContainSubstring("1"))
		Expect(strings.TrimSpace(run(ctx, id, "id -u").Stdout)).To(Equal("1000"))
	})

	It("kills a command over its limit", func(ctx SpecContext) {
		id := started(ctx, sandbox.AgentSpec(image))
		begun := time.Now()
		out, err := p.Exec(ctx, id, "sleep 30", time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(out.TimedOut).To(BeTrue(), "%+v", out)
		Expect(time.Since(begun)).To(BeNumerically("<", 10*time.Second))
	})

	It("moves files only as archives through the API", func(ctx SpecContext) {
		id := started(ctx, sandbox.AgentSpec(image))
		run(ctx, id, "mkdir src && echo hello > src/a.txt && tar -cf /work/t.tar src")
		archive, err := p.Download(ctx, id, "t.tar")
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Destroy(ctx, id)).To(Succeed())

		second := started(ctx, sandbox.AgentSpec(image))
		// Download wraps the file in a tar of its own, so the inner archive
		// arrives as a file.
		Expect(p.Upload(ctx, second, archive)).To(Succeed())
		out := run(ctx, second, "tar -xf t.tar && cat src/a.txt")
		Expect(strings.TrimSpace(out.Stdout)).To(Equal("hello"), "%+v", out)
	})

	It("is gone once destroyed", func(ctx SpecContext) {
		id := started(ctx, sandbox.AgentSpec(image))
		Expect(p.Destroy(ctx, id)).To(Succeed())
		_, err := p.Exec(ctx, id, "true", limit)
		Expect(errors.Is(err, sandbox.ErrNotFound)).To(BeTrue(), "%v", err)
		Expect(errors.Is(p.Upload(ctx, id, nil), sandbox.ErrNotFound)).To(BeTrue())
		_, err = p.Download(ctx, id, "x")
		Expect(errors.Is(err, sandbox.ErrNotFound)).To(BeTrue(), "%v", err)
		Expect(p.Destroy(ctx, id)).To(Succeed(), "destroying twice is not an error")
	})

	It("refuses to give an agent sandbox egress", func(ctx SpecContext) {
		spec := sandbox.AgentSpec(image)
		spec.Egress = sandbox.AllowDomains("example.com")
		_, err := p.Create(ctx, spec)
		Expect(errors.Is(err, sandbox.ErrRefused)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("no network access"))
	})

	It("ends an abandoned sandbox by itself", func(ctx SpecContext) {
		spec := sandbox.AgentSpec(image)
		spec.Lifetime = 2 * time.Second
		// Never destroyed, as if its owner had been killed.
		id, err := p.Create(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() error {
			return exec.CommandContext(ctx, "podman", "container", "exists", string(id)).Run()
		}).WithTimeout(30*time.Second).WithPolling(250*time.Millisecond).Should(HaveOccurred(), "the container still exists")
		_, err = p.Exec(ctx, id, "true", limit)
		Expect(errors.Is(err, sandbox.ErrNotFound)).To(BeTrue(), "%v", err)
	})

	It("does not mistake a command's exit code 125 for a missing sandbox", func(ctx SpecContext) {
		id := started(ctx, sandbox.AgentSpec(image))
		out, err := p.Exec(ctx, id, "echo out; exit 125", limit)
		Expect(err).NotTo(HaveOccurred())
		Expect(out.ExitCode).To(Equal(125))
		Expect(out.Stdout).To(Equal("out\n"))
	})

	It("keeps only the end of output too long to hold, and says so", func(ctx SpecContext) {
		id := started(ctx, sandbox.AgentSpec(image))
		out := run(ctx, id, "head -c 20000000 /dev/zero | tr '\\0' x; echo; echo end")
		Expect(out.Truncated).To(BeTrue())
		Expect(out.Stdout).To(HaveLen(podman.OutputLimit))
		Expect(out.Stdout).To(HaveSuffix("x\nend\n"))
	})

	It("gives a fetch sandbox a network, and an agent sandbox none", func(ctx SpecContext) {
		// Read from the container's own network namespace, so no traffic is sent.
		interfaces := "ls /sys/class/net"
		agent := started(ctx, sandbox.AgentSpec(image))
		Expect(strings.Fields(run(ctx, agent, interfaces).Stdout)).To(Equal([]string{"lo"}))
		fetch := started(ctx, sandbox.FetchSpec(image))
		Expect(len(strings.Fields(run(ctx, fetch, interfaces).Stdout))).To(BeNumerically(">", 1))
	})

	It("builds with no network from dependencies fetched in a sandbox of their own", func(ctx SpecContext) {
		env := map[string]string{"CARGO_HOME": "/work/.cargo"}
		fetching := sandbox.FetchSpec(image)
		fetching.Env = env
		deps, err := sandbox.FetchDependencies(ctx, p, fetching, project())
		Expect(err).NotTo(HaveOccurred())

		agent := sandbox.AgentSpec(image)
		agent.Env = env
		id := started(ctx, agent)
		Expect(sandbox.Install(ctx, p, id, deps)).To(Succeed())
		Expect(sandbox.Unpack(ctx, p, id, project())).To(Succeed())
		Expect(run(ctx, id, "test -d .cargo && test -d .cache && test ! -e .dependencies.tar && test ! -e .almanac").ExitCode).To(Equal(0))

		out, err := p.Exec(ctx, id, "cargo build --offline --locked 2>&1 && ./target/debug/sample", 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(out.ExitCode).To(Equal(0), "%+v", out)
		Expect(out.Stdout).To(HaveSuffix("42\n"))
	})
})

// project is a checkout as a forge serves one: a gzipped tar with one top
// directory. It has no dependencies, so fetching it needs no live registry.
func project() []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	w := tar.NewWriter(gz)
	for name, text := range map[string]string{
		"owner-repo-abc/Cargo.toml":  "[package]\nname = \"sample\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"owner-repo-abc/Cargo.lock":  "version = 4\n\n[[package]]\nname = \"sample\"\nversion = \"0.1.0\"\n",
		"owner-repo-abc/src/main.rs": "fn main() {\n    println!(\"{}\", 42);\n}\n",
	} {
		Expect(w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(text))})).To(Succeed())
		_, err := w.Write([]byte(text))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(w.Close()).To(Succeed())
	Expect(gz.Close()).To(Succeed())
	return buf.Bytes()
}
