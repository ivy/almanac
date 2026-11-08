package views_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/buildfailures"
	"github.com/ivy/almanac/almanacs/renovate/securityfixes"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/views"
)

// ending is an outcome reduced to what model.Endings says of it.
type ending struct {
	Step, Rule, Label string
	Effects           []effect.Kind
}

func fromModel(w model.Workflow) []ending {
	var out []ending
	for _, e := range w.Endings() {
		kinds := []effect.Kind{}
		for _, each := range e.Effects {
			kinds = append(kinds, each.Kind())
		}
		out = append(out, ending{e.Step, e.Rule, e.Label, kinds})
	}
	return out
}

func fromOutcomes(s views.Summary) []ending {
	var out []ending
	for _, o := range s.Endings {
		kinds := []effect.Kind{}
		for _, each := range o.Effects {
			kinds = append(kinds, each.Kind)
		}
		out = append(out, ending{o.Step, o.Rule, o.Label, kinds})
	}
	return out
}

var _ = Describe("Outcomes", func() {
	DescribeTable("lists exactly the model's endings, in flow order, with their labels and effects",
		func(w model.Workflow, count int) {
			s, err := views.Outcomes(renovate.Vocabulary, w)
			Expect(err).NotTo(HaveOccurred())
			Expect(fromOutcomes(s)).To(Equal(fromModel(w)))
			Expect(s.Endings).To(HaveLen(count))
			for _, o := range s.Endings {
				Expect(o.Label).NotTo(BeEmpty())
				Expect(o.StepLabel).NotTo(BeEmpty())
				for _, e := range o.Effects {
					Expect(e.Text).NotTo(BeEmpty(), o.ID())
				}
			}
		},
		Entry("build_failures", buildfailures.Model, 11),
		Entry("security_fixes", securityfixes.Model, 11),
	)

	It("lists the conditions on the path to an ending, in the order a run meets them", func() {
		s, err := views.Outcomes(renovate.Vocabulary, buildfailures.Model)
		Expect(err).NotTo(HaveOccurred())
		exhausted := find(s, "attempts/exhausted")

		Expect(exhausted.When.String()).To(Equal("if the key's attempts are spent (3 allowed)"))
		Expect(exhausted.Path).To(HaveLen(5))
		Expect(exhausted.Path[0].String()).To(Equal("if CI failed on a Renovate branch, or finished on a commit Almanac patched"))
		Expect(exhausted.Path[0].At).To(Equal("when"))
		// A rule written as Not(failing) that did not match is a run on which CI failed.
		Expect(exhausted.Path[3].String()).To(Equal("if CI failed on the pull request's head"))
		Expect(exhausted.Path[3].At).To(Equal("actionable/green"))
		Expect(exhausted.Path[4].String()).To(Equal("unless (the key's attempts are spent (3 allowed) and a person was already asked)"))
		Expect(exhausted.Through).To(Equal([]string{"pull", "actionable"}))
	})

	It("names the agents called and the holds set on the way to an ending", func() {
		s, err := views.Outcomes(renovate.Vocabulary, buildfailures.Model)
		Expect(err).NotTo(HaveOccurred())

		push := find(s, "decide/push")
		Expect(push.When).To(BeNil())
		Expect(push.Agents).To(Equal([]string{"fixer"}))
		Expect(push.Along).To(ConsistOf(HaveField("Text", "set almanac/trust pending on pull.head in event.repo")))
		Expect(push.Effects).To(ConsistOf(HaveField("Text", ContainSubstring("push the checked patch to event.branch"))))

		cooling := find(s, "cooled/cooling")
		Expect(cooling.Agents).To(BeEmpty())
		Expect(cooling.Along).To(BeEmpty())
		Expect(cooling.When.String()).To(Equal("if the release is younger than the cooldown (72h)"))
	})

	It("tells a classifier's answer, and a question that shows a hostile fact through a map", func() {
		s, err := views.Outcomes(renovate.Vocabulary, securityfixes.Model)
		Expect(err).NotTo(HaveOccurred())
		concerns := find(s, "verdict/concerns")
		Expect(concerns.When.String()).To(Equal("if what the reviewer concluded about the update: concerns"))
		Expect(concerns.Effects[1].Text).To(ContainSubstring("showing the reviewer's notes (review)"))
		Expect(concerns.Effects[1].Text).To(HaveSuffix("nothing, if one was already asked"))
	})

	It("survives JSON, so an old version's outcomes can be compared later", func() {
		s, err := views.Outcomes(renovate.Vocabulary, securityfixes.Model)
		Expect(err).NotTo(HaveOccurred())
		raw, err := json.Marshal(s)
		Expect(err).NotTo(HaveOccurred())
		var back views.Summary
		Expect(json.Unmarshal(raw, &back)).To(Succeed())
		Expect(back).To(Equal(s))
		Expect(views.Compare(back, s).Empty()).To(BeTrue())
	})

	It("refuses a model the checker refuses", func() {
		broken := buildfailures.Model.Clone()
		broken.Flow[0].Observe.From = "renovate.no_such_observer"
		_, err := views.Outcomes(renovate.Vocabulary, broken)
		Expect(err).To(MatchError(ContainSubstring("renovate.no_such_observer")))
	})
})

var _ = Describe("Where hostile facts go", func() {
	allowed := []views.UseKind{views.AsNarrowing, views.AsMap, views.AsText, views.AsAgent, views.AsObserver, views.AsPublishTime, views.AsScreen, views.AsPolicy}

	DescribeTable("lists every hostile fact the model declares, read only where the checker allows",
		func(w model.Workflow) {
			s, err := views.Outcomes(renovate.Vocabulary, w)
			Expect(err).NotTo(HaveOccurred())
			var listed []string
			for _, h := range s.Hostile {
				listed = append(listed, string(h.Ref))
				for _, u := range h.Uses {
					Expect(allowed).To(ContainElement(u.As), string(h.Ref))
				}
			}
			for _, f := range w.Facts {
				if f.Hostile {
					Expect(listed).To(ContainElement(f.Name))
				}
			}
		},
		Entry("build_failures", buildfailures.Model),
		Entry("security_fixes", securityfixes.Model),
	)

	It("follows build_failures' hostile facts to every reader", func() {
		s, err := views.Outcomes(renovate.Vocabulary, buildfailures.Model)
		Expect(err).NotTo(HaveOccurred())
		Expect(uses(s)).To(Equal(map[string][]string{
			"pull.text": {"cooldown publish_time renovate.newest_release"},
			"logs":      {"fixer agent fixer"},
			"updated":   {"decide/undid narrowing renovate.undoes_update"},
			"patch": {
				"check policy patch.default",
				"decide/empty map renovate.closing_words",
				"decide/refused map renovate.patch_diff",
				"decide/undid narrowing renovate.undoes_update",
				"decide/undid map renovate.patch_diff",
			},
		}))
	})

	It("follows security_fixes' hostile facts to every reader, including a hostile field of a clean fact", func() {
		s, err := views.Outcomes(renovate.Vocabulary, securityfixes.Model)
		Expect(err).NotTo(HaveOccurred())
		Expect(uses(s)).To(Equal(map[string][]string{
			"pull.text": {
				"advisory observer renovate.advisory",
				"cooldown publish_time renovate.newest_release",
				"diff observer renovate.package_diff",
			},
			"diff.unshown": {"checks/hidden narrowing renovate.hides_files", "checks/hidden text "},
			"diff.diff":    {"review agent reviewer"},
			"review":       {"verdict/concerns narrowing reviewer.verdict", "verdict/concerns map reviewer.notes"},
		}))
		Expect(s.Hostile[0].From).To(Equal("pull"))
	})
})

func find(s views.Summary, id string) views.Outcome {
	for _, o := range s.Endings {
		if o.ID() == id {
			return o
		}
	}
	Fail("no ending " + id)
	return views.Outcome{}
}

// uses are each hostile fact's readers, as "where kind part".
func uses(s views.Summary) map[string][]string {
	out := map[string][]string{}
	for _, h := range s.Hostile {
		out[string(h.Ref)] = []string{}
		for _, u := range h.Uses {
			where := u.Step
			if u.Rule != "" {
				where += "/" + u.Rule
			}
			out[string(h.Ref)] = append(out[string(h.Ref)], where+" "+string(u.As)+" "+u.Part)
		}
	}
	return out
}
