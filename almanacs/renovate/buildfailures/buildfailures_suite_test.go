package buildfailures_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBuildFailures(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Build Failures Suite")
}
