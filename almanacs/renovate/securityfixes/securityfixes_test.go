package securityfixes_test

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/store"
)

var _ = Describe("security_fixes, on the memory store", func() { securityFixes(onMemory) })

var _ = Describe("security_fixes, on PostgreSQL", Label("db"), func() { securityFixes(onPostgres) })

// securityFixes are the workflow's specs, against simforge, a registry and an advisory feed held
// in memory, and a scripted reviewer, run by a live runtime over the store fresh opens.
func securityFixes(fresh func(context.Context) storeOpener) {
	var (
		ctx  context.Context
		open storeOpener
	)

	BeforeEach(func(sc SpecContext) {
		ctx = context.WithoutCancel(sc)
		open = fresh(ctx)
	})

	given := func(s scenario, p simforge.Pull) *world { return newWorld(ctx, open, s, p) }
	run := func(s scenario) *world {
		w := given(s, securityPull())
		outcome := w.run()
		Expect(outcome.Result).To(Equal(runtime.Ran), outcome.Reason)
		return w
	}

	It("a valid fix goes pending, then success", func() {
		w := run(valid)
		Expect(w.states()).To(Equal([]string{"pending", "success"}))
		Expect(w.reviewer.Calls()).To(Equal(1))
		Expect(w.asked()).To(BeEmpty())

		By("giving the reviewer the diff, still hostile, and nothing to write with")
		Expect(w.reviewer.Grants()[0].Write).To(BeFalse())
		var input map[string]map[string]string
		Expect(json.Unmarshal(w.reviewer.Inputs()[0], &input)).To(Succeed())
		Expect(input).To(HaveKey("diff"))
		Expect(input["diff"]["$untrusted"]).To(ContainSubstring(injection))
		Expect(w.reviewer.Prompts()[0]).NotTo(ContainSubstring(injection))
	})

	It("replays a verdict with everything it read unreachable, and reaches the same effects", func() {
		w := given(valid, securityPull())
		first := w.run()
		Expect(first.Result).To(Equal(runtime.Ran), first.Reason)
		again := w.replayDown(first.RunID)
		Expect(again.Result).To(Equal(runtime.Ran), again.Reason)
		Expect(again.RunID).To(Equal(first.RunID))
		Expect(planned(again.Effects)).To(MatchJSON(planned(first.Effects)))
		Expect(w.walked.last()).To(Equal("verdict/tested"))
		Expect(w.reviewer.Calls()).To(Equal(1), "the reviewer is not asked again")
		Expect(w.states()).To(Equal([]string{"pending", "success"}), "a replay applies nothing")
	})

	It("pending is posted before any other work", func() {
		w := given(valid, securityPull())
		w.sim.FailRequests("/pulls/", simforge.ServerError(404))
		Expect(w.deliver("opened")).To(Equal(store.Queued))
		outcomes := w.settle()
		Expect(outcomes).To(ConsistOf(HaveField("Result", runtime.Failed)))
		Expect(w.states()).To(Equal([]string{"pending"}), "held even though the read failed")
	})

	It("a target that is not the patched version fails and asks", func() {
		s := valid
		s.namesTheFix = false
		w := run(s)
		Expect(w.states()).To(Equal([]string{"pending", "failure"}))
		Expect(w.asked()).To(HaveLen(1))
		Expect(w.reviewer.Calls()).To(BeZero(), "no model is asked once a check fails")
	})

	It("a brand new fix asks a person and stays pending", func() {
		s := valid
		s.hoursOld = 2
		w := run(s)
		Expect(w.states()).To(Equal([]string{"pending"}))
		Expect(w.asked()).To(HaveLen(1))
		Expect(w.reviewer.Calls()).To(BeZero())
	})

	It("an injected all-clear cannot pass a failed check", func() {
		s := valid
		s.installScript = true
		s.verdict = "NoConcerns"
		w := run(s)
		Expect(w.states()).To(Equal([]string{"pending", "failure"}))
	})

	It("a reviewer's concerns veto passing checks", func() {
		s := valid
		s.verdict = "Concerns"
		w := run(s)
		Expect(w.states()).To(Equal([]string{"pending", "failure"}))
		Expect(w.asked()).To(HaveLen(1))
	})

	It("a package that cannot be read fails closed", func() {
		s := valid
		s.readable = false
		w := run(s)
		Expect(w.states()).To(Equal([]string{"pending", "failure"}))
	})

	It("the label also triggers it", func() {
		p := simforge.RenovatePull("renovate/tar-7.x", "Update tar")
		p.Labels = append(p.Labels, "security")
		w := given(valid, p)
		Expect(w.deliver("opened")).To(Equal(store.Queued))
	})

	It("an ordinary update is ignored", func() {
		w := given(valid, simforge.RenovatePull("renovate/tar-7.x", "Update tar"))
		Expect(w.deliver("opened")).To(Equal(store.Ignored))
	})

	It("a person's branch with a matching name is left alone", func() {
		p := securityPull()
		p.Author = "someone"
		w := given(valid, p)
		w.run()
		Expect(w.states()).To(Equal([]string{"pending"}), "held until read, then nothing else")
		Expect(w.reviewer.Calls()).To(BeZero())
	})

	It("opened and labeled together ask once", func() {
		s := valid
		s.hoursOld = 2
		w := given(s, securityPull())
		w.run()
		w.sim.AddLabel(repoName, w.number, "security")
		Expect(w.deliver("labeled")).To(Equal(store.Queued))
		again := w.settle()
		Expect(again).To(ConsistOf(HaveField("Result", runtime.Ran)))
		Expect(again[0].Effects).To(BeEmpty(), "a person was asked already")
		Expect(w.asked()).To(HaveLen(1))
	})

	It("a scan finds a security pull request with no verdict", func() {
		w := given(valid, securityPull())
		found, err := w.rt.Backfill(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(found.Queued).To(Equal(1))
		w.settle()
		Expect(w.states()).To(Equal([]string{"pending", "success"}))
	})

	It("a scan leaves a decided pull request alone", func() {
		w := run(valid)
		found, err := w.rt.Backfill(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(found.Found).To(BeZero())
	})

	// Each row is the effects a run decides, from what the checks and the reviewer say. A run
	// with no review is one whose reviewer said nothing that decides it: no concerns, about an
	// update nothing has tested yet and CI still could.
	DescribeTable("the policy table",
		func(namesTheFix, install, cooled bool, verdict string, effects []string) {
			s := valid
			s.namesTheFix, s.installScript, s.verdict = namesTheFix, install, verdict
			if !cooled {
				s.hoursOld = 2
			}
			if verdict == "" {
				s.verdict, s.tested, s.workflows, s.at = "NoConcerns", false, true, opened.Add(5*time.Minute)
			}
			outcome := given(s, securityPull()).run()
			Expect(outcome.Result).To(Equal(runtime.Ran), outcome.Reason)
			Expect(kinds(outcome.Effects)).To(Equal(effects))
		},
		Entry(nil, true, false, true, "NoConcerns", []string{"Success"}),
		Entry(nil, true, false, true, "Concerns", []string{"Failure", "Ask"}),
		Entry(nil, false, false, true, "NoConcerns", []string{"Failure", "Ask"}),
		Entry(nil, true, true, true, "NoConcerns", []string{"Failure", "Ask"}),
		Entry(nil, true, false, false, "", []string{"Ask"}),
		Entry(nil, true, false, true, "", []string{}),
	)

	It("a model can never approve what a check refused", func() {
		for _, namesTheFix := range []bool{true, false} {
			for _, install := range []bool{true, false} {
				for _, cooled := range []bool{true, false} {
					s := valid
					s.namesTheFix, s.installScript, s.verdict = namesTheFix, install, "NoConcerns"
					if !cooled {
						s.hoursOld = 2
					}
					// Each is a forge and a store of its own.
					w := newWorld(ctx, fresh(ctx), s, securityPull())
					Expect(w.run().Result).To(Equal(runtime.Ran))
					passed := namesTheFix && !install && cooled
					approved := slices.Contains(w.states(), "success")
					Expect(approved).To(Equal(passed), "names the fix %t, install script %t, cooled %t", namesTheFix, install, cooled)
					Expect(w.reviewer.Calls() > 0).To(Equal(passed), "the reviewer is asked only once every check passed")
				}
			}
		}
	})

	// To Renovate a success is a passing check, so this would merge an update nothing tested.
	It("a repository with no checks is never attested, and asks a person", func() {
		s := valid
		s.tested = false
		w := run(s)
		Expect(w.states()).NotTo(ContainElement("success"))
		Expect(w.asked()).To(ConsistOf(ContainSubstring("Nothing tests this repository")))
	})

	// Workflows can exist and never run on a pull request, which must not hold a fix in silence.
	It("CI gets an hour to report", func() {
		s := valid
		s.tested, s.workflows, s.at = false, true, opened.Add(5*time.Minute)
		w := given(s, securityPull())
		found, err := w.rt.Backfill(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(found.Queued).To(Equal(1))
		w.settle()
		Expect(w.walked.last()).To(Equal("verdict/waiting"))
		Expect(w.asked()).To(BeEmpty(), "five minutes is not a long wait")

		w.Clock.Advance(56 * time.Minute)
		found, err = w.rt.Backfill(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(found.Queued).To(Equal(1), "the scan looks again once CI has had its hour")
		w.settle()
		Expect(w.walked.last()).To(Equal("verdict/untestable"))
		Expect(w.asked()).To(ConsistOf(ContainSubstring("Nothing tests this repository")))
		Expect(w.states()).NotTo(ContainElement("success"))
	})

	// A reviewer that read part of an update must not be able to clear the rest of it.
	It("an update too large to read in full reaches no reviewer, and asks", func() {
		s := valid
		s.truncated = true
		w := run(s)
		Expect(w.reviewer.Calls()).To(BeZero())
		Expect(w.states()).To(HaveExactElements("pending", "failure"))
		Expect(w.asked()).To(ConsistOf(ContainSubstring("too large")))
	})

	// A file named and not shown is a file nobody reviewed, however it came to be binary.
	It("a file no reviewer can read reaches no reviewer, and asks", func() {
		s := valid
		s.unshown = "index.js"
		w := given(s, securityPull())
		outcome := w.run()
		Expect(w.reviewer.Calls()).To(BeZero())
		Expect(w.states()).To(HaveExactElements("pending", "failure"))
		Expect(w.asked()).To(ConsistOf(ContainSubstring("no reviewer can read")))
		Expect(planned(outcome.Effects)).To(ContainSubstring(`"body":{"$untrusted":"index.js"}`))
	})

	It("every package's unshown files are kept", func() {
		s := valid
		s.unshown, s.group = "a.bin", "b.bin"
		w := given(s, securityPull())
		outcome := w.run()
		Expect(w.walked.last()).To(Equal("checks/hidden"))
		Expect(planned(outcome.Effects)).To(ContainSubstring(`"body":{"$untrusted":"a.bin\nb.bin"}`))
	})
}
