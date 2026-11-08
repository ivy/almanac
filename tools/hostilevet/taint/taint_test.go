package taint_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/ivy/almanac/tools/hostilevet/taint"
)

// Each package under testdata marks what hostilevet must report with a want comment, and
// anything else it reports fails the spec.
var _ = Describe("hostilevet", func() {
	testdata, err := filepath.Abs("testdata")
	if err != nil {
		panic(err)
	}

	DescribeTable("reports exactly what each case marks",
		func(pkg string) {
			analysistest.Run(GinkgoT(), testdata, taint.Analyzer, pkg)
		},
		Entry("formatting a hostile value with fmt or log", "example.com/cases/format"),
		Entry("functions given to Map, Narrow and Classify, and what they do with the text", "example.com/cases/narrowing"),
		Entry("revealing or reading the tag outside a boundary", "example.com/cases/boundary"),
		Entry("values of a closed type outside its declared set", "example.com/cases/closed"),
		Entry("unclean values in a title", "example.com/cases/title"),
		Entry("events, readings, answers, inputs and prompt variables that could carry free text", "example.com/cases/shapes"),
	)

	It("accepts the holes docs/safety-rules.md lists, so that closing one shows here", func() {
		analysistest.Run(GinkgoT(), testdata, taint.Analyzer, "example.com/cases/holes")
	})

	It("reports nothing in package hostile and the packages under it, which implement the rules", func() {
		analysistest.Run(GinkgoT(), testdata, taint.Analyzer, "github.com/ivy/almanac/hostile/...")
	})

	It("lets only the listed boundaries reveal hostile text", func() {
		Expect(taint.MayReveal("github.com/ivy/almanac/github")).To(BeTrue())
		Expect(taint.MayReveal("github.com/ivy/almanac/patch")).To(BeTrue())
		Expect(taint.MayReveal("github.com/ivy/almanac/github_test")).To(BeTrue())
		Expect(taint.MayReveal("github.com/ivy/almanac/github/events")).To(BeFalse())
		Expect(taint.MayReveal("github.com/ivy/almanac/workflow")).To(BeFalse())
	})
})
