package version_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/version"
)

var _ = Describe("Version", func() {
	It("reports dev when the linker set nothing", func() {
		Expect(version.String()).To(Equal("dev"))
	})
})
