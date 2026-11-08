package renovate_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/buildfailures"
	"github.com/ivy/almanac/check"
	"github.com/ivy/almanac/internal/fixture/workflows"
	"github.com/ivy/almanac/vocab"
)

var _ = Describe("The Renovate vocabulary", func() {
	It("registers every part without a problem", func() {
		Expect(renovate.Vocabulary.Err()).NotTo(HaveOccurred())
	})

	It("labels every part it adds in words, not code", func() {
		builtIn := map[string]bool{}
		for _, entry := range vocab.New().Entries() {
			builtIn[entry.Name] = true
		}
		for _, entry := range renovate.Vocabulary.Entries() {
			if builtIn[entry.Name] {
				continue
			}
			Expect(entry.Label).NotTo(BeEmpty(), entry.Name)
			Expect(entry.Label).NotTo(ContainSubstring("_"), entry.Name)
			Expect(entry.Label).NotTo(ContainSubstring("`"), entry.Name)
			Expect(entry.Label).To(Equal(strings.TrimSpace(entry.Label)), entry.Name)
		}
	})

	It("names the providers each reading needs, so a binary missing one fails when it starts", func() {
		needs := func(name string) []string {
			entry, ok := renovate.Vocabulary.Lookup(name)
			Expect(ok).To(BeTrue(), name)
			var names []string
			for _, need := range entry.Needs {
				names = append(names, need.Name())
			}
			return names
		}
		Expect(needs("renovate.pull_for_branch")).To(ConsistOf("*github.Client", "renovate.Target"))
		Expect(needs("renovate.advisory")).To(ConsistOf("advisory.Advisories"))
		Expect(needs("renovate.newest_release")).To(ConsistOf("registry.Registry"))
		Expect(needs("renovate.failing_pulls")).To(ConsistOf("*github.Client", "renovate.Target", "registry.Registry", "clock.Clock"))
	})

	// security_fixes is checked as the fixture writes it, which names the same parts, until it
	// is written against this vocabulary.
	It("holds the parts the Renovate workflows are written with, as they are written", func() {
		Expect(check.Check(renovate.Vocabulary, buildfailures.Model)).To(Succeed())
		Expect(check.Check(renovate.Vocabulary, workflows.SecurityFixes)).To(Succeed())
	})

	It("declares its agents with the prompts the Rust almanac uses", func() {
		manifest, err := check.Manifest(renovate.Vocabulary, buildfailures.Model)
		Expect(err).NotTo(HaveOccurred())
		Expect(manifest.Agents).To(HaveLen(1))
		Expect(manifest.Agents[0].Name).To(Equal("fixer"))
		Expect(manifest.Agents[0].Prompt).To(Equal("fixer.md"))
		Expect(manifest.Agents[0].PromptVars).To(Equal([]string{"repo"}))
		Expect(manifest.Agents[0].Grant.Write).To(BeTrue())

		manifest, err = check.Manifest(renovate.Vocabulary, workflows.SecurityFixes)
		Expect(err).NotTo(HaveOccurred())
		Expect(manifest.Agents[0].Name).To(Equal("reviewer"))
		Expect(manifest.Agents[0].Prompt).To(Equal("reviewer.md"))
		Expect(manifest.Agents[0].Grant.Write).To(BeFalse())
	})
})
