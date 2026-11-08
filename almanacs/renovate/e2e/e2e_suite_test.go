package e2e_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/ivy/almanac/internal/pgtest"
)

func TestEndToEnd(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Renovate End-to-End Suite")
}

// The binaries under test, built once per process.
var almanac, broker string

var _ = SynchronizedBeforeSuite(pgtest.Start, func(url []byte) {
	pgtest.Use(url)
	if !Label("e2e").MatchesLabelFilter(GinkgoLabelFilter()) {
		return
	}
	var err error
	almanac, err = gexec.Build("github.com/ivy/almanac/almanacs/renovate/cmd/almanac-renovate")
	Expect(err).NotTo(HaveOccurred())
	broker, err = gexec.Build("github.com/ivy/almanac/cmd/almanac-broker")
	Expect(err).NotTo(HaveOccurred())
})

var _ = SynchronizedAfterSuite(gexec.CleanupBuildArtifacts, pgtest.Stop)
