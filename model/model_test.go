package model_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
)

// small is a workflow written as data, without the DSL.
func small() model.Workflow {
	when := model.Cond{Pred: "demo.failing", Args: []model.Ref{"pull"}}
	return model.Workflow{
		Name:   "demo",
		Intent: "Say when a build fails.",
		On:     "demo.build",
		Key:    []model.Ref{"event.repo"},
		May:    []effect.Kind{effect.KindAsk},
		Limits: model.Limits{RunsPerHour: 5},
		Facts:  []model.Fact{{Name: "event", Type: "demo.Build"}, {Name: "pull", Type: "demo.Pull"}},
		Flow: []model.Step{
			{ID: "pull", Label: "Read the pull request", Kind: model.Observe,
				Observe: &model.ObserveStep{Fact: "pull", From: "demo.pull", Args: []model.Ref{"event"}}},
			{ID: "screen", Label: "Worth a look?", Kind: model.Screen,
				Screen: &model.ScreenStep{Question: "Is it <bad>?", Text: "event.text", Above: 0.5}},
			{ID: "cooldown", Label: "How old?", Kind: model.Measure,
				Measure: &model.MeasureStep{From: "demo.published", Args: []model.Ref{"event"}, MinAge: model.Duration(72 * time.Hour)}},
			{ID: "decide", Label: "Failing?", Kind: model.Decide, Decide: []model.Rule{
				{ID: "failing", Label: "It failed", When: &when, Then: []model.Effect{{Ask: &model.AskEffect{
					Target: "event.repo", Title: model.Title{Format: hostile.NewFormat("Build %d failed")},
					Once: true, Body: &model.Body{Showing: "event.text"},
				}}}},
				{ID: "fine", Label: "It is fine", Then: []model.Effect{}},
			}},
		},
	}
}

var _ = Describe("A model", func() {
	Describe("its canonical form", func() {
		It("sorts every object's keys, keeps array order, and escapes no HTML", func() {
			canonical, err := model.Canonical(small())
			Expect(err).NotTo(HaveOccurred())
			Expect(string(canonical)).To(HavePrefix(`{"facts":[{"name":"event","type":"demo.Build"},{"name":"pull","type":"demo.Pull"}],"flow":[{"id":"pull","kind":"observe","label":"Read the pull request","observe":{"args":["event"],"fact":"pull","from":"demo.pull"}},{"id":"screen","kind":"screen","label":"Worth a look?","screen":{"above":0.5,"question":"Is it <bad>?","text":"event.text"}}`))
			Expect(string(canonical)).To(ContainSubstring(`"measure":{"args":["event"],"from":"demo.published","min_age":"72h0m0s"}`))
			Expect(string(canonical)).To(HaveSuffix(`"intent":"Say when a build fails.","key":["event.repo"],"limits":{"runs_per_hour":5},"may":["Ask"],"name":"demo","on":"demo.build"}`))
		})

		It("writes a rule with no condition as otherwise, by leaving its when out", func() {
			canonical, err := model.Canonical(small())
			Expect(err).NotTo(HaveOccurred())
			Expect(string(canonical)).To(ContainSubstring(`{"id":"fine","label":"It is fine","then":[]}`))
		})

		It("is the same bytes every time", func() {
			first, err := model.Canonical(small())
			Expect(err).NotTo(HaveOccurred())
			for range 20 {
				again, err := model.Canonical(small())
				Expect(err).NotTo(HaveOccurred())
				Expect(again).To(Equal(first))
			}
		})

		It("cannot be read back, because a title's format comes only from a constant", func() {
			canonical, err := model.Canonical(small())
			Expect(err).NotTo(HaveOccurred())
			var w model.Workflow
			Expect(json.Unmarshal(canonical, &w)).NotTo(Succeed())
		})
	})

	Describe("its digest", func() {
		It("names the canonical form", func() {
			digest, err := model.Digest(small())
			Expect(err).NotTo(HaveOccurred())
			Expect(digest).To(MatchRegexp(`^sha256:[0-9a-f]{64}$`))
		})

		It("changes when anything a person reads changes, even a label", func() {
			before, err := model.Digest(small())
			Expect(err).NotTo(HaveOccurred())
			relabelled := small()
			relabelled.Flow[0].Label = "Read the pull request again"
			after, err := model.Digest(relabelled)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).NotTo(Equal(before))
		})
	})

	It("lists every way a run can end, in flow order", func() {
		var endings []string
		for _, e := range small().Endings() {
			endings = append(endings, e.Step+"/"+e.Rule+": "+e.Label)
		}
		Expect(endings).To(Equal([]string{"screen/: Worth a look?", "decide/failing: It failed", "decide/fine: It is fine"}))
	})

	It("lists every reference to a fact, with the attempts an ask made once reads", func() {
		Expect(small().Refs()).To(Equal([]model.Ref{
			"event.repo", "event", "event.text", "event", "pull", "event.repo", "attempts", "event.text",
		}))
	})

	It("splits a reference into its fact and the path inside it", func() {
		Expect(model.Ref("pull.head.sha").Fact()).To(Equal("pull"))
		Expect(model.Ref("pull.head.sha").Path()).To(Equal([]string{"head", "sha"}))
		Expect(model.Ref("pull").Path()).To(BeEmpty())
	})

	It("clones into a copy that shares no memory", func() {
		original := small()
		clone := original.Clone()
		Expect(clone).To(Equal(original))
		clone.Flow[0].Observe.Args[0] = "changed"
		clone.Flow[3].Decide[0].When.Args[0] = "changed"
		clone.Flow[3].Decide[0].Then[0].Ask.Body.Showing = "changed"
		clone.Key[0] = "changed"
		Expect(original).To(Equal(small()))
	})

	It("names an effect template's kind by the field it sets", func() {
		Expect(model.Effect{Push: &model.PushEffect{}}.Kind()).To(Equal(effect.KindPush))
		Expect(model.Effect{}.Kind()).To(BeZero())
	})
})
