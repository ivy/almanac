package dsl_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/tools/go/packages"
)

// typeErrors type-checks body as the body of a function in a package that dot-imports the DSL
// and imports the packages a workflow or its vocabulary uses, and returns the type errors.
func typeErrors(body string) []string {
	return sourceErrors(`package probe

import (
	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	. "github.com/ivy/almanac/dsl"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

var (
	_ = Define
	_ agent.Pass
	_ cooled.Cooled
	_ effect.Kind
	_ hostile.Slug
	_ patch.Patch
	_ = renovate.Vocabulary
	_ vocab.Kind
	_ wire.Payload
	_ workflow.Cx
)

func probe() {
` + body + `
}
`)
}

// sourceErrors type-checks source as a file of a package beside this one, and returns the
// errors, type errors and import errors alike.
func sourceErrors(source string) []string {
	dir, err := filepath.Abs("typecheck_probe")
	Expect(err).NotTo(HaveOccurred())
	file := filepath.Join(dir, "probe.go")
	pkgs, err := packages.Load(&packages.Config{
		Mode:    packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:     filepath.Dir(dir),
		Overlay: map[string][]byte{file: []byte(source)},
	}, "file="+file)
	Expect(err).NotTo(HaveOccurred())
	Expect(pkgs).To(HaveLen(1))
	var errs []string
	for _, e := range pkgs[0].Errors {
		errs = append(errs, e.Msg)
	}
	return errs
}

var _ = Describe("The Go compiler, through the DSL's types", func() {
	BeforeEach(func() {
		_, err := os.Stat("typecheck_probe")
		Expect(os.IsNotExist(err)).To(BeTrue(), "the probe is only ever an overlay")
	})

	It("accepts a well-typed workflow part", func() {
		Expect(typeErrors(`
			pull := Fact[renovate.Pull]("pull")
			repo := Field[hostile.Slug](pull, "repo")
			_ = Rule("nothing", "Nothing to do", pull.Is(renovate.NotActionable),
				Ask(repo, Title("Build %d failed"), Field[uint64](pull, "number")))
		`)).To(BeEmpty())
	})

	DescribeTable("refuses",
		func(body, because string) {
			errs := typeErrors(body)
			Expect(errs).NotTo(BeEmpty())
			Expect(strings.Join(errs, "\n")).To(ContainSubstring(because))
		},
		Entry("a hostile fact in a clean predicate",
			`_ = Hostile[renovate.Pull]("pull").Is(renovate.NotActionable)`,
			"cannot use renovate.NotActionable"),
		Entry("a fact of another type in a predicate",
			`_ = Fact[renovate.SecurityView]("pull").Is(renovate.NotActionable)`,
			"cannot use renovate.NotActionable"),
		Entry("a hostile fact in a title",
			`_ = Ask(Fact[hostile.Slug]("repo"), Title("%s"), Hostile[string]("logs"))`,
			"does not implement dsl.Clean"),
		Entry("a hostile fact in a key",
			`_ = Key(Hostile[hostile.Slug]("repo"))`,
			"does not implement dsl.Clean"),
		Entry("a title that is not a constant",
			`format := "Build failed"; _ = Title(format)`,
			"cannot use format"),
		Entry("a push of a patch no policy checked",
			`_ = Push(Hostile[patch.Patch]("patch"), Fact[hostile.Slug]("repo"), Fact[uint64]("n"), Fact[hostile.Slug]("branch"), "fix")`,
			"cannot use Hostile[patch.Patch]"),
		Entry("a field inside a hostile fact",
			`_ = Field[uint64](Hostile[renovate.Pull]("pull"), "number")`,
			"does not match"),
		Entry("an observer's reading put in a fact of another type",
			`_ = Observe("logs", "Read the logs", Fact[string]("logs"), renovate.FailureLogs, Fact[renovate.Pull]("pull"))`,
			"does not match"),
	)
})
