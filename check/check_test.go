package check_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/check"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/internal/fixture/renovate"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

// refusal is what the checker refuses in w: each problem's code, and every message.
func refusal(v *vocab.Vocabulary, w model.Workflow) ([]diag.Code, string) {
	err := check.Check(v, w)
	Expect(err).To(HaveOccurred(), "the checker accepted the model")
	var problems diag.Problems
	Expect(errors.As(err, &problems)).To(BeTrue())
	return problems.Codes(), err.Error()
}

func refused(w model.Workflow) ([]diag.Code, string) { return refusal(renovate.Vocabulary, w) }

// step is the step of w with id, to break.
func step(w model.Workflow, id string) *model.Step {
	for i := range w.Flow {
		if w.Flow[i].ID == id {
			return &w.Flow[i]
		}
	}
	Fail("no step " + id)
	return nil
}

var _ = Describe("The checker", func() {
	It("accepts a model that breaks no rule", func() {
		Expect(check.Check(renovate.Vocabulary, whole())).To(Succeed())
		Expect(check.Check(renovate.Vocabulary, reviewed())).To(Succeed())
	})

	It("names every problem in a model, not only the first", func() {
		w := whole()
		w.Limits.RunsPerHour = 0
		step(w, "pull").Label = ""
		codes, _ := refused(w)
		Expect(codes).To(ConsistOf(diag.ModelDeclaration, diag.ModelUnlabelled))
	})

	Describe("refuses a name the vocabulary does not have", func() {
		It("as a predicate, naming the workflow, step and rule", func() {
			w := whole()
			step(w, "cooled").Decide[0].When = &model.Cond{Pred: "renovate.nope", Args: []model.Ref{"pull"}}
			codes, message := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelUnknownName}))
			Expect(message).To(ContainSubstring(`repairs, step "cooled", rule "young": the vocabulary has no predicate named ` + "`renovate.nope`"))
		})

		It("as a classifier's value", func() {
			w := reviewed()
			step(w, "verdict").Decide[0].When.Is = "maybe"
			codes, _ := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelUnknownName}))
		})

		It("as the event, an observer, a map, an agent or a scan", func() {
			w := whole()
			w.On = "renovate.nothing"
			w.Scan = "renovate.nothing"
			step(w, "pull").Observe.From = "renovate.nothing"
			step(w, "fixer").Agent.Agent = "nobody"
			step(w, "decide").Decide[0].Then[0].Ask.Body.Map = "renovate.nothing"
			codes, _ := refused(w)
			Expect(codes).To(HaveEach(diag.ModelUnknownName))
			Expect(codes).To(HaveLen(5))
		})

		It("as a field of a fact", func() {
			w := whole()
			step(w, "fixer").Agent.Checkout.Sha = "pull.tip"
			codes, _ := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelUnknownName}))
		})
	})

	Describe("refuses a fact read before a step produces it", func() {
		It("in a decide", func() {
			codes, message := refused(readTooEarly())
			Expect(codes).To(Equal([]diag.Code{diag.ModelFactBeforeObserved}))
			Expect(message).To(ContainSubstring(`repairs, step "early", rule "nothing" reads ` + "`pull`"))
		})

		It("in when, which reads only the event", func() {
			codes, message := refused(whenReadsPull())
			Expect(codes).To(Equal([]diag.Code{diag.ModelFactBeforeObserved}))
			Expect(message).To(ContainSubstring("before a run, in when"))
		})

		It("for the attempts, before a run", func() {
			codes, _ := refused(whenReadsAttempts())
			Expect(codes).To(Equal([]diag.Code{diag.ModelFactBeforeObserved}))
		})

		It("in a key or subject", func() {
			w := whole()
			w.Key = append(w.Key, "pull.number")
			w.Subjects = []model.Subject{{Kind: model.RepoSubject, Of: []model.Ref{"pull.repo"}}}
			codes, _ := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelFactBeforeObserved, diag.ModelFactBeforeObserved}))
		})
	})

	Describe("refuses an effect the workflow may not produce", func() {
		It("in a rule", func() {
			w := whole()
			w.May = w.May[:3]
			codes, message := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelEffectNotAllowed}))
			Expect(message).To(ContainSubstring(`step "decide", rule "push" produces an effect of kind Push`))
		})

		It("as a hold, which is an Attest", func() {
			w := holds()
			Expect(check.Check(renovate.Vocabulary, w)).To(Succeed())
			w.May = w.May[1:]
			codes, message := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelEffectNotAllowed}))
			Expect(message).To(ContainSubstring(`step "hold" holds a status`))
		})

		It("as the await of an ask made once", func() {
			w := asksOnceThenAwaits()
			Expect(check.Check(renovate.Vocabulary, w)).To(Succeed())
			w.May = w.May[2:]
			codes, _ := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelEffectNotAllowed}))
		})
	})

	Describe("refuses a hostile fact anywhere but a narrowing predicate, a map, an observer or an agent", func() {
		It("as an effect's repository", func() {
			w := whole()
			step(w, "decide").Decide[1].Then[0].Push.Repo = "pull.text"
			codes, message := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelHostileFact}))
			Expect(message).To(ContainSubstring("uses `pull.text` as the repository of a Push"))
		})

		It("as a hold's commit", func() {
			w := holds()
			step(w, "hold").Hold.Sha = "event.sha"
			Expect(check.Check(renovate.Vocabulary, w)).To(Succeed())
			w = whole()
			w.Flow = append([]model.Step{w.Flow[0], {ID: "hold", Label: "Hold", Kind: model.Hold,
				Hold: &model.HoldStep{Context: trusted, Repo: "event.repo", Sha: "pull.text"}}}, w.Flow[1:]...)
			codes, _ := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelHostileFact}))
		})

		It("in a clean predicate", func() {
			codes, _ := refused(cleanPredicateOfHostile())
			Expect(codes).To(Equal([]diag.Code{diag.ModelHostileFact}))
		})

		It("by looking inside one", func() {
			w := whole()
			step(w, "decide").Decide[0].Then[0].Ask.Body = &model.Body{Showing: "patch.diff"}
			codes, message := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelHostileFact}))
			Expect(message).To(ContainSubstring("a path into patch"))
		})
	})

	Describe("refuses a fact of the wrong type", func() {
		It("for an effect's field", func() {
			w := whole()
			step(w, "decide").Decide[1].Then[0].Push.Repo = "pull.head"
			codes, message := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelWrongType}))
			Expect(message).To(ContainSubstring("which takes a hostile.Slug, and it is a hostile.Sha"))
		})

		It("for a fact declared as another type than its step produces", func() {
			w := whole()
			w.Facts[1].Type = "renovate.SecurityView"
			codes, _ := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelWrongType}))
		})

		It("for a body shown through a map of another type", func() {
			w := whole()
			step(w, "decide").Decide[0].Then[0].Ask.Body = &model.Body{Showing: "logs", Map: renovate.PatchDiff.Name()}
			codes, _ := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelWrongType}))
		})
	})

	It("refuses a title filled from anything but clean numbers, bools, slugs and shas", func() {
		w := titleOfPull()
		codes, message := refused(w)
		Expect(codes).To(Equal([]diag.Code{diag.ModelTitleNotClean}))
		Expect(message).To(ContainSubstring("fills a title with `pull`, a renovate.Pull"))
		step(w, "d").Decide[0].Then[0].Ask.Title.Values = []model.Ref{"pull.text"}
		codes, _ = refused(w)
		Expect(codes).To(Equal([]diag.Code{diag.ModelTitleNotClean}))
	})

	It("refuses hostile input to a worker, which can change things", func() {
		Expect(check.Check(workerReadsNumber())).To(Succeed())
		codes, message := refusal(workerReadsPull())
		Expect(codes).To(Equal([]diag.Code{diag.ModelAgentInputHostile}))
		Expect(message).To(ContainSubstring("gives the worker agent `counter` the input `pull`"))
	})

	Describe("refuses a prompt filled wrongly", func() {
		It("with a variable missing, or one the prompt does not declare", func() {
			codes, message := refused(promptWrongVars())
			Expect(codes).To(Equal([]diag.Code{diag.ModelPromptVars, diag.ModelPromptVars}))
			Expect(message).To(ContainSubstring(`the prompt's variable "repo" is not filled`))
			Expect(message).To(ContainSubstring(`"owner" is not a variable of the prompt`))
		})

		It("from a fact that is not clean", func() {
			codes, _ := refused(promptFromPull())
			Expect(codes).To(Equal([]diag.Code{diag.ModelPromptVars}))
		})
	})

	Describe("refuses a push of anything but a patch a check_patch step accepted", func() {
		It("with no check_patch step", func() {
			codes, message := refused(pushWithoutCheck())
			Expect(codes).To(ContainElement(diag.ModelPushUnchecked))
			Expect(message).To(ContainSubstring(`step "decide", rule "push" pushes ` + "`checked`"))
		})

		It("naming the agent's patch itself", func() {
			w := whole()
			step(w, "decide").Decide[1].Then[0].Push.Checked = "patch"
			codes, _ := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelPushUnchecked}))
		})
	})

	It("refuses a fixer before anything measured the release's age", func() {
		codes, message := refused(fixerFirst())
		Expect(codes).To(Equal([]diag.Code{diag.ModelFixerBeforeMeasure}))
		Expect(message).To(ContainSubstring(`step "fixer" calls the fixer ` + "`fixer`"))
	})

	Describe("refuses a flow that can end having decided nothing", func() {
		It("because it does not end with a decide", func() {
			codes, _ := refused(endsWithObserve())
			Expect(codes).To(Equal([]diag.Code{diag.ModelNoEnding}))
		})

		It("because its last decide has no otherwise rule", func() {
			codes, message := refused(endsWithoutOtherwise())
			Expect(codes).To(Equal([]diag.Code{diag.ModelNoEnding}))
			Expect(message).To(ContainSubstring(`step "d": the last decide has no otherwise rule`))
		})

		It("because a decide has no rules", func() {
			codes, _ := refused(emptyDecide())
			Expect(codes).To(Equal([]diag.Code{diag.ModelNoEnding}))
		})
	})

	Describe("refuses what no run can reach", func() {
		It("a rule after an otherwise rule", func() {
			codes, message := refused(ruleAfterOtherwise())
			Expect(codes).To(ContainElement(diag.ModelUnreachable))
			Expect(message).To(ContainSubstring(`rule "o": it always matches`))
		})

		It("a step after a decide that always ends the run", func() {
			codes, _ := refused(stepAfterOtherwise())
			Expect(codes).To(Equal([]diag.Code{diag.ModelUnreachable}))
		})
	})

	It("refuses a step or rule ID used twice", func() {
		codes, message := refused(stepIDTwice())
		Expect(codes).To(Equal([]diag.Code{diag.ModelDuplicateID}))
		Expect(message).To(ContainSubstring("uses the ID `pull`"))
		codes, _ = refused(ruleIDTwice())
		Expect(codes).To(Equal([]diag.Code{diag.ModelDuplicateID}))
	})

	It("refuses a fact produced twice", func() {
		codes, message := refused(pullTwice())
		Expect(codes).To(ContainElement(diag.ModelDuplicateFact))
		Expect(message).To(ContainSubstring("produces the fact `pull`"))
		codes, _ = refused(measureTwice())
		Expect(codes).To(Equal([]diag.Code{diag.ModelDuplicateFact}))
	})

	It("refuses a step or rule with no ID or no label", func() {
		codes, _ := refused(unlabelled())
		Expect(codes).To(Equal([]diag.Code{diag.ModelUnlabelled, diag.ModelUnlabelled}))
	})

	Describe("refuses a declaration that is missing or out of range", func() {
		It("with no intent, no runs per hour, or no key", func() {
			codes, _ := refused(bare())
			Expect(codes).To(Equal([]diag.Code{diag.ModelDeclaration, diag.ModelDeclaration, diag.ModelDeclaration}))
		})

		It("testing spent attempts with no attempts limit", func() {
			w := testsSpent()
			Expect(check.Check(renovate.Vocabulary, w)).To(Succeed())
			w.Limits.Attempts = 0
			codes, message := refused(w)
			Expect(codes).To(Equal([]diag.Code{diag.ModelDeclaration}))
			Expect(message).To(ContainSubstring("declares no attempts limit"))
		})

		It("naming a fact as a built-in one", func() {
			codes, _ := refused(factNamedAge())
			Expect(codes).To(ContainElement(diag.ModelDeclaration))
		})

		It("with a step whose kind and contents disagree", func() {
			w := whole()
			step(w, "pull").Kind = model.Measure
			codes, _ := refused(w)
			Expect(codes).To(ContainElement(diag.ModelDeclaration))
		})
	})

	It("refuses every model against a vocabulary with problems of its own", func() {
		v := vocab.New()
		vocab.Predicate(v, "age.cooled", "again", func(Count) bool { return true })
		codes, _ := refusal(v, whole())
		Expect(codes).To(Equal([]diag.Code{diag.VocabDuplicate}))
	})
})
