package report_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate/report"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/simforge"
)

// execute runs the almanac-renovate command line with only the report verb, in an environment
// holding exactly vars, and returns its exit status, stdout and stderr.
func execute(vars map[string]string, args ...string) (int, string, string) {
	GinkgoT().Setenv("ALMANAC_CONFIG", filepath.Join(GinkgoT().TempDir(), "missing.toml"))
	env := func(name string) (string, bool) {
		if name == "ALMANAC_REGISTRY_URL" {
			// Nothing in these specs dates an update, and nothing may reach a real registry.
			return "http://127.0.0.1:1", true
		}
		value, ok := vars[name]
		return value, ok
	}
	almanac := app.Almanac{Name: "almanac-renovate", Verbs: []app.Verb{report.Verb(env)}}
	var stdout, stderr bytes.Buffer
	status := almanac.Execute(context.Background(), args, &stdout, &stderr)
	return status, stdout.String(), stderr.String()
}

var _ = Describe("The report verb", func() {
	It("reads through the command line, and refuses without a token", func() {
		sim := fleet()
		status, stdout, stderr := execute(map[string]string{
			"GITHUB_TOKEN":            "t",
			"ALMANAC_OWNER":           owner,
			"ALMANAC_RENOVATE_AUTHOR": simforge.Renovate,
			"GITHUB_API_URL":          sim.URL(),
			"ALMANAC_READ_ONLY":       "0",
		}, "--format", "json", "report")
		Expect(status).To(Equal(0), stderr)
		var found map[string]any
		Expect(json.Unmarshal([]byte(stdout), &found)).To(Succeed())
		Expect(found["repositories_scanned"]).To(BeEquivalentTo(3))
		Expect(stdout).To(Equal(asRustPrints(snapshot(rustJSON)) + "\n"))
		Expect(sim.Writes()).To(BeEmpty(), "a report reads, whatever ALMANAC_READ_ONLY says")

		status, _, stderr = execute(map[string]string{"ALMANAC_OWNER": owner}, "--format", "json", "report")
		Expect(status).NotTo(Equal(0))
		var refused map[string]any
		Expect(json.Unmarshal([]byte(stderr), &refused)).To(Succeed())
		Expect(refused["code"]).To(Equal("almanac::config::env_missing"))
		Expect(refused["message"]).To(ContainSubstring("GITHUB_TOKEN"))
	})

	It("prints the report for a person as the Rust report does", func() {
		sim := fleet()
		status, stdout, stderr := execute(map[string]string{
			"GITHUB_TOKEN":   "t",
			"ALMANAC_OWNER":  owner,
			"GITHUB_API_URL": sim.URL(),
		}, "report")
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal(snapshot(rustText) + "\n"))
	})

	It("names the account it needs to report on", func() {
		status, _, stderr := execute(map[string]string{"GITHUB_TOKEN": "t"}, "report")
		Expect(status).NotTo(Equal(0))
		Expect(stderr).To(ContainSubstring("ALMANAC_OWNER"))
	})
})
