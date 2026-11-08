package simforge_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSimforge(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Simforge Suite")
}
