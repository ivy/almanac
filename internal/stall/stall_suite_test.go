package stall_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestStall(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Stall Suite")
}
