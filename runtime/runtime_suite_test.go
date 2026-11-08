package runtime_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/internal/pgtest"
)

func TestRuntime(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Runtime Suite")
}

var _ = SynchronizedBeforeSuite(pgtest.Start, pgtest.Use)
var _ = SynchronizedAfterSuite(func() {}, pgtest.Stop)
