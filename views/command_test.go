package views_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/buildfailures"
	"github.com/ivy/almanac/almanacs/renovate/securityfixes"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/views"
)

// run runs the verbs with args as an almanac's binary would, and returns what they printed.
func run(args ...string) (string, error) {
	root := &cobra.Command{Use: "almanac", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(views.Commands(renovate.Vocabulary, buildfailures.Model, securityfixes.Model)...)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

var _ = Describe("The verbs", func() {
	It("prints a workflow's outcomes as JSON", func() {
		out, err := run("outcomes", "security_fixes", "--format", "json")
		Expect(err).NotTo(HaveOccurred())
		var s views.Summary
		Expect(json.Unmarshal([]byte(out), &s)).To(Succeed())
		want, err := views.Outcomes(renovate.Vocabulary, securityfixes.Model)
		Expect(err).NotTo(HaveOccurred())
		Expect(s).To(Equal(want))
	})

	It("prints a workflow's outcomes as text", func() {
		out, err := run("outcomes", "build_failures")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("11 ways to end"))
		Expect(out).To(ContainSubstring("decide/push  Push the repair"))
	})

	It("compares with an old version's outcomes, read from a file", func() {
		old, err := run("outcomes", "build_failures", "--format", "json")
		Expect(err).NotTo(HaveOccurred())
		path := filepath.Join(GinkgoT().TempDir(), "old.json")
		Expect(os.WriteFile(path, []byte(old), 0o600)).To(Succeed())

		out, err := run("outcomes", "build_failures", "--diff", path)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("no changes\n"))

		page, err := run("views", "build_failures", "--diff", path)
		Expect(err).NotTo(HaveOccurred())
		Expect(page).To(ContainSubstring(`id="changes"`))
		Expect(page).To(ContainSubstring("The outcomes, steps and settings are the same in both versions."))
	})

	It("writes a page", func() {
		page, err := run("views", "security_fixes")
		Expect(err).NotTo(HaveOccurred())
		Expect(page).To(HavePrefix("<!doctype html>"))
		Expect(page).To(ContainSubstring("<h1>security_fixes</h1>"))
	})

	It("asks which workflow, when the almanac has several and none is named", func() {
		_, err := run("views")
		var d *diag.Error
		Expect(err).To(BeAssignableToTypeOf(d))
		Expect(err.(*diag.Error).Code).To(Equal(diag.Usage))
		Expect(err.(*diag.Error).Help).To(Equal("name one of: build_failures, security_fixes"))
	})

	It("refuses a workflow it does not have", func() {
		_, err := run("outcomes", "no_such")
		Expect(err).To(MatchError(ContainSubstring(`no workflow named "no_such"`)))
	})

	It("refuses an old version that is not a workflow's outcomes", func() {
		path := filepath.Join(GinkgoT().TempDir(), "old.json")
		Expect(os.WriteFile(path, []byte(`{"not":"outcomes"}`), 0o600)).To(Succeed())
		_, err := run("views", "build_failures", "--diff", path)
		Expect(err).To(BeAssignableToTypeOf(&diag.Error{}))
		Expect(err.(*diag.Error).Code).To(Equal(diag.InputInvalidJSON))
	})
})
