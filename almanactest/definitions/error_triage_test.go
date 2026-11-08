package definitions_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/almanactest/definitions"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
)

var _ = Describe("error_triage, on the memory store", func() {
	samples(onMemory, errorTriage)
})

var _ = Describe("error_triage, on PostgreSQL", Label("db"), func() {
	samples(onPostgres, errorTriage)
})

// triage is one way error_triage can end: how likely the classifier thought the error worth a
// look, how sure the investigator was, and what the run came to.
type triage struct {
	name       string
	screened   float64
	confidence string
	ending     string
	result     runtime.Result
	decides    []effect.Summary
}

var triageTable = []triage{
	{"an error the screen says is not worth a look", 0.2, "High", "worth_it/", runtime.Stopped, []effect.Summary{}},
	{"an investigator sure enough to file", 0.9, "High", "triage/confident", runtime.Ran, []effect.Summary{files}},
	{"an investigator who is not sure", 0.9, "Low", "triage/unsure", runtime.Ran, []effect.Summary{asks}},
}

func triageEntries() []TableEntry {
	entries := make([]TableEntry, len(triageTable))
	for i, t := range triageTable {
		entries[i] = Entry(t.name, t)
	}
	return entries
}

func errorTriage(w *world) {
	It("has a row for every ending of the model, and only those", func() {
		var endings []string
		for _, t := range triageTable {
			endings = append(endings, t.ending)
		}
		Expect(endings).To(ConsistOf(endingsOf(definitions.ErrorTriage)))
	})

	DescribeTable("reaches each ending",
		func(t triage) {
			w.classifier = almanactest.Answering(t.screened)
			w.investigator.Reply(findings(t.confidence))
			rt := w.runtime(definitions.ErrorTriage)
			Expect(w.ingest(rt, issue("s1"))).To(Equal(store.Queued))
			outcomes := w.drain(rt)
			Expect(outcomes).To(HaveLen(1))
			Expect(outcomes[0].Result).To(Equal(t.result), outcomes[0].Reason)
			Expect(summaries(outcomes[0])).To(Equal(t.decides))
			Expect(w.classifier.Calls()).To(Equal(1), "every error costs one classifier call")
		},
		triageEntries(),
	)

	It("files the investigator's summary in the repository the report names, still hostile", func() {
		outcome := w.decided(w.runtime(definitions.ErrorTriage), issue("s1"))
		Expect(outcome.Effects).To(HaveLen(1))
		filed, ok := outcome.Effects[0].(effect.File)
		Expect(ok).To(BeTrue())
		Expect(filed.Target).To(Equal(hostile.MustParseSlug(repo)))
		Expect(mustJSON(filed.Body)).To(MatchJSON(mustJSON(hostile.New("A nil map in the handler. " + injection))))
	})

	It("gives the investigator the message by name, still hostile", func() {
		w.decided(w.runtime(definitions.ErrorTriage), issue("s1"))
		Expect(w.investigator.Inputs()).To(HaveExactElements(MatchJSON(mustJSON(map[string]any{"message": hostile.New(injection)}))))
		Expect(w.investigator.Prompts()[0]).To(ContainSubstring("# Investigate an application error"))
		Expect(w.investigator.Prompts()[0]).NotTo(ContainSubstring("IGNORE PREVIOUS"))
	})
}

var _ = Describe("The samples' manifest", func() {
	It("finds the agents and providers each model uses", func() {
		manifest := runtime.New(runnable(definitions.ErrorTriage), providers.New()).Manifest()
		Expect(manifest).To(HaveLen(1))
		Expect(manifest[0].Workflow).To(Equal("error_triage"))
		Expect(manifest[0].Agents).To(HaveExactElements(HaveField("Name", "investigator")))
		Expect(names(manifest[0].Requires)).To(ContainElement("agent.Classifier"))
	})

	It("names the prompt file and its variables", func() {
		manifest := runtime.New(runnable(definitions.DependencyUpdates), providers.New()).Manifest()
		Expect(manifest[0].Agents).To(HaveLen(1))
		Expect(manifest[0].Agents[0].Prompt).To(Equal("reviewer.md"))
		Expect(manifest[0].Agents[0].PromptVars).To(Equal([]string{"repo"}))
	})

	It("shows each agent's capabilities", func() {
		manifest := runtime.New(runnable(definitions.DependencyUpdates), providers.New()).Manifest()
		reviewer := manifest[0].Agents[0]
		Expect(reviewer.Name).To(Equal("reviewer"))
		Expect(reviewer.Profile).To(Equal("quarantined"))
		Expect(reviewer.Grant.Egress).To(BeFalse())
		Expect(manifest[0].On).To(Equal(opened))
		Expect(names(manifest[0].Requires)).To(ContainElement("definitions.Registry"))
	})
})

// names are the names of what a manifest requires.
func names(requires []providers.Requirement) []string {
	out := make([]string, len(requires))
	for i, r := range requires {
		out[i] = r.Name()
	}
	return out
}
