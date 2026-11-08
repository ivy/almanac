package compare_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate/compare"
	"github.com/ivy/almanac/diag"
)

// fixture is a report in testdata, read.
func fixture(name string) compare.Report {
	GinkgoHelper()
	path := filepath.Join("testdata", name)
	text, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	found, err := compare.ReadReport(path, text)
	Expect(err).NotTo(HaveOccurred())
	return found
}

// pull is one pull request's comparison.
func pull(s compare.States, name string) compare.PullStates {
	GinkgoHelper()
	for _, p := range s.Pulls {
		if p.Pull == name {
			return p
		}
	}
	Fail(name + " is not in the comparison")
	return compare.PullStates{}
}

var _ = Describe("Comparing two reports' states", func() {
	It("prints identical reports as agreeing, however each is laid out", func() {
		same := compare.CompareStates(fixture("rust.json"), fixture("go-same.json"))
		Expect(same.Differs()).To(BeFalse())
		Expect(same.Totals).To(Equal(map[compare.Agreement]int{
			compare.Agree: 7, compare.Differ: 0, compare.RustInProgress: 0, compare.OnlyRust: 0, compare.OnlyGo: 0,
		}))
		for _, p := range same.Pulls {
			Expect(p.Agreement).To(Equal(compare.Agree), p.Pull)
			Expect(p.Differences).To(BeEmpty(), p.Pull)
		}
		Expect(compare.RenderStates(same)).To(Equal(
			"agree             owner/alpha#1  mergeable  renovate/left-pad-2.x  nothing is in its way (note: automerge is off, so this will not merge itself)\n" +
				"agree             owner/beta#1  needs-person  renovate/tar-7.x  nothing says when this update was published\n" +
				"agree             owner/beta#2  waiting  renovate/ansi-5.x  Renovate to rebase it\n" +
				"agree             owner/beta#3  mergeable  renovate/no-ci-1.x  nothing is in its way (note: no checks ran on this pull request)\n" +
				"agree             owner/beta#4  in-progress  renovate/glob-10.x  Almanac is validating it\n" +
				"agree             owner/beta#9  unreadable  its branch or head is outside what Almanac accepts\n" +
				"agree             owner/gamma#1  waiting  renovate/minimist-1.x  CI to finish\n" +
				"3 repositories scanned by both; 7 pull requests: 7 agree, 0 differ, 0 rust-in-progress, 0 only-rust, 0 only-go\n"))
	})

	Describe("two reports that differ", func() {
		var differs compare.States
		BeforeEach(func() {
			differs = compare.CompareStates(fixture("rust.json"), fixture("go-differs.json"))
		})

		It("marks a different state, and the reason that comes with it", func() {
			p := pull(differs, "owner/beta#1")
			Expect(p.Agreement).To(Equal(compare.Differ))
			Expect(p.Differences).To(Equal([]string{"state", "reason"}))
			Expect(p.Rust.State).To(Equal("needs-person"))
			Expect(p.Go.State).To(Equal("in-progress"))
		})

		It("marks a different reason for the same state", func() {
			Expect(pull(differs, "owner/beta#2").Differences).To(Equal([]string{"reason"}))
		})

		It("marks a note one report gives and the other does not", func() {
			p := pull(differs, "owner/alpha#1")
			Expect(p.Agreement).To(Equal(compare.Differ))
			Expect(p.Differences).To(Equal([]string{"note"}))
		})

		It("marks a different branch for the same pull request", func() {
			Expect(pull(differs, "owner/beta#3").Differences).To(Equal([]string{"branch"}))
		})

		It("marks a pull request one report could not read and the other placed", func() {
			p := pull(differs, "owner/beta#9")
			Expect(p.Agreement).To(Equal(compare.Differ))
			Expect(p.Differences).To(ContainElement("state"))
			Expect(p.Rust.State).To(Equal("unreadable"))
			Expect(p.Go.State).To(Equal("waiting"))
		})

		It("lists a pull request present in one report only", func() {
			onlyRust := pull(differs, "owner/gamma#1")
			Expect(onlyRust.Agreement).To(Equal(compare.OnlyRust))
			Expect(onlyRust.Go).To(BeNil())
			onlyGo := pull(differs, "owner/delta#1")
			Expect(onlyGo.Agreement).To(Equal(compare.OnlyGo))
			Expect(onlyGo.Rust).To(BeNil())
			Expect(onlyGo.Go.State).To(Equal("blocked-by-config"))
		})

		It("names a pull request the Rust version is working on now, and does not count it", func() {
			p := pull(differs, "owner/beta#4")
			Expect(p.Agreement).To(Equal(compare.RustInProgress))
			Expect(p.Differences).To(Equal([]string{"state", "reason"}))
		})

		It("marks a different count of repositories scanned", func() {
			Expect(differs.RepositoriesScanned).To(Equal(compare.Scanned{Rust: 3, Go: 4}))
			Expect(differs.Differs()).To(BeTrue())
		})

		It("totals every kind, and prints both standings wherever they differ", func() {
			Expect(differs.Totals).To(Equal(map[compare.Agreement]int{
				compare.Agree: 0, compare.Differ: 5, compare.RustInProgress: 1, compare.OnlyRust: 1, compare.OnlyGo: 1,
			}))
			Expect(compare.RenderStates(differs)).To(Equal(
				"differ            owner/alpha#1  differs in note\n" +
					"    rust: mergeable  renovate/left-pad-2.x  nothing is in its way (note: automerge is off, so this will not merge itself)\n" +
					"    go:   mergeable  renovate/left-pad-2.x  nothing is in its way\n" +
					"differ            owner/beta#1  differs in state, reason\n" +
					"    rust: needs-person  renovate/tar-7.x  nothing says when this update was published\n" +
					"    go:   in-progress  renovate/tar-7.x  a repair is due\n" +
					"differ            owner/beta#2  differs in reason\n" +
					"    rust: waiting  renovate/ansi-5.x  Renovate to rebase it\n" +
					"    go:   waiting  renovate/ansi-5.x  Renovate to rebase it, which drops Almanac's patch\n" +
					"differ            owner/beta#3  differs in branch\n" +
					"    rust: mergeable  renovate/no-ci-1.x  nothing is in its way (note: no checks ran on this pull request)\n" +
					"    go:   mergeable  renovate/no-ci-2.x  nothing is in its way (note: no checks ran on this pull request)\n" +
					"rust-in-progress  owner/beta#4  the Rust version is working on it now; not counted\n" +
					"    rust: in-progress  renovate/glob-10.x  Almanac is validating it\n" +
					"    go:   mergeable  renovate/glob-10.x  nothing is in its way\n" +
					"differ            owner/beta#9  differs in branch, state, reason\n" +
					"    rust: unreadable  its branch or head is outside what Almanac accepts\n" +
					"    go:   waiting  renovate/semver-7.x  CI to finish\n" +
					"only-go           owner/delta#1\n" +
					"    go:   blocked-by-config  renovate/chalk-5.x  set platformAutomerge to false, or require the almanac/trust check\n" +
					"only-rust         owner/gamma#1\n" +
					"    rust: waiting  renovate/minimist-1.x  CI to finish\n" +
					"repositories scanned differ: 3 by Rust, 4 by Go; 8 pull requests: 0 agree, 5 differ, 1 rust-in-progress, 1 only-rust, 1 only-go\n"))
		})

		It("says the same in JSON, with both standings and every difference", func() {
			text, err := json.Marshal(differs)
			Expect(err).NotTo(HaveOccurred())
			var decoded map[string]any
			Expect(json.Unmarshal(text, &decoded)).To(Succeed())
			Expect(decoded["repositories_scanned"]).To(Equal(map[string]any{"rust": 3.0, "go": 4.0}))
			Expect(decoded["totals"]).To(HaveKeyWithValue("rust-in-progress", 1.0))
			pulls := decoded["pulls"].([]any)
			Expect(pulls[1]).To(Equal(map[string]any{
				"pull": "owner/beta#1", "agreement": "differ", "differences": []any{"state", "reason"},
				"rust": map[string]any{"branch": "renovate/tar-7.x", "state": "needs-person", "reason": "nothing says when this update was published"},
				"go":   map[string]any{"branch": "renovate/tar-7.x", "state": "in-progress", "reason": "a repair is due"},
			}))
			Expect(pulls[7]).To(HaveKeyWithValue("go", BeNil()))
		})
	})

	Describe("a file that is not a report", func() {
		read := func(text string) error {
			_, err := compare.ReadReport("r.json", []byte(text))
			return err
		}

		DescribeTable("is refused, saying what is wrong with it",
			func(text, problem string) {
				err := read(text)
				var refused *diag.Error
				Expect(err).To(BeAssignableToTypeOf(refused))
				Expect(err.(*diag.Error).Code).To(Equal(diag.InputNotAReport))
				Expect(err.Error()).To(ContainSubstring(problem))
			},
			Entry("not JSON", "repositories: none", "not the JSON a report prints"),
			Entry("no count of repositories", `{"repositories": []}`, "no repositories_scanned"),
			Entry("a repository that is not a slug", `{"repositories_scanned": 1, "repositories": [{"repo": "a b", "pulls": []}]}`, `"a b"`),
			Entry("a branch that is not a slug", `{"repositories_scanned": 1, "repositories": [{"repo": "o/r", "pulls": [{"number": 1, "branch": "\u001b[31m", "state": "waiting", "reason": "r"}]}]}`, "branch"),
			Entry("a state no report gives", `{"repositories_scanned": 1, "repositories": [{"repo": "o/r", "pulls": [{"number": 1, "branch": "b", "state": "merged", "reason": "r"}]}]}`, `"merged"`),
			Entry("an unreadable entry that names no pull request", `{"repositories_scanned": 1, "unreadable": ["o/r"], "repositories": []}`, `"o/r"`),
			Entry("a pull request listed twice", `{"repositories_scanned": 1, "unreadable": ["o/r#1"], "repositories": [{"repo": "o/r", "pulls": [{"number": 1, "branch": "b", "state": "waiting", "reason": "r"}]}]}`, "twice"),
		)

		It("shows a control character in a reason as a question mark", func() {
			found, err := compare.ReadReport("r.json", []byte(`{"repositories_scanned": 1, "repositories": [{"repo": "o/r", "pulls": [{"number": 1, "branch": "b", "state": "waiting", "reason": "CI\u001b[2J to finish"}]}]}`))
			Expect(err).NotTo(HaveOccurred())
			compared := compare.CompareStates(found, found)
			Expect(compared.Pulls[0].Rust.Reason).To(Equal("CI?[2J to finish"))
		})
	})
})
