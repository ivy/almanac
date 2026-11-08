package engine_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/engine"
	"github.com/ivy/almanac/internal/fixture/sample"
	"github.com/ivy/almanac/internal/fixture/workflows"
	"github.com/ivy/almanac/workflow"
)

// errorReported is an application error, with a message an attacker wrote.
var errorReported = json.RawMessage(`{"issue_id": 42, "repo": "` + repo + `", "message": "TypeError: ` + injection + `"}`)

// investigated is a recorded run of error_triage that the screen let through, and that the
// investigator answered with confidence.
func investigated(confidence string) *recorded {
	r := newRecorded()
	r.screened = 0.9
	r.answers["investigator"] = `{"confidence": "` + confidence + `", "summary": "A nil map in the handler. ` + injection + `"}`
	return r
}

// filed is work or a question about the error, as the outbox stores it.
func filed(kind, title string) string {
	return `[{"` + kind + `": {"` + map[string]string{"File": "title", "Ask": "question"}[kind] + `": "` + title + `", "target": "` + repo + `",
		"body": {"$untrusted": "A nil map in the handler. ` + injection + `"}}}]`
}

var errorTriageEndings = []ending{
	{"an error the screen says is not worth a look",
		func() *recorded { r := investigated("high"); r.screened = 0.2; return r }, "worth_it", "", `[]`},
	{"an investigator sure enough to file",
		func() *recorded { return investigated("high") }, "triage", "confident", filed("File", "Fix this application error")},
	{"an investigator who is not sure",
		func() *recorded { return investigated("low") }, "triage", "unsure", filed("Ask", "Is this error worth fixing?")},
}

var _ = Describe("error_triage, walked over recorded facts", func() {
	var et *engine.Workflow

	BeforeEach(func() {
		var err error
		et, err = engine.New(sample.Vocabulary, workflows.ErrorTriage)
		Expect(err).NotTo(HaveOccurred())
	})

	It("has a recorded run for every ending of the model, and only those", func() {
		Expect(stepsAndRules(errorTriageEndings)).To(ConsistOf(endingsOf(workflows.ErrorTriage)))
	})

	DescribeTable("reaches each ending from the facts recorded for it",
		func(e ending) {
			r := e.run()
			ended, err := et.Walk(context.Background(), r, errorReported)
			if e.rule == "" {
				var halt *workflow.Halt
				Expect(err).To(BeAssignableToTypeOf(halt))
				Expect(r.calls).To(BeEmpty(), "a screened-out error costs one classifier call and no investigation")
			} else {
				Expect(err).NotTo(HaveOccurred())
				Expect(r.calls).To(HaveLen(1))
			}
			Expect([]string{ended.Step, ended.Rule}).To(Equal([]string{e.step, e.rule}))
			Expect(planned(ended.Decided.Effects)).To(MatchJSON(e.effects))
			Expect(r.screens).To(Equal([]string{"Is this error worth an engineer's attention?"}))
		},
		entries(errorTriageEndings),
	)

	It("gives the investigator the message by name, still hostile", func() {
		r := investigated("high")
		_, err := et.Walk(context.Background(), r, errorReported)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.inputs[0]).To(MatchJSON(`{"message": {"$untrusted": "TypeError: ` + injection + `"}}`))
	})
})
