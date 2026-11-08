package advisory_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAdvisory(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Advisory Suite")
}
