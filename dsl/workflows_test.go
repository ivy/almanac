package dsl_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/check"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/internal/fixture/sample"
	"github.com/ivy/almanac/internal/fixture/workflows"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

var _ = Describe("The Rust workflows, written in the DSL", func() {
	DescribeTable("pass the checker",
		func(v *vocab.Vocabulary, w model.Workflow) {
			Expect(check.Check(v, w)).To(Succeed())
		},
		Entry("build_failures", renovate.Vocabulary, workflows.BuildFailures),
		Entry("security_fixes", renovate.Vocabulary, workflows.SecurityFixes),
		Entry("error_triage", sample.Vocabulary, workflows.ErrorTriage),
		Entry("dependency_updates", sample.Vocabulary, workflows.DependencyUpdates),
	)

	It("builds error_triage as exactly this canonical JSON", func() {
		canonical, err := model.Canonical(workflows.ErrorTriage)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(canonical)).To(Equal(strings.Join([]string{
			`{"facts":[{"name":"event","type":"sample.IssueEvent"},{"hostile":true,"name":"findings","type":"sample.Findings"}],`,
			`"flow":[`,
			`{"id":"worth_it","kind":"screen","label":"Is the error worth a look?","screen":{"above":0.7,"question":"Is this error worth an engineer's attention?","text":"event.message"}},`,
			`{"agent":{"agent":"investigator","fact":"findings","inputs":{"message":"event.message"}},"id":"investigate","kind":"agent","label":"Investigate the error"},`,
			`{"decide":[`,
			`{"id":"confident","label":"Sure enough to file","then":[{"File":{"body":{"map":"investigator.summary","showing":"findings"},"target":"event.repo","title":{"format":"Fix this application error"}}}],"when":{"args":["findings"],"is":"high","pred":"investigator.confidence"}},`,
			`{"id":"unsure","label":"Ask whether to fix it","then":[{"Ask":{"body":{"map":"investigator.summary","showing":"findings"},"target":"event.repo","title":{"format":"Is this error worth fixing?"}}}]}`,
			`],"id":"triage","kind":"decide","label":"How sure is the investigator?"}],`,
			`"intent":"File a bug for application errors worth fixing.","key":["event.issue_id"],"limits":{"runs_per_hour":10},"may":["File","Ask"],"name":"error_triage","on":"sentry.issue","subjects":[{"kind":"repo","of":["event.repo"]}]}`,
		}, "")))
	})

	It("lists every way build_failures can end", func() {
		var endings []string
		for _, e := range workflows.BuildFailures.Endings() {
			endings = append(endings, e.Step+"/"+e.Rule)
		}
		Expect(endings).To(Equal([]string{
			"actionable/nothing", "actionable/passed", "actionable/green",
			"attempts/asked", "attempts/exhausted",
			"cooled/undated", "cooled/cooling",
			"decide/empty", "decide/refused", "decide/undid", "decide/push",
		}))
	})

	It("declares each fact where the flow first produces it, with the attempts read on first use", func() {
		var facts []string
		for _, f := range workflows.BuildFailures.Facts {
			kind := "clean"
			if f.Hostile {
				kind = "hostile"
			}
			facts = append(facts, f.Name+": "+kind+" "+f.Type)
		}
		Expect(facts).To(Equal([]string{
			"event: clean renovate.BuildEvent",
			"pull: clean renovate.Pull",
			"attempts: clean vocab.Attempts",
			"age: clean cooled.Age",
			"logs: hostile string",
			"updated: hostile []renovate.Line",
			"patch: hostile patch.Patch",
			"checked: clean vocab.PatchOutcome",
		}))
	})

	It("puts what a person reads in the model: an ask made once, then awaited, and a push that holds", func() {
		var ask *model.AskEffect
		var push *model.PushEffect
		for _, e := range workflows.BuildFailures.Endings() {
			for _, effect := range e.Effects {
				if e.Rule == "undated" && effect.Ask != nil {
					ask = effect.Ask
				}
				if effect.Push != nil {
					push = effect.Push
				}
			}
		}
		Expect(ask).NotTo(BeNil())
		Expect(ask.Once).To(BeTrue())
		Expect(ask.ThenAwait).To(Equal("a person to look at this update"))
		Expect(ask.Pull).To(Equal(model.Ref("pull.number")))
		Expect(ask.Body.Text).To(HavePrefix("No agent reads an update"))
		Expect(push).To(Equal(&model.PushEffect{
			Checked: "checked", Repo: "event.repo", Pull: "pull.number", Branch: "event.branch",
			Message: "fix(deps): repair the build after this update", Hold: renovate.TrustContext,
		}))
	})

	It("makes a manifest with the trigger, budgets, agents and providers (R-43)", func() {
		manifest, err := check.Manifest(renovate.Vocabulary, workflows.BuildFailures)
		Expect(err).NotTo(HaveOccurred())
		digest, err := model.Digest(workflows.BuildFailures)
		Expect(err).NotTo(HaveOccurred())
		Expect(manifest.On).To(Equal("github.check_suite.completed"))
		Expect(manifest.Scans).To(BeTrue())
		Expect(manifest.RunsPerHour).To(Equal(20))
		Expect(manifest.Attempts).To(Equal(3))
		Expect(manifest.May).To(Equal([]effect.Kind{effect.KindAttest, effect.KindAwait, effect.KindAsk, effect.KindPush}))
		Expect(manifest.Agents).To(HaveLen(1))
		Expect(manifest.Agents[0].Name).To(Equal("fixer"))
		Expect(manifest.Agents[0].Grant.Write).To(BeTrue())
		Expect(manifest.Agents[0].PromptVars).To(Equal([]string{"repo"}))
		var requires []string
		for _, r := range manifest.Requires {
			requires = append(requires, r.Name())
		}
		Expect(requires).To(Equal([]string{"renovate.GitHub"}))
		Expect(manifest.Digest).To(Equal(digest))
	})

	It("names the classifier a screen needs in the manifest", func() {
		manifest, err := check.Manifest(sample.Vocabulary, workflows.ErrorTriage)
		Expect(err).NotTo(HaveOccurred())
		var requires []string
		for _, r := range manifest.Requires {
			requires = append(requires, r.Name())
		}
		Expect(requires).To(Equal([]string{"agent.Classifier"}))
		Expect(manifest.On).To(Equal("sentry.issue"))
		Expect(manifest.Attempts).To(BeZero())
	})
})
