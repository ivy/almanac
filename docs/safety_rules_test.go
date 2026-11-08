package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// row is one case in a table of safety-rules.md.
type row struct {
	name, becomes, fires, proof, note string
}

// rows are the cases the table under heading lists, by the columns it has.
func rows(page, heading string) []row {
	_, section, found := strings.Cut(page, "\n## "+heading+"\n")
	Expect(found).To(BeTrue(), "safety-rules.md has no section %q", heading)
	section, _, _ = strings.Cut(section, "\n## ")
	var out []row
	for line := range strings.SplitSeq(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		r := row{name: strings.Trim(cells[0], "`"), becomes: cells[1]}
		switch len(cells) {
		case 5:
			r.fires, r.proof, r.note = cells[2], cells[3], cells[4]
		case 4:
			r.proof, r.note = cells[2], cells[3]
		default:
			Fail("a row of " + heading + " has " + strings.Join(cells, "|"))
		}
		out = append(out, r)
	}
	return out
}

// cases are the Rust cases in crates/fails/tests/ui/dir, by name.
func cases(dir string) []string {
	files, err := filepath.Glob(filepath.Join("..", "crates", "fails", "tests", "ui", dir, "*.rs"))
	Expect(err).NotTo(HaveOccurred())
	var names []string
	for _, file := range files {
		names = append(names, strings.TrimSuffix(filepath.Base(file), ".rs"))
	}
	Expect(names).NotTo(BeEmpty())
	return names
}

func names(rs []row) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.name)
	}
	return out
}

var link = regexp.MustCompile(`\]\(([^)#]+)\)`)

// expectProof checks that every spec a row links to exists and names the case.
func expectProof(r row) {
	GinkgoHelper()
	links := link.FindAllStringSubmatch(r.proof, -1)
	if r.becomes != "not applicable" {
		Expect(links).NotTo(BeEmpty(), "%s names no spec that proves it", r.name)
	}
	for _, l := range links {
		source, err := os.ReadFile(l[1])
		Expect(err).NotTo(HaveOccurred(), "%s links to %s", r.name, l[1])
		Expect(string(source)).To(ContainSubstring(r.name), "%s links to %s, which does not mention it", r.name, l[1])
	}
}

var _ = Describe("docs/safety-rules.md", func() {
	source, err := os.ReadFile("safety-rules.md")
	if err != nil {
		panic(err)
	}
	page := string(source)

	It("has one row for each Rust compile-fail case", func() {
		listed := names(rows(page, "Compile-fail cases"))
		Expect(listed).To(ConsistOf(cases("fail")))
		Expect(slices.Compact(slices.Sorted(slices.Values(listed)))).To(HaveLen(len(listed)), "a case has two rows")
	})

	It("has one row for each Rust pass case", func() {
		Expect(names(rows(page, "Pass cases"))).To(ConsistOf(cases("pass")))
	})

	It("says when each case fires, and why one that fires at run time, or is weakened, is not refused earlier", func() {
		for _, r := range rows(page, "Compile-fail cases") {
			Expect(r.becomes).To(BeElementOf("type error", "hostilevet", "checker", "start-up", "run time", "weakened", "not applicable"), r.name)
			Expect(r.fires).To(BeElementOf("build", "start-up", "run time", "never"), r.name)
			if r.fires == "run time" {
				Expect(r.becomes).To(BeElementOf("run time", "weakened"), "%s fires at run time and is not marked so", r.name)
			}
			if r.fires == "never" {
				Expect(r.becomes).To(BeElementOf("weakened", "not applicable"), "%s never fires and is not marked so", r.name)
			}
			if slices.Contains([]string{"run time", "weakened", "not applicable"}, r.becomes) {
				Expect(r.note).NotTo(BeEmpty(), "%s does not say why", r.name)
			}
		}
	})

	It("names, for each case, specs that exist and mention it", func() {
		for _, r := range slices.Concat(rows(page, "Compile-fail cases"), rows(page, "Pass cases")) {
			expectProof(r)
		}
	})
})
