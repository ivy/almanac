package report_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/report"
	"github.com/ivy/almanac/cooled"
)

// healthy is a pull request with nothing in its way.
func healthy() report.Facts {
	return report.Facts{
		Conflict: report.NoConflict,
		Checks:   renovate.ChecksGreen,
		Trust:    renovate.TrustAbsent,
		Cooldown: cooled.Passed,
	}
}

func stateOf(f report.Facts) report.State { return report.Derive(f).State }

// everyScenario is every combination of facts the policy distinguishes, with attempts up to
// past the cap.
func everyScenario() []report.Facts {
	conflicts := []report.Conflict{report.NoConflict, report.Conflicted, report.ConflictUnknown}
	checks := []renovate.Checks{renovate.ChecksNone, renovate.ChecksPending, renovate.ChecksGreen, renovate.ChecksRed}
	trusts := []renovate.Trust{renovate.TrustAbsent, renovate.TrustPending, renovate.TrustSuccess, renovate.TrustFailure}
	cooldowns := []cooled.Age{cooled.Passed, cooled.Young, cooled.Unknown}
	flags := []report.Flag{report.Unset, report.Off, report.On}
	var configs []report.Config
	for _, automerge := range flags {
		for _, platform := range flags {
			for _, ignored := range flags {
				for _, required := range []bool{false, true} {
					configs = append(configs, report.Config{
						Automerge: automerge, PlatformAutomerge: platform, GitIgnoredAuthors: ignored, TrustRequired: required,
					})
				}
			}
		}
	}
	var all []report.Facts
	for _, conflict := range conflicts {
		for _, check := range checks {
			for _, trust := range trusts {
				for _, security := range []bool{false, true} {
					for attempts := range renovate.AttemptCap + 3 {
						for _, cooldown := range cooldowns {
							for _, config := range configs {
								all = append(all, report.Facts{
									Conflict: conflict, Checks: check, Trust: trust, Security: security,
									Attempts: attempts, Cooldown: cooldown, Config: config,
								})
							}
						}
					}
				}
			}
		}
	}
	return all
}

var _ = Describe("Where a pull request stands", func() {
	It("a green pull request with no conflict is mergeable", func() {
		Expect(stateOf(healthy())).To(Equal(report.Mergeable))
	})

	It("a repository that cannot automerge is still mergeable, and says so", func() {
		f := healthy()
		f.Config.Automerge = report.Off
		v := report.Derive(f)
		Expect(v.State).To(Equal(report.Mergeable))
		Expect(v.Note).To(ContainSubstring("will not merge itself"))
	})

	It("a repository with no CI is reported as having no checks", func() {
		f := healthy()
		f.Checks = renovate.ChecksNone
		v := report.Derive(f)
		Expect(v.State).To(Equal(report.Mergeable))
		Expect(v.Note).To(ContainSubstring("no checks"))
	})

	It("a conflict waits for Renovate to rebase it, even over a patch", func() {
		conflict := healthy()
		conflict.Conflict = report.Conflicted
		Expect(stateOf(conflict)).To(Equal(report.Waiting))
		patched := conflict
		patched.Attempts = 1
		v := report.Derive(patched)
		Expect(v.State).To(Equal(report.Waiting))
		Expect(v.Reason).To(ContainSubstring("drops Almanac's patch"))
	})

	// The report half of build_failures' a_conflict_on_a_patched_branch_waits_for_renovate: the
	// facts that test's run leaves behind, with the pull request then in conflict.
	It("a conflict on a patched branch waits for Renovate", func() {
		Expect(stateOf(report.Facts{
			Conflict: report.Conflicted,
			Checks:   renovate.ChecksRed,
			Trust:    renovate.TrustAbsent,
			Attempts: 1,
			Cooldown: cooled.Passed,
		})).To(Equal(report.Waiting))
	})

	It("a patched conflict Renovate would not rebase is blocked by config", func() {
		f := healthy()
		f.Conflict = report.Conflicted
		f.Attempts = 1
		f.Config.GitIgnoredAuthors = report.Off
		Expect(stateOf(f)).To(Equal(report.BlockedByConfig))
	})

	It("waits for GitHub to work out whether a pull request can merge", func() {
		f := healthy()
		f.Conflict = report.ConflictUnknown
		v := report.Derive(f)
		Expect(v.State).To(Equal(report.Waiting))
		Expect(v.Reason).To(Equal("GitHub to compute mergeability"))
	})

	It("red CI is repaired only after the cooldown", func() {
		red := func(age cooled.Age) report.Facts {
			f := healthy()
			f.Checks = renovate.ChecksRed
			f.Cooldown = age
			return f
		}
		Expect(stateOf(red(cooled.Passed))).To(Equal(report.InProgress))
		Expect(stateOf(red(cooled.Young))).To(Equal(report.Waiting))
		Expect(stateOf(red(cooled.Unknown))).To(Equal(report.NeedsPerson))
	})

	It("repeated failed repairs ask a person", func() {
		f := healthy()
		f.Checks = renovate.ChecksRed
		f.Attempts = renovate.AttemptCap
		Expect(stateOf(f)).To(Equal(report.NeedsPerson))
	})

	It("a status that cannot hold a security update names the setting", func() {
		f := healthy()
		f.Security = true
		f.Trust = renovate.TrustPending
		v := report.Derive(f)
		Expect(v.State).To(Equal(report.BlockedByConfig))
		Expect(v.Reason).To(ContainSubstring("platformAutomerge"))
	})

	It("a required trust check holds a security update", func() {
		f := healthy()
		f.Security = true
		f.Trust = renovate.TrustPending
		f.Config.TrustRequired = true
		Expect(stateOf(f)).To(Equal(report.InProgress))
	})

	It("a patched branch Renovate will not merge names the setting", func() {
		f := healthy()
		f.Attempts = 1
		f.Config.GitIgnoredAuthors = report.Off
		v := report.Derive(f)
		Expect(v.State).To(Equal(report.BlockedByConfig))
		Expect(v.Reason).To(ContainSubstring("gitIgnoredAuthors"))
	})

	It("a failed trust check, or a hold nothing tests, asks a person", func() {
		failed := healthy()
		failed.Trust = renovate.TrustFailure
		Expect(report.Derive(failed).Reason).To(Equal("it failed trust checks"))
		untested := healthy()
		untested.Checks = renovate.ChecksNone
		untested.Trust = renovate.TrustPending
		Expect(report.Derive(untested)).To(Equal(report.Verdict{State: report.NeedsPerson, Reason: "nothing tests this repository"}))
	})

	Describe("over every scenario", func() {
		all := everyScenario()

		It("covers the whole space of facts", func() {
			Expect(all).To(HaveLen(3 * 4 * 4 * 2 * (renovate.AttemptCap + 3) * 3 * 54))
		})

		It("every scenario gets one state with a reason", func() {
			for _, f := range all {
				Expect(report.Derive(f).Reason).NotTo(BeEmpty(), "%+v", f)
			}
		})

		It("mergeable holds only when the definition does", func() {
			for _, f := range all {
				if stateOf(f) != report.Mergeable {
					continue
				}
				Expect(f.Conflict).To(Equal(report.NoConflict), "%+v", f)
				Expect(f.Checks).To(BeElementOf(renovate.ChecksGreen, renovate.ChecksNone), "%+v", f)
				Expect(f.Trust).To(BeElementOf(renovate.TrustAbsent, renovate.TrustSuccess), "%+v", f)
			}
		})

		It("blocked by config always names a setting", func() {
			for _, f := range all {
				v := report.Derive(f)
				if v.State != report.BlockedByConfig {
					continue
				}
				named := strings.Contains(v.Reason, "platformAutomerge") || strings.Contains(v.Reason, "gitIgnoredAuthors")
				Expect(named).To(BeTrue(), v.Reason)
			}
		})

		It("a young or undated update is never being repaired", func() {
			for _, f := range all {
				if f.Cooldown != cooled.Passed {
					Expect(report.Derive(f).Reason).NotTo(Equal("a repair is due"), "%+v", f)
				}
			}
		})
	})
})
