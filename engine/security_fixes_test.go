package engine_test

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/engine"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/internal/fixture/workflows"
	"github.com/ivy/almanac/workflow"
)

// securityOpened is a Renovate security pull request opened.
var securityOpened = json.RawMessage(`{"repo": "` + repo + `", "branch": "renovate/npm-tar-vulnerability", "sha": "` + sha + `", "number": 7, "labeled": false}`)

// securityUpdate is a recorded run of security_fixes that reaches the reviewer: an open pull
// request, an advisory naming it, a cooled release and a readable diff. Edit changes what was
// recorded before it is recorded.
func securityUpdate(edit func(*renovate.SecurityView, *renovate.Advisory, *renovate.PackageDiff)) *recorded {
	body := "Fixes CVE-2026-0001. " + injection
	view := renovate.SecurityView{Found: true, Head: hostile.MustParseSha(sha), Text: hostile.New(renovate.PullText{Body: &body})}
	advisory := renovate.Advisory{NamesTheFix: true}
	diff := renovate.PackageDiff{Readable: true, Unshown: hostile.New(""), Diff: hostile.New("+ the fix " + injection)}
	if edit != nil {
		edit(&view, &advisory, &diff)
	}
	r := newRecorded().record("pull", view).record("advisory", advisory).record("diff", diff)
	r.published = cooled.PublishedAt(now.Add(-30 * 24 * time.Hour))
	r.answers["reviewer"] = `{"verdict": "no_concerns", "notes": "` + injection + `"}`
	return r
}

// reviewed is securityUpdate with the reviewer answering verdict, on a head that was tested,
// was not, or never will be.
func reviewed(verdict string, edit func(*renovate.SecurityView)) *recorded {
	r := securityUpdate(func(v *renovate.SecurityView, _ *renovate.Advisory, _ *renovate.PackageDiff) {
		if edit != nil {
			edit(v)
		}
	})
	r.answers["reviewer"] = `{"verdict": "` + verdict + `", "notes": "pinned an old tar. ` + injection + `"}`
	return r
}

// checked is securityUpdate with a deterministic check failing.
func checked(edit func(*renovate.Advisory, *renovate.PackageDiff)) *recorded {
	return securityUpdate(func(_ *renovate.SecurityView, a *renovate.Advisory, d *renovate.PackageDiff) { edit(a, d) })
}

// refusal is what security_fixes does when a check fails: fail trust and ask, once.
func refusal(question, body string) string {
	return `[` + failTrust + `, ` + ask(question, body) + `]`
}

var securityFixesEndings = []ending{
	{"not an open Renovate pull request",
		func() *recorded {
			return securityUpdate(func(v *renovate.SecurityView, _ *renovate.Advisory, _ *renovate.PackageDiff) { v.Found = false })
		}, "found", "gone", `[]`},
	{"not the fix an advisory names",
		func() *recorded {
			return checked(func(a *renovate.Advisory, _ *renovate.PackageDiff) { a.NamesTheFix = false })
		},
		"checks", "not_the_fix", refusal("This update is not the fix an advisory names", "")},
	{"a package that could not be read",
		func() *recorded {
			return checked(func(_ *renovate.Advisory, d *renovate.PackageDiff) { d.Readable = false })
		},
		"checks", "unreadable", refusal("This security update's package could not be read", "")},
	{"files no reviewer can read",
		func() *recorded {
			return checked(func(_ *renovate.Advisory, d *renovate.PackageDiff) { d.Unshown = hostile.New("dist/index.min.js") })
		},
		"checks", "hidden", refusal("This security update changes files no reviewer can read", "dist/index.min.js")},
	{"a diff too large to review in full",
		func() *recorded {
			return checked(func(_ *renovate.Advisory, d *renovate.PackageDiff) { d.Truncated = true })
		},
		"checks", "truncated", refusal("This security update is too large for its reviewer to read in full", "")},
	{"an install script added",
		func() *recorded {
			return checked(func(_ *renovate.Advisory, d *renovate.PackageDiff) { d.InstallScriptAdded = true })
		},
		"checks", "install_script", refusal("This security update adds an install script", "")},
	{"a fix younger than the cooldown",
		func() *recorded {
			r := securityUpdate(nil)
			r.published = cooled.PublishedAt(now.Add(-time.Hour))
			return r
		},
		"checks", "too_young", `[` + ask("This fix is younger than the cooldown", "") + `]`},
	{"a reviewer with concerns",
		func() *recorded { return reviewed("concerns", nil) },
		"verdict", "concerns", refusal("A reviewer raised concerns about this security update", "pinned an old tar. "+injection)},
	{"no concerns, and tested",
		func() *recorded { return reviewed("no_concerns", func(v *renovate.SecurityView) { v.Tested = true }) },
		"verdict", "tested", `[` + passTrust + `]`},
	{"no concerns, and nothing will test it",
		func() *recorded {
			return reviewed("no_concerns", func(v *renovate.SecurityView) { v.Untestable = true })
		},
		"verdict", "untestable", `[` + ask("Nothing tests this repository, so this security update cannot merge", "") + `]`},
	{"no concerns, and waiting for tests",
		func() *recorded { return reviewed("no_concerns", nil) },
		"verdict", "waiting", `[]`},
}

var _ = Describe("security_fixes, walked over recorded facts", func() {
	var sf *engine.Workflow

	BeforeEach(func() {
		var err error
		sf, err = engine.New(renovate.Vocabulary, workflows.SecurityFixes)
		Expect(err).NotTo(HaveOccurred())
	})

	It("has a recorded run for every ending of the model, and only those", func() {
		Expect(stepsAndRules(securityFixesEndings)).To(ConsistOf(endingsOf(workflows.SecurityFixes)))
	})

	DescribeTable("reaches each ending from the facts recorded for it",
		func(e ending) {
			r := e.run()
			ended, err := sf.Walk(context.Background(), r, securityOpened)
			Expect(err).NotTo(HaveOccurred())
			Expect([]string{ended.Step, ended.Rule}).To(Equal([]string{e.step, e.rule}))
			Expect(planned(ended.Decided.Effects)).To(MatchJSON(e.effects))
			Expect(r.reads).To(BeEmpty())
			Expect(r.providers).To(BeEmpty())

			By("holding the update before anything else, and asking the reviewer only once every check passed")
			Expect(r.holds).To(Equal([]string{held}))
			if e.step == "verdict" {
				Expect(r.calls).To(HaveLen(1))
			} else {
				Expect(r.calls).To(BeEmpty())
			}
		},
		entries(securityFixesEndings),
	)

	It("asks about a failed check only once", func() {
		r := checked(func(a *renovate.Advisory, _ *renovate.PackageDiff) { a.NamesTheFix = false })
		r.attempts = workflow.Attempts{Asked: true}
		ended, err := sf.Walk(context.Background(), r, securityOpened)
		Expect(err).NotTo(HaveOccurred())
		Expect(ended.Rule).To(Equal("not_the_fix"))
		Expect(planned(ended.Decided.Effects)).To(MatchJSON(`[` + failTrust + `]`))
	})

	It("gives the reviewer the diff, still hostile, and no commit to check out", func() {
		r := reviewed("no_concerns", nil)
		_, err := sf.Walk(context.Background(), r, securityOpened)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.calls[0].Grant().Write).To(BeFalse())
		Expect(r.inputs[0]).To(MatchJSON(`{"diff": {"$untrusted": "+ the fix ` + injection + `"}}`))
	})
})
