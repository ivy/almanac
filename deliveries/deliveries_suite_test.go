package deliveries_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/internal/pgtest"
)

func TestDeliveries(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Deliveries Suite")
}

var _ = SynchronizedBeforeSuite(pgtest.Start, pgtest.Use)
var _ = SynchronizedAfterSuite(func() {}, pgtest.Stop)

var database = pgtest.Database
