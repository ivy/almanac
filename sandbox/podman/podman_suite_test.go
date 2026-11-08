package podman_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPodman(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Podman Suite")
}
