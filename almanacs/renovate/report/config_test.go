package report_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate/report"
	"github.com/ivy/almanac/hostile"
)

func config(text string) report.Config { return report.ConfigOf(hostile.New(text), false) }

func presets(text string) []string {
	var names []string
	for _, slug := range report.PresetsOf(hostile.New(text)) {
		names = append(names, slug.String())
	}
	return names
}

var _ = Describe("A repository's Renovate config", func() {
	It("a JSON5 Renovate config is read", func() {
		c := config("{\n  // Comments and trailing commas are normal here.\n  platformAutomerge: false,\n  automerge: true,\n}\n")
		Expect(c.PlatformAutomerge).To(Equal(report.Off))
		Expect(c.Automerge).To(Equal(report.On))
	})

	It("a config that cannot be read is unknown", func() {
		Expect(config("not config at all {")).To(Equal(report.Config{}))
		Expect(config("")).To(Equal(report.Config{}))
		Expect(config("[true]")).To(Equal(report.Config{}))
	})

	It("reads gitIgnoredAuthors as whether it names anyone", func() {
		Expect(config(`{"gitIgnoredAuthors": ["bot@example.com"]}`).GitIgnoredAuthors).To(Equal(report.On))
		Expect(config(`{"gitIgnoredAuthors": []}`).GitIgnoredAuthors).To(Equal(report.Off))
		Expect(config(`{}`).GitIgnoredAuthors).To(Equal(report.Unset))
	})

	It("keeps every setting it understands beside one it does not", func() {
		c := config(`{"automerge": "yes", "platformAutomerge": false, "gitIgnoredAuthors": "someone", "extends": 7}`)
		Expect(c).To(Equal(report.Config{PlatformAutomerge: report.Off}))
	})

	It("keeps whether the default branch requires Almanac's status", func() {
		Expect(report.ConfigOf(hostile.New("not config"), true).TrustRequired).To(BeTrue())
	})

	It("a shared preset is found by the repository it lives in", func() {
		Expect(presets(`{"extends": ["github>owner/config", "config:recommended"]}`)).To(Equal([]string{"owner/config"}))
		Expect(presets(`{"extends": ["github>owner/config//other"]}`)).To(BeEmpty())
	})

	It("finds a preset's repository past a named preset or a tag", func() {
		Expect(presets(`{"extends": ["github>owner/config:base", "github>owner/tagged#v1"]}`)).To(Equal([]string{"owner/config", "owner/tagged"}))
		Expect(presets(`{"extends": ["github>owner:base", 7, "github>owner/odd+name"]}`)).To(BeEmpty())
	})
})
