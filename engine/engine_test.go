package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/check"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/engine"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/internal/fixture/sample"
	"github.com/ivy/almanac/internal/fixture/workflows"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

func buildFailures() *engine.Workflow {
	GinkgoHelper()
	bf, err := engine.New(renovate.Vocabulary, workflows.BuildFailures)
	Expect(err).NotTo(HaveOccurred())
	return bf
}

func probing(m model.Workflow) *engine.Workflow {
	GinkgoHelper()
	e, err := engine.New(probes, m)
	Expect(err).NotTo(HaveOccurred())
	return e
}

func names(requirements []providers.Requirement) []string {
	var all []string
	for _, r := range requirements {
		all = append(all, r.Name())
	}
	return all
}

var _ = Describe("A model run as a workflow", func() {
	It("declares what the model declares: its trigger, limits, effects, agents and providers", func() {
		bf := buildFailures()
		Expect(bf.Name()).To(Equal("build_failures"))
		Expect(bf.Intent()).To(Equal(workflows.BuildFailures.Intent))
		Expect(bf.Kind()).To(Equal("github.check_suite.completed"))
		Expect(bf.Handles("github.status")).To(BeTrue())
		Expect(bf.Handles("github.pull_request.opened")).To(BeFalse())
		Expect(bf.RunsPerHour()).To(Equal(20))
		Expect(bf.May()).To(Equal([]effect.Kind{effect.KindAttest, effect.KindAwait, effect.KindAsk, effect.KindPush}))
		Expect(bf.Agents()).To(HaveLen(1))
		Expect(bf.Agents()[0].Name).To(Equal("fixer"))
		Expect(names(bf.Requirements())).To(Equal([]string{"renovate.GitHub"}))

		manifest, err := check.Manifest(renovate.Vocabulary, workflows.BuildFailures)
		Expect(err).NotTo(HaveOccurred())
		Expect(bf.Manifest()).To(Equal(manifest))
		Expect(bf.Manifest().Digest).To(HavePrefix("sha256:"))
	})

	It("records the digest of the model a run walked, before anything else", func() {
		r := failedBuild(fixed)
		_, err := buildFailures().Walk(context.Background(), r, buildFailed)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.records["model"]).To(HaveLen(1))
		Expect(r.records["model"][0]).To(MatchJSON(`"` + buildFailures().Manifest().Digest + `"`))
	})

	It("identifies a payload by key and subjects, from the event alone", func() {
		identity, err := buildFailures().Identify(buildFailed)
		Expect(err).NotTo(HaveOccurred())
		Expect(identity).To(Equal(workflow.Identity{
			Key:      repo + ":" + branch,
			Subjects: []workflow.Subject{workflow.RepoSubject(hostile.MustParseSlug(repo))},
			Wanted:   true,
		}))
	})

	It("keys on a number as its digits", func() {
		et, err := engine.New(sample.Vocabulary, workflows.ErrorTriage)
		Expect(err).NotTo(HaveOccurred())
		identity, err := et.Identify(errorReported)
		Expect(err).NotTo(HaveOccurred())
		Expect(identity.Key).To(Equal("42"))
	})

	It("does not want an event its filter refuses", func() {
		identity, err := buildFailures().Identify(json.RawMessage(`{"repo": "` + repo + `", "branch": "` + branch + `", "sha": "` + sha + `", "failed": true, "finished": true, "patched": false, "bookkeeping": true}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(identity.Wanted).To(BeFalse())
		Expect(identity.Key).To(Equal(repo + ":" + branch))
	})

	It("names the field of a payload that does not parse", func() {
		_, err := buildFailures().Identify(json.RawMessage(`{"branch": "` + branch + `"}`))
		var malformed *wire.Malformed
		Expect(errors.As(err, &malformed)).To(BeTrue())
		_, err = buildFailures().Execute(context.Background(), newRecorded(), json.RawMessage(`{}`))
		Expect(errors.As(err, &malformed)).To(BeTrue())
	})

	It("scans with the model's scan, finding events of its own kind", func() {
		found, err := probing(linked).Scan(context.Background(), workflow.NewScanner(providers.New()))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(1))
		Expect(found[0].Kind).To(Equal("probe.happened"))
		Expect(probing(linked).Manifest().Scans).To(BeTrue())

		found, err = probing(sneaky("reading")).Scan(context.Background(), workflow.NewScanner(providers.New()))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeEmpty())
	})

	It("links what a run found once it has found it, and nothing it never read", func() {
		e := probing(linked)
		r := newRecorded().record("package", map[string]any{"seen": true, "package": "tar"})
		ended, err := e.Walk(context.Background(), r, json.RawMessage(`{"repo": "`+repo+`", "stop": false}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(ended.Rule).To(Equal("end"))
		Expect(ended.Decided.Links).To(Equal([]workflow.Subject{workflow.PackageSubject(hostile.MustParseSlug(repo), hostile.MustParseSlug("tar"))}))

		r = newRecorded()
		ended, err = e.Walk(context.Background(), r, json.RawMessage(`{"repo": "`+repo+`", "stop": true}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(ended.Rule).To(Equal("stop"))
		Expect(ended.Decided.Links).To(BeEmpty())
		Expect(r.reads).To(BeEmpty())
	})

	It("keeps a copy of the model, so changing the model it was made from changes nothing it runs", func() {
		m := workflows.BuildFailures.Clone()
		bf, err := engine.New(renovate.Vocabulary, m)
		Expect(err).NotTo(HaveOccurred())
		m.Flow[0].Observe.From = "renovate.failure_logs"
		m.May = nil
		m.Flow[len(m.Flow)-1].Decide = nil

		ended, err := bf.Walk(context.Background(), failedBuild(fixed), buildFailed)
		Expect(err).NotTo(HaveOccurred())
		Expect(ended.Rule).To(Equal("push"))
		Expect(bf.May()).To(ContainElement(effect.KindPush))

		copied := bf.Model()
		copied.Name = "renamed"
		Expect(bf.Name()).To(Equal("build_failures"))
	})
})

var _ = Describe("What a model does not declare", func() {
	DescribeTable("is refused before any walk, so no run can reach it",
		func(edit func(*model.Workflow), code diag.Code) {
			m := workflows.BuildFailures.Clone()
			edit(&m)
			bf, err := engine.New(renovate.Vocabulary, m)
			Expect(bf).To(BeNil())
			var problems diag.Problems
			Expect(errors.As(err, &problems)).To(BeTrue())
			Expect(problems.Codes()).To(ContainElement(code))
		},
		Entry("an observer the vocabulary does not hold", func(m *model.Workflow) {
			m.Flow[0].Observe.From = "renovate.read_secrets"
		}, diag.ModelUnknownName),
		Entry("a part that is not an observer, in an observe step", func(m *model.Workflow) {
			m.Flow[0].Observe.From = renovate.Failing.Name()
		}, diag.ModelUnknownName),
		Entry("an agent the vocabulary does not hold", func(m *model.Workflow) {
			m.Flow[stepIndex(*m, model.Agent)].Agent.Agent = "deployer"
		}, diag.ModelUnknownName),
		Entry("a publish-time source the vocabulary does not hold", func(m *model.Workflow) {
			m.Flow[stepIndex(*m, model.Measure)].Measure.From = "renovate.someone_says_so"
		}, diag.ModelUnknownName),
		Entry("an effect the workflow may not produce", func(m *model.Workflow) {
			m.May = slices.DeleteFunc(m.May, func(k effect.Kind) bool { return k == effect.KindPush })
		}, diag.ModelEffectNotAllowed),
		Entry("a hold by a workflow that may not attest", func(m *model.Workflow) {
			m.May = slices.DeleteFunc(m.May, func(k effect.Kind) bool { return k == effect.KindAttest })
		}, diag.ModelEffectNotAllowed),
		Entry("a predicate the vocabulary does not hold", func(m *model.Workflow) {
			m.Flow[1].Decide[0].When.Pred = "renovate.trust_me"
		}, diag.ModelUnknownName),
		Entry("a map the vocabulary does not hold", func(m *model.Workflow) {
			last := m.Flow[len(m.Flow)-1].Decide
			last[1].Then[1].Ask.Body.Map = "renovate.everything"
		}, diag.ModelUnknownName),
	)

	It("reaches only the agents its steps name, which the runtime refuses to go beyond", func() {
		Expect(buildFailures().Agents()).To(ConsistOf(HaveField("Name", "fixer")))
		Expect(probing(linked).Agents()).To(BeEmpty())
	})

	It("lets an observer reach only the providers its registration says it needs", func() {
		forge := &countingForge{}
		r := newRecorded()
		r.live = true
		r.provided = map[string]any{"effect.Forge": forge}
		_, err := probing(sneaky("reading")).Walk(context.Background(), r, json.RawMessage(`{"repo": "`+repo+`", "stop": false}`))
		Expect(err).To(MatchError(ContainSubstring("probe.reads_forge reached for effect.Forge, which its registration does not say it needs")))
		Expect(r.providers).To(BeEmpty(), "the run's context was never asked")
		Expect(forge.calls).To(BeZero())
	})

	It("lets an observer reach a provider it declares", func() {
		r := newRecorded()
		r.live = true
		r.provided = map[string]any{"engine_test.Packages": packages{}}
		ended, err := probing(linked).Walk(context.Background(), r, json.RawMessage(`{"repo": "`+repo+`", "stop": false}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.providers).To(Equal([]string{"engine_test.Packages"}))
		Expect(ended.Decided.Links).To(HaveLen(1))
	})

	DescribeTable("refuses an observed fact named for what a run's context records itself",
		func(m model.Workflow) {
			_, err := engine.New(probes, m)
			var problems diag.Problems
			Expect(errors.As(err, &problems)).To(BeTrue())
			Expect(problems.Codes()).To(ContainElement(diag.ModelDeclaration))
			Expect(err).To(MatchError(ContainSubstring("recorded under its name")))
		},
		Entry("a hold", sneaky("hold")),
		Entry("a screen", sneaky("screen")),
		Entry("a cooldown", sneaky("cooldown")),
		Entry("the guard's verdict", sneaky("guard")),
		Entry("the model's digest", sneaky("model")),
		Entry("an agent's answer", sharesAnAgentsName),
	)
})

// stepIndex is the index of the first step of kind in m.
func stepIndex(m model.Workflow, kind model.StepKind) int {
	return slices.IndexFunc(m.Flow, func(s model.Step) bool { return s.Kind == kind })
}

// countingForge counts calls, and never applies one.
type countingForge struct{ calls int }

func (f *countingForge) Attest(context.Context, string, effect.Attest) error { f.calls++; return nil }
func (f *countingForge) Push(context.Context, string, effect.Commit) error   { f.calls++; return nil }

// packages says every event is about tar.
type packages struct{}

func (packages) Package(context.Context, hostile.Slug) (hostile.Slug, error) {
	return hostile.MustParseSlug("tar"), nil
}
