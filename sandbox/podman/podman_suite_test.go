package podman_test

import (
	"context"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/sandbox"
	"github.com/ivy/almanac/sandbox/podman"
)

// abandoning names a sandbox for this test binary to end as an abandoned run would, then exit.
const abandoning = "ALMANAC_TEST_ABANDON"

func TestMain(m *testing.M) {
	if name, ok := os.LookupEnv(abandoning); ok {
		abandonThenExit(sandbox.ID(name))
	}
	os.Exit(m.Run())
}

// abandonThenExit is the process half of the spec of a sandbox abandoned by a stopping service:
// it ends the sandbox under a run context that has already ended, and exits at once.
func abandonThenExit(id sandbox.ID) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sandbox.End(ctx, podman.New(), id); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(0)
}

func TestPodman(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Podman Suite")
}
