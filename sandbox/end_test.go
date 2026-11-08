package sandbox_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/sandbox"
	"github.com/ivy/almanac/sandbox/fake"
)

// slowToStop is a sandbox whose destroy takes stopping, or until it is given up on.
type slowToStop struct {
	*fake.Sandbox
	stopping time.Duration
}

func (s slowToStop) Destroy(ctx context.Context, id sandbox.ID) error {
	select {
	case <-time.After(s.stopping):
		return s.Sandbox.Destroy(ctx, id)
	case <-ctx.Done():
		return &sandbox.Error{Op: "destroy", Sandbox: id, Err: ctx.Err()}
	}
}

var _ = Describe("Ending a sandbox", func() {
	var sb slowToStop
	var id sandbox.ID

	BeforeEach(func(ctx SpecContext) {
		sb = slowToStop{Sandbox: fake.New()}
		var err error
		id, err = sb.Create(ctx, sandbox.AgentSpec("image"))
		Expect(err).NotTo(HaveOccurred())
	})

	abandoned := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	// crates/sandbox/src/podman.rs: ABANDON_LIMIT
	It("gives up after 3s once the run is abandoned", func() {
		Expect(sandbox.AbandonLimit).To(Equal(3 * time.Second))
	})

	// The Go half of crates/sandbox/tests/podman.rs a_sandbox_abandoned_by_a_stopping_service_is_gone:
	// End returns only once the sandbox is gone, so a process exiting next cannot cut the stop short.
	It("ends a sandbox whose run was abandoned, and returns once it is gone", func() {
		sb.stopping = 100 * time.Millisecond
		Expect(sandbox.EndWithin(abandoned(), sb, id, time.Second)).To(Succeed())
		Expect(sb.Alive()).To(BeZero())
	})

	It("waits no longer than the limit once the run is abandoned", func() {
		sb.stopping = time.Minute
		began := time.Now()
		err := sandbox.EndWithin(abandoned(), sb, id, 100*time.Millisecond)
		Expect(time.Since(began)).To(BeNumerically("<", 5*time.Second))
		Expect(err).To(MatchError(ContainSubstring("still stopping 100ms after its run was abandoned")))
		Expect(err).To(MatchError(ContainSubstring("ends itself when its lifetime runs out")))
	})

	It("limits a destroy under way when the run is abandoned during it", func() {
		sb.stopping = time.Minute
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		err := sandbox.EndWithin(ctx, sb, id, 100*time.Millisecond)
		Expect(err).To(MatchError(ContainSubstring("still stopping")))
	})

	It("puts no limit on a destroy while the run goes on", func(ctx SpecContext) {
		sb.stopping = 300 * time.Millisecond
		Expect(sandbox.EndWithin(ctx, sb, id, 50*time.Millisecond)).To(Succeed())
		Expect(sb.Alive()).To(BeZero())
	})
})
