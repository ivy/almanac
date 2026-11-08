package podman_test

import (
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/sandbox"
	"github.com/ivy/almanac/sandbox/podman"
)

// These specs read the command line and need no Podman. The ones labelled
// sandbox prove the same properties in a real container.
var _ = Describe("The podman command line", func() {
	It("names a sandbox after the process that made it, so a supervisor can stop what a killed process left", func() {
		first, err := podman.NewID()
		Expect(err).NotTo(HaveOccurred())
		second, err := podman.NewID()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(first)).To(MatchRegexp(`^almanac-%d-[0-9a-f]{16}$`, os.Getpid()))
		Expect(first).NotTo(Equal(second))
	})

	It("gives an agent sandbox no network, no capabilities and no privilege", func() {
		args := podman.CreateArgs("almanac-x", sandbox.AgentSpec("image"))
		Expect(args).To(ContainElements(
			"--network=none", "--cap-drop=all", "--security-opt=no-new-privileges",
			"--read-only", "--read-only-tmpfs=false",
		))
		Expect(strings.Join(args, " ")).To(ContainSubstring("--user 1000:1000"))
		Expect(strings.Join(args, " ")).To(ContainSubstring("--rm"))
	})

	It("mounts nothing from the host", func() {
		args := strings.Join(podman.CreateArgs("almanac-x", sandbox.AgentSpec("image")), " ")
		Expect(args).NotTo(ContainSubstring("--volume"))
		Expect(args).NotTo(ContainSubstring("--mount"))
		Expect(args).NotTo(MatchRegexp(`(^| )-v `))
		Expect(args).To(ContainSubstring("--tmpfs /work:rw,exec,size=4096m,mode=0755,U"))
		Expect(args).To(ContainSubstring("--tmpfs /tmp:rw,size=1024m,mode=1777"))
	})

	It("sets every limit from the spec", func() {
		spec := sandbox.AgentSpec("image")
		spec.MemoryMB, spec.CPUs, spec.PIDs = 512, 1, 64
		Expect(podman.CreateArgs("almanac-x", spec)).To(ContainElements("--memory=512m", "--cpus=1", "--pids-limit=64"))
	})

	It("puts a fetch sandbox on the default network", func() {
		Expect(podman.CreateArgs("almanac-x", sandbox.FetchSpec("image"))).NotTo(ContainElement("--network=none"))
	})

	It("keeps HOME and TMPDIR in scratch space, whatever the spec's env says", func() {
		spec := sandbox.AgentSpec("image")
		spec.Env = map[string]string{"HOME": "/work", "CARGO_HOME": "/work/.cargo"}
		args := strings.Join(podman.CreateArgs("almanac-x", spec), " ")
		Expect(args).To(ContainSubstring("--env CARGO_HOME=/work/.cargo"))
		Expect(args).To(HaveSuffix("--env TMPDIR=/work/.tmp --env HOME=/work/.tmp image sleep 7200"))
	})

	It("ends the sandbox's main process after its lifetime", func() {
		spec := sandbox.AgentSpec("image")
		spec.Lifetime = 90 * time.Second
		args := podman.CreateArgs("almanac-x", spec)
		Expect(args[len(args)-3:]).To(Equal([]string{"image", "sleep", "90"}))
	})

	It("keeps the end of a stream too long to hold, and says so", func() {
		t := podman.NewTail(4)
		for _, chunk := range []string{"ab", "cdef", "gh", "ijklmnop", "q"} {
			_, err := t.Write([]byte(chunk))
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(t.String()).To(Equal("nopq"))
		Expect(t.Truncated()).To(BeTrue())

		short := podman.NewTail(4)
		_, _ = short.Write([]byte("abcd"))
		Expect(short.String()).To(Equal("abcd"))
		Expect(short.Truncated()).To(BeFalse())
	})
})
