package sandbox_test

import (
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/sandbox"
)

func refused(spec sandbox.Spec) string {
	err := spec.Validate()
	Expect(errors.Is(err, sandbox.ErrRefused)).To(BeTrue(), "%v", err)
	return err.Error()
}

var _ = Describe("A sandbox spec", func() {
	It("starts with no network for an agent, and every limit set", func() {
		spec := sandbox.AgentSpec("image")
		Expect(spec.Purpose).To(Equal(sandbox.Agent))
		Expect(spec.Egress.None()).To(BeTrue())
		Expect(spec.Validate()).To(Succeed())
	})

	It("refuses an agent sandbox with any egress", func() {
		spec := sandbox.AgentSpec("image")
		spec.Egress = sandbox.AllowDomains("example.com")
		Expect(refused(spec)).To(ContainSubstring("no network access"))

		// An empty allow-list is still egress: Podman gives it the default network.
		spec.Egress = sandbox.AllowDomains()
		Expect(refused(spec)).To(ContainSubstring("no network access"))
	})

	It("is an agent sandbox with no egress when left at its zero value", func() {
		var spec sandbox.Spec
		Expect(spec.Purpose).To(Equal(sandbox.Agent))
		Expect(spec.Egress.None()).To(BeTrue())
		Expect(refused(spec)).To(ContainSubstring("no image"))
	})

	It("allows a fetch sandbox its domains", func() {
		spec := sandbox.FetchSpec("image", "crates.io", "proxy.golang.org")
		Expect(spec.Validate()).To(Succeed())
		Expect(spec.Egress.Domains()).To(Equal([]string{"crates.io", "proxy.golang.org"}))
	})

	DescribeTable("refuses a limit left unset, since hostile code runs here",
		func(unset func(*sandbox.Spec), named string) {
			spec := sandbox.AgentSpec("image")
			unset(&spec)
			Expect(refused(spec)).To(ContainSubstring(named))
		},
		Entry("memory", func(s *sandbox.Spec) { s.MemoryMB = 0 }, "MemoryMB"),
		Entry("cpus", func(s *sandbox.Spec) { s.CPUs = 0 }, "CPUs"),
		Entry("processes", func(s *sandbox.Spec) { s.PIDs = -1 }, "PIDs"),
		Entry("workdir size", func(s *sandbox.Spec) { s.WorkMB = 0 }, "WorkMB"),
		Entry("lifetime", func(s *sandbox.Spec) { s.Lifetime = 0 }, "Lifetime"),
		Entry("lifetime under a second", func(s *sandbox.Spec) { s.Lifetime = time.Millisecond }, "Lifetime"),
	)

	It("refuses an environment variable name that would smuggle in another", func() {
		spec := sandbox.AgentSpec("image")
		spec.Env = map[string]string{"A=B": "c"}
		Expect(refused(spec)).To(ContainSubstring(`"A=B"`))
	})
})

var _ = Describe("A path in a sandbox", func() {
	DescribeTable("stays inside the workdir",
		func(path, clean string) {
			Expect(sandbox.CheckPath(path)).To(Equal(clean))
		},
		Entry("a file", "t.tar", "t.tar"),
		Entry("a nested file", "src/./a.txt", "src/a.txt"),
		Entry("a dotfile", ".dependencies.tar", ".dependencies.tar"),
		Entry("a path that comes back in", "src/../a.txt", "a.txt"),
	)

	DescribeTable("is refused when it leaves the workdir",
		func(path string) {
			_, err := sandbox.CheckPath(path)
			Expect(errors.Is(err, sandbox.ErrRefused)).To(BeTrue(), "%v", err)
		},
		Entry("absolute", "/etc/passwd"),
		Entry("parent", ".."),
		Entry("escaping", "src/../../etc/passwd"),
		Entry("empty", ""),
	)
})
