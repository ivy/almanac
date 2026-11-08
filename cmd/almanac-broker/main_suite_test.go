package main_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/ivy/almanac/internal/pgtest"
)

func TestAlmanacBroker(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "almanac-broker Suite")
}

var database = pgtest.Database

// binary is almanac-broker, built once per process.
var binary string

var _ = SynchronizedBeforeSuite(pgtest.Start, func(url []byte) {
	pgtest.Use(url)
	var err error
	binary, err = gexec.Build("github.com/ivy/almanac/cmd/almanac-broker")
	Expect(err).NotTo(HaveOccurred())
})

var _ = SynchronizedAfterSuite(gexec.CleanupBuildArtifacts, pgtest.Stop)
