package hostile_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/hostile"
)

var _ = Describe("Titlef", func() {
	It("formats numbers, bools, validated text, titles and closed values", func() {
		repo := hostile.MustParseSlug("octo-org/hello")
		sha := hostile.MustParseSha("0123456789abcdef0123456789abcdef01234567")
		inner := hostile.Titlef("build")

		title := hostile.Titlef("%s %v at %s in %s: %d, %.1f, %t", inner, Concerns, sha, repo, -2, 0.5, true)

		Expect(title.String()).To(Equal("build concerns at 0123456789abcdef0123456789abcdef01234567 in octo-org/hello: -2, 0.5, true"))
	})

	DescribeTable("panics on a value that could hold free text, without quoting it",
		func(value any) {
			Expect(func() { hostile.Titlef("%v", value) }).To(PanicWith(And(
				ContainSubstring("a title holds only literals and clean values"),
				Not(ContainSubstring("IGNORE")),
			)))
		},
		Entry("a string", injection),
		Entry("a hostile value", hostile.New(injection)),
		Entry("a byte slice", []byte(injection)),
		Entry("an error", errInjection{}),
		Entry("a nil pointer", (*hostile.Slug)(nil)),
	)
})

type errInjection struct{}

func (errInjection) Error() string { return injection }
