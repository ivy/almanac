package fake_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/sandbox"
	"github.com/ivy/almanac/sandbox/fake"
)

var _ = Describe("The fake sandbox", func() {
	ctx := context.Background()

	It("refuses what a real provider refuses", func() {
		spec := sandbox.AgentSpec("image")
		spec.Egress = sandbox.AllowDomains()
		_, err := fake.New().Create(ctx, spec)
		Expect(errors.Is(err, sandbox.ErrRefused)).To(BeTrue(), "%v", err)
	})

	It("lets a fetch sandbox have egress", func() {
		f := fake.New()
		_, err := f.Create(ctx, sandbox.FetchSpec("image", "crates.io"))
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Specs()[0].Egress.Domains()).To(Equal([]string{"crates.io"}))
	})

	It("tracks its lifetime", func() {
		f := fake.New()
		id, err := f.Create(ctx, sandbox.AgentSpec("image"))
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Alive()).To(Equal(1))
		Expect(f.Destroy(ctx, id)).To(Succeed())
		Expect(f.Alive()).To(Equal(0))
		_, err = f.Exec(ctx, id, "true", time.Second)
		Expect(errors.Is(err, sandbox.ErrNotFound)).To(BeTrue(), "%v", err)
		Expect(f.Destroy(ctx, id)).To(Succeed(), "destroying twice is not an error")
	})

	It("answers commands from its function and records where each ran", func() {
		f := fake.Answering(func(command string) sandbox.Output {
			return sandbox.Output{ExitCode: 3, Stdout: "ran " + command}
		})
		id, err := f.Create(ctx, sandbox.AgentSpec("image"))
		Expect(err).NotTo(HaveOccurred())
		out, err := f.Exec(ctx, id, "make", time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal(sandbox.Output{ExitCode: 3, Stdout: "ran make"}))
		Expect(f.Commands()).To(Equal([]string{"make"}))
		Expect(f.Calls()).To(Equal([]fake.Command{{Sandbox: id, Line: "make"}}))
	})

	It("hands back the last upload, and refuses a path outside the workdir", func() {
		f := fake.New()
		id, err := f.Create(ctx, sandbox.AgentSpec("image"))
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Upload(ctx, id, []byte("first"))).To(Succeed())
		Expect(f.Upload(ctx, id, []byte("second"))).To(Succeed())
		Expect(f.Download(ctx, id, "anything")).To(Equal([]byte("second")))
		_, err = f.Download(ctx, id, "../etc/passwd")
		Expect(errors.Is(err, sandbox.ErrRefused)).To(BeTrue(), "%v", err)
	})
})
