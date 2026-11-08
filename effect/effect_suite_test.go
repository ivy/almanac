package effect_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestEffect(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Effect Suite")
}
