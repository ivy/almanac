package renovate_test

import (
	"encoding/base64"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/reveal"
	"github.com/ivy/almanac/patch"
)

// holds answers the predicate registered as name, as a run's condition does.
func holds(name string, args ...any) bool {
	return holdsIs(name, "", args...)
}

func holdsIs(name, is string, args ...any) bool {
	entry, ok := renovate.Vocabulary.Lookup(name)
	Expect(ok).To(BeTrue(), name)
	yes, err := entry.Holds(args, is)
	Expect(err).NotTo(HaveOccurred())
	return yes
}

// shown is a hostile fact as the map registered as name shows it to a person.
func shown(name string, arg any) string {
	entry, ok := renovate.Vocabulary.Lookup(name)
	Expect(ok).To(BeTrue(), name)
	text, err := entry.Show(arg)
	Expect(err).NotTo(HaveOccurred())
	return reveal.Value(text)
}

func writing(path, content string) patch.Change {
	return patch.Change{Op: patch.Write, Path: path, Content: base64.StdEncoding.EncodeToString([]byte(content))}
}

var _ = Describe("The predicates", func() {
	renovateBranch := hostile.MustParseSlug("renovate/lodash-4.x")

	DescribeTable("a build needs attention when CI failed on a Renovate branch, or finished on a patch",
		func(event renovate.BuildEvent, want bool) {
			Expect(holds("renovate.needs_attention", event)).To(Equal(want))
		},
		Entry("a failure on a Renovate branch", renovate.BuildEvent{Branch: renovateBranch, Failed: true, Finished: true}, true),
		Entry("a pass on a patched commit", renovate.BuildEvent{Branch: renovateBranch, Finished: true, Patched: true}, true),
		Entry("a pass", renovate.BuildEvent{Branch: renovateBranch, Finished: true}, false),
		Entry("a patched commit still building", renovate.BuildEvent{Branch: renovateBranch, Patched: true}, false),
		Entry("a failure on another branch", renovate.BuildEvent{Branch: hostile.MustParseSlug("main"), Failed: true, Finished: true}, false),
		Entry("Almanac's own status", renovate.BuildEvent{Branch: renovateBranch, Failed: true, Finished: true, Bookkeeping: true}, false),
	)

	DescribeTable("a pull request is not actionable when it is gone, conflicted, or still running",
		func(pull renovate.Pull, want bool) {
			Expect(holds("renovate.not_actionable", pull)).To(Equal(want))
		},
		Entry("gone", renovate.Pull{}, true),
		Entry("conflicted", renovate.Pull{Found: true, Conflicted: true}, true),
		Entry("running", renovate.Pull{Found: true, Running: true}, true),
		Entry("failing", renovate.Pull{Found: true, Failed: true}, false),
	)

	It("reads failing and a passed repair from the pull request", func() {
		Expect(holds("renovate.failing", renovate.Pull{Failed: true})).To(BeTrue())
		Expect(holds("renovate.repair_passed", renovate.Pull{RepairPassed: true})).To(BeTrue())
		Expect(holds("renovate.repair_passed", renovate.Pull{})).To(BeFalse())
	})

	DescribeTable("a patch undoes an update when it takes back a line the update added outside a lockfile",
		func(changes []patch.Change, want bool) {
			added := hostile.New([]renovate.Line{
				{Path: "package.json", Line: `  "lodash": "4.17.21",`},
				{Path: "package-lock.json", Line: `  "version": "4.17.21",`},
			})
			Expect(holds("renovate.undoes_update", hostile.New(patch.Patch{Changes: changes}), added)).To(Equal(want))
		},
		Entry("a change elsewhere", []patch.Change{writing("src/index.js", "fixed\n")}, false),
		Entry("a manifest that keeps the line", []patch.Change{writing("package.json", "{\n  \"lodash\": \"4.17.21\",\n}\n")}, false),
		Entry("a regenerated lockfile", []patch.Change{writing("package-lock.json", "{}\n")}, false),
		Entry("a manifest that drops the line", []patch.Change{writing("package.json", "{\n  \"lodash\": \"4.17.20\",\n}\n")}, true),
		Entry("a deleted manifest", []patch.Change{{Op: patch.Delete, Path: "package.json"}}, true),
	)

	DescribeTable("a pull request is a security update by its label or its branch name, on a Renovate branch",
		func(branch string, labeled, want bool) {
			event := renovate.SecurityEvent{Branch: hostile.MustParseSlug(branch), Labeled: labeled}
			Expect(holds("renovate.is_security", event)).To(Equal(want))
		},
		Entry("labelled", "renovate/npm-lodash", true, true),
		Entry("named for a vulnerability", "renovate/npm-lodash-vulnerability", false, true),
		Entry("neither", "renovate/npm-lodash", false, false),
		Entry("labelled, on someone else's branch", "feature/lodash", true, false),
	)

	It("reads the security pull request, the advisory and the diff", func() {
		Expect(holds("renovate.open", renovate.SecurityView{Found: true})).To(BeTrue())
		Expect(holds("renovate.tested", renovate.SecurityView{Tested: true})).To(BeTrue())
		Expect(holds("renovate.untestable", renovate.SecurityView{Untestable: true})).To(BeTrue())
		Expect(holds("renovate.names_the_fix", renovate.Advisory{NamesTheFix: true})).To(BeTrue())
		Expect(holds("renovate.readable", renovate.PackageDiff{Readable: true})).To(BeTrue())
		Expect(holds("renovate.truncated", renovate.PackageDiff{Truncated: true})).To(BeTrue())
		Expect(holds("renovate.adds_install_script", renovate.PackageDiff{InstallScriptAdded: true})).To(BeTrue())
		Expect(holds("renovate.hides_files", hostile.New("blob.bin"))).To(BeTrue())
		Expect(holds("renovate.hides_files", hostile.New(""))).To(BeFalse())
	})
})

var _ = Describe("The reviewer's verdict", func() {
	review := func(answer string) hostile.Of[renovate.Review] {
		decoded, err := hostile.Decode[renovate.Review](hostile.New(json.RawMessage(answer)))
		Expect(err).NotTo(HaveOccurred())
		return decoded
	}

	It("is read from the answer reviewer.md asks for", func() {
		Expect(holdsIs("reviewer.verdict", "concerns", review(`{"verdict": "Concerns", "notes": "it phones home"}`))).To(BeTrue())
		Expect(holdsIs("reviewer.verdict", "no_concerns", review(`{"verdict": "NoConcerns", "notes": "a fix"}`))).To(BeTrue())
		Expect(holdsIs("reviewer.verdict", "concerns", review(`{"verdict": "NoConcerns", "notes": "a fix"}`))).To(BeFalse())
		Expect(renovate.RaisedConcerns.Is()).To(Equal("concerns"))
	})

	It("refuses any verdict but the two it may give", func() {
		_, err := hostile.Decode[renovate.Review](hostile.New(json.RawMessage(`{"verdict": "Approved", "notes": "merge it"}`)))
		Expect(err).To(HaveOccurred())
	})

	It("shows the notes to a person, still hostile", func() {
		Expect(shown("reviewer.notes", review(`{"verdict": "Concerns", "notes": "it phones home"}`))).To(Equal("it phones home"))
	})
})

var _ = Describe("A patch shown to a person", func() {
	It("is its diff, or what the fixer said when it finished", func() {
		summary := hostile.New(patch.Patch{Diff: "--- a\n+++ b\n", Answer: json.RawMessage(`{"summary": "nothing to change"}`)})
		Expect(shown("renovate.patch_diff", summary)).To(Equal("--- a\n+++ b\n"))
		Expect(shown("renovate.closing_words", summary)).To(Equal("nothing to change"))
		Expect(shown("renovate.closing_words", hostile.New(patch.Patch{Answer: json.RawMessage(`"I gave up"`)}))).To(Equal("I gave up"))
		Expect(shown("renovate.closing_words", hostile.New(patch.Patch{Answer: json.RawMessage(`{"other": 1}`)}))).To(Equal(`{"other": 1}`))
	})
})
