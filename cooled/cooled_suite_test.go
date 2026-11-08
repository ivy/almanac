package cooled_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCooled(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Cooled Suite")
}
