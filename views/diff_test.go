package views_test

import (
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/buildfailures"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/views"
)

// proposed is build_failures as a change might propose it: stop at once on a pull request that
// is not failing, give up after two attempts instead of three, and no longer refuse a repair
// that takes back the update.
func proposed() model.Workflow {
	w := buildfailures.Model.Clone()
	w.Limits.Attempts = 2
	stop := model.Step{
		ID:    "stop",
		Label: "Stop on a passing build",
		Kind:  model.Decide,
		Decide: []model.Rule{{
			ID:    "not_failing",
			Label: "CI is not failing",
			When:  &model.Cond{Not: &model.Cond{Pred: renovate.Failing.Name(), Args: []model.Ref{"pull"}}},
			Then:  []model.Effect{},
		}},
	}
	at := slices.IndexFunc(w.Flow, func(s model.Step) bool { return s.ID == "actionable" })
	w.Flow = slices.Insert(w.Flow, at+1, stop)
	last := &w.Flow[len(w.Flow)-1]
	last.Decide = slices.DeleteFunc(last.Decide, func(r model.Rule) bool { return r.ID == "undid" })
	return w
}

func ids(os []views.Outcome) []string {
	var out []string
	for _, o := range os {
		out = append(out, o.ID())
	}
	return out
}

func endings(cs []views.Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Ending)
	}
	return out
}

var _ = Describe("Diff", func() {
	It("finds no change between a version and itself", func() {
		c, err := views.Diff(renovate.Vocabulary, buildfailures.Model, buildfailures.Model.Clone())
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Empty()).To(BeTrue())
		Expect(c.Old).To(Equal(c.New))
	})

	Describe("between build_failures and a proposed change", func() {
		var c views.Changes

		BeforeEach(func() {
			var err error
			c, err = views.Diff(renovate.Vocabulary, buildfailures.Model, proposed())
			Expect(err).NotTo(HaveOccurred())
		})

		It("lists the ending the stop rule adds", func() {
			Expect(ids(c.Added)).To(Equal([]string{"stop/not_failing"}))
			Expect(c.Added[0].When.String()).To(Equal("unless CI failed on the pull request's head"))
			Expect(c.Added[0].Effects).To(BeEmpty())
		})

		It("lists the ending the change removes", func() {
			Expect(ids(c.Removed)).To(Equal([]string{"decide/undid"}))
		})

		It("lists the endings whose own condition the attempts cap changes", func() {
			Expect(endings(c.Changed)).To(Equal([]string{"attempts/asked", "attempts/exhausted"}))
			Expect(c.Changed[1].Notes).To(ContainElement(
				"ends when: “if the key's attempts are spent (3 allowed)” → “if the key's attempts are spent (2 allowed)”"))
		})

		It("lists the endings reached another way, and leaves alone those before the change", func() {
			Expect(endings(c.PathOnly)).To(Equal([]string{
				"cooled/undated", "cooled/cooling", "decide/empty", "decide/refused", "decide/push",
			}))
			var push views.Change
			for _, ch := range c.PathOnly {
				if ch.Ending == "decide/push" {
					push = ch
				}
			}
			Expect(push.Notes).To(ContainElements(
				"on the path, added: if CI failed on the pull request's head, at stop/not_failing",
				"on the path, added: unless the key's attempts are spent (2 allowed), at attempts/exhausted",
				"on the path, removed: unless the key's attempts are spent (3 allowed), at attempts/exhausted",
				"on the path, removed: unless the patch takes back a line the update added, outside a lockfile, at decide/undid",
				"step passed, added: stop",
			))
			Expect(endings(c.Changed)).NotTo(ContainElement(HavePrefix("actionable/")))
			Expect(endings(c.PathOnly)).NotTo(ContainElement(HavePrefix("actionable/")))
		})

		It("lists the settings and steps that changed", func() {
			Expect(c.Settings).To(Equal([]views.Setting{{Name: "attempts", Old: "3", New: "2"}}))
			Expect(c.Steps).To(Equal([]views.StepChange{
				{ID: "stop", Label: "Stop on a passing build", Change: views.Added},
				{ID: "decide", Label: "What did the fixer produce?", Change: views.Changed, Notes: []string{
					"what it does: “tries 4 rules in order, and the first that matches ends the run” → “tries 3 rules in order, and the first that matches ends the run”",
				}},
			}))
		})
	})

	It("refuses a version the checker refuses", func() {
		broken := proposed()
		broken.Flow[2].Decide[0].When.Not.Pred = "renovate.no_such_predicate"
		_, err := views.Diff(renovate.Vocabulary, buildfailures.Model, broken)
		Expect(err).To(MatchError(ContainSubstring("the new version")))
	})
})
