package hostile_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHostile(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Hostile Suite")
}
