package securityfixes_test

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate/securityfixes"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/simforge"
)

// ending is a world whose run ends at step/rule, posting states and asking question, if any.
type ending struct {
	name     string
	s        scenario
	pull     func() simforge.Pull
	end      string
	states   []string
	question string
}

// with is valid changed by edit.
func with(edit func(*scenario)) scenario {
	s := valid
	edit(&s)
	return s
}

var endings = []ending{
	{"a pull request someone other than Renovate opened",
		valid, func() simforge.Pull {
			p := securityPull()
			p.Author = "someone"
			return p
		},
		"found/gone", []string{"pending"}, ""},
	{"an update no advisory names as its fix",
		with(func(s *scenario) { s.namesTheFix = false }), nil,
		"checks/not_the_fix", []string{"pending", "failure"}, "This update is not the fix an advisory names"},
	{"a release that is not an archive",
		with(func(s *scenario) { s.readable = false }), nil,
		"checks/unreadable", []string{"pending", "failure"}, "This security update's package could not be read"},
	{"a release that makes a file binary",
		with(func(s *scenario) { s.unshown = "index.js" }), nil,
		"checks/hidden", []string{"pending", "failure"}, "This security update changes files no reviewer can read"},
	{"a release whose diff is too large to show in full",
		with(func(s *scenario) { s.truncated = true }), nil,
		"checks/truncated", []string{"pending", "failure"}, "This security update is too large for its reviewer to read in full"},
	{"a release that adds an install script",
		with(func(s *scenario) { s.installScript = true }), nil,
		"checks/install_script", []string{"pending", "failure"}, "This security update adds an install script"},
	{"a release two hours old",
		with(func(s *scenario) { s.hoursOld = 2 }), nil,
		"checks/too_young", []string{"pending"}, "This fix is younger than the cooldown"},
	{"a reviewer with concerns",
		with(func(s *scenario) { s.verdict = "Concerns" }), nil,
		"verdict/concerns", []string{"pending", "failure"}, "A reviewer raised concerns about this security update"},
	{"no concerns, and CI passed",
		valid, nil,
		"verdict/tested", []string{"pending", "success"}, ""},
	{"no concerns, in a repository with no workflows",
		with(func(s *scenario) { s.tested = false }), nil,
		"verdict/untestable", []string{"pending"}, "Nothing tests this repository, so this security update cannot merge"},
	{"no concerns, five minutes after it opened, in a repository with workflows",
		with(func(s *scenario) { s.tested, s.workflows, s.at = false, true, opened.Add(5*time.Minute) }), nil,
		"verdict/waiting", []string{"pending"}, ""},
}

var _ = Describe("Every way security_fixes can end, through the runtime", func() {
	It("has a world for every ending of the model, and only those", func() {
		var model, ours []string
		for _, e := range securityfixes.Model.Endings() {
			model = append(model, e.Step+"/"+e.Rule)
		}
		for _, e := range endings {
			ours = append(ours, e.end)
		}
		Expect(ours).To(ConsistOf(model))
	})

	entries := make([]TableEntry, len(endings))
	for i, e := range endings {
		entries[i] = Entry(e.name, e)
	}

	DescribeTable("ends where its world leads",
		func(sc SpecContext, e ending) {
			ctx := context.WithoutCancel(sc)
			p := securityPull()
			if e.pull != nil {
				p = e.pull()
			}
			w := newWorld(ctx, onMemory(ctx), e.s, p)
			outcome := w.run()
			Expect(outcome.Result).To(Equal(runtime.Ran), outcome.Reason)
			Expect(w.walked.last()).To(Equal(e.end))
			Expect(w.states()).To(Equal(e.states), "pending first, then the verdict")
			if e.question == "" {
				Expect(w.asked()).To(BeEmpty())
			} else {
				Expect(w.asked()).To(ConsistOf(e.question))
			}

			By("asking the reviewer only once every check passed")
			if strings.HasPrefix(e.end, "verdict/") {
				Expect(w.reviewer.Calls()).To(Equal(1))
			} else {
				Expect(w.reviewer.Calls()).To(BeZero())
			}
		},
		entries,
	)
})
