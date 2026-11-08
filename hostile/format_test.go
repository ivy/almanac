package hostile_test

import (
	"encoding"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/hostile"
)

var _ = Describe("Format", func() {
	repo := hostile.MustParseSlug("octo-org/hello")

	It("holds a literal format until a run has the values for it", func() {
		format := hostile.NewFormat("%s failed %d times")

		Expect(format.Title(repo, 3).String()).To(Equal("octo-org/hello failed 3 times"))
		Expect(format.String()).To(Equal("%s failed %d times"))
	})

	It("makes the same title Titlef makes", func() {
		Expect(hostile.NewFormat("%s at %v").Title(repo, Concerns)).To(Equal(hostile.Titlef("%s at %v", repo, Concerns)))
	})

	It("is stored as the text it was written as, and cannot be read back from data", func() {
		stored, err := json.Marshal(hostile.NewFormat("Repairs did not fix %s"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(stored)).To(Equal(`"Repairs did not fix %s"`))

		_, decodes := any(&hostile.Format{}).(encoding.TextUnmarshaler)
		Expect(decodes).To(BeFalse())
	})

	DescribeTable("panics on a value that could hold free text, without quoting it",
		func(value any) {
			Expect(func() { hostile.NewFormat("%v").Title(value) }).To(PanicWith(And(
				ContainSubstring("a title holds only literals and clean values"),
				Not(ContainSubstring("IGNORE")),
			)))
		},
		Entry("a string", injection),
		Entry("a hostile value", hostile.New(injection)),
		Entry("a byte slice", []byte(injection)),
	)

	It("fills a format for an interpreter, refusing a value that is not clean with an error", func() {
		title, err := hostile.NewFormat("%s failed %d times").Fill([]any{repo, 3})
		Expect(err).NotTo(HaveOccurred())
		Expect(title).To(Equal(hostile.Titlef("%s failed %d times", repo, 3)))

		_, err = hostile.NewFormat("%v").Fill([]any{hostile.New(injection)})
		Expect(err).To(MatchError(And(ContainSubstring("a title holds only literals and clean values"), Not(ContainSubstring("IGNORE")))))
	})
})
