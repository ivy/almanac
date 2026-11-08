package vocab_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestVocab(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Vocab Suite")
}
