package app_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/ivy/almanac/internal/pgtest"
)

func TestApp(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "App Suite")
}

// demo is almanac-demo, built once per process.
var demo string

var _ = SynchronizedBeforeSuite(pgtest.Start, func(url []byte) {
	pgtest.Use(url)
	var err error
	demo, err = gexec.Build("github.com/ivy/almanac/app/internal/almanac-demo")
	Expect(err).NotTo(HaveOccurred())
})

var _ = SynchronizedAfterSuite(gexec.CleanupBuildArtifacts, pgtest.Stop)
