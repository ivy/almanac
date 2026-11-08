package buildfailures_test

import (
	"encoding/json"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/buildfailures"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/engine"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/workflow"
)

// A scenario is a world and the event that reaches the workflow in it.
type scenario struct {
	w        *world
	event    json.RawMessage
	attempts workflow.Attempts
}

// failedBuild is a pull request whose build failed on its head, with the release cooled and no
// attempts spent, and a fixer that answers reply.
func failedBuild(reply string, edits ...pullEdit) scenario {
	w := newWorld(reply, edits...).released(cooledAge)
	w.sim.AddSuite(repoName, simforge.FinishedSuite(w.head(), branch, "failure", buildLog))
	return scenario{w: w, event: w.event(true, false)}
}

// greenRepair is a repair Almanac pushed whose build passed, held pending if held.
func greenRepair(held bool) scenario {
	w := newWorld(fixedMain).released(cooledAge)
	repaired := strings.Repeat("c", 40)
	w.sim.MoveBranch(repoName, w.number, repaired, "fix(deps): repair the build after this update\n\nAlmanac-Key: k")
	w.sim.AddSuite(repoName, simforge.FinishedSuite(repaired, branch, "success", ""))
	if held {
		w.status(repaired, "pending")
	}
	return scenario{w: w, event: w.event(false, true)}
}

// event is a check suite finishing on the head, as a webhook or a scan delivers it.
func (w *world) event(failed, patched bool) json.RawMessage {
	return renovate.SuitePayload(hostile.MustParseSlug(repoName), hostile.MustParseSlug(branch), hostile.MustParseSha(w.head()), failed, patched)
}

// spent is s with the attempts spent, and whether a person was asked.
func (s scenario) spent(asked bool) scenario {
	s.attempts = workflow.Attempts{Used: renovate.AttemptCap, Asked: asked}
	return s
}

// The effects each ending makes, as the outbox stores them.
func failTrust(sha string) string {
	return `{"Attest": {"context": "almanac/trust", "state": "Failure", "repo": "` + repoName + `", "sha": "` + sha + `"}}`
}

func passTrust(sha string) string {
	return `{"Attest": {"context": "almanac/trust", "state": "Success", "repo": "` + repoName + `", "sha": "` + sha + `"}}`
}

func ask(number int, question, body string) string {
	b, _ := json.Marshal(body)
	return `{"Ask": {"question": "` + question + `", "target": "` + repoName + `", "pull": ` + strconv.Itoa(number) + `, "body": {"$untrusted": ` + string(b) + `}}}`
}

func await(until string) string { return `{"Await": {"until": "` + until + `"}}` }

func push(number int, base, reply string) string {
	return `{"Push": {"repo": "` + repoName + `", "pull": ` + strconv.Itoa(number) + `, "branch": "` + branch + `",
		"message": "fix(deps): repair the build after this update", "hold": "almanac/trust", "base": "` + base + `",
		"patch": {"$untrusted": ` + reply + `}}}`
}

// An ending is a scenario that ends a run at step and rule, making effects.
type ending struct {
	name       string
	step, rule string
	arrange    func() scenario
	effects    func(w *world) string
}

var endings = []ending{
	{"a pull request in conflict, which is Renovate's to rebase", "actionable", "nothing",
		func() scenario { return failedBuild(fixedMain, inConflict) },
		func(*world) string { return `[]` }},
	{"green after a repair Almanac pushed and holds", "actionable", "passed",
		func() scenario { return greenRepair(true) },
		func(w *world) string { return `[` + passTrust(w.head()) + `]` }},
	{"green, and no hold of Almanac's to clear", "actionable", "green",
		func() scenario { return greenRepair(false) },
		func(*world) string { return `[]` }},
	{"out of attempts, and a person was asked", "attempts", "asked",
		func() scenario { return failedBuild(fixedMain).spent(true) },
		func(*world) string { return `[]` }},
	{"out of attempts", "attempts", "exhausted",
		func() scenario { return failedBuild(fixedMain).spent(false) },
		func(w *world) string {
			return `[` + failTrust(w.head()) + `, ` + ask(w.number, "Repairs did not fix this build", "") + `]`
		}},
	{"nothing dates the release", "cooled", "undated",
		undatedBuild,
		func(w *world) string {
			return `[` + ask(w.number, "This update's build failed, and nothing says when it was published",
				"No agent reads an update before its cooldown passes, and this one has no publish time to measure from. It needs a person.") + `]`
		}},
	{"the release is younger than the cooldown", "cooled", "cooling",
		youngBuild,
		func(*world) string { return `[` + await("the update's cooldown to pass") + `]` }},
	{"a repair with no change", "decide", "empty",
		func() scenario { return failedBuild(changesNothing) },
		func(w *world) string {
			return `[` + failTrust(w.head()) + `, ` + ask(w.number, "A repair ended with no change to push", "This needs a CI change.") + `]`
		}},
	{"a repair the patch policy refuses", "decide", "refused",
		func() scenario { return failedBuild(touchesCI) },
		func(w *world) string {
			return `[` + failTrust(w.head()) + `, ` + ask(w.number, "A repair was refused by the patch policy", "the diff") + `]`
		}},
	{"a repair that takes back the update", "decide", "undid",
		func() scenario { return failedBuild(undoesUpdate, updating("package.json", manifestDiff)) },
		func(w *world) string {
			return `[` + failTrust(w.head()) + `, ` + ask(w.number, "A repair took back the update", "the diff") + `]`
		}},
	{"a repair the policy accepts, which keeps the update", "decide", "push",
		func() scenario { return failedBuild(fixedMain) },
		func(w *world) string { return `[` + push(w.number, w.head(), fixedMain) + `]` }},
}

// entries are the endings as table entries.
func entries() []TableEntry {
	made := make([]TableEntry, len(endings))
	for i, e := range endings {
		made[i] = Entry(e.name, e)
	}
	return made
}

// undatedBuild is failedBuild with nothing in the registry to date the release.
func undatedBuild() scenario {
	w := newWorld(fixedMain)
	w.sim.AddSuite(repoName, simforge.FinishedSuite(w.head(), branch, "failure", buildLog))
	return scenario{w: w, event: w.event(true, false)}
}

// youngBuild is failedBuild with a release younger than the cooldown.
func youngBuild() scenario {
	w := newWorld(fixedMain).released(youngAge)
	w.sim.AddSuite(repoName, simforge.FinishedSuite(w.head(), branch, "failure", buildLog))
	return scenario{w: w, event: w.event(true, false)}
}

// walk runs the workflow over a scenario outside a runtime, and says where it ended.
func walk(s scenario) (engine.Ended, *walking) {
	GinkgoHelper()
	bf, err := buildfailures.New()
	Expect(err).NotTo(HaveOccurred())
	cx := &walking{set: s.w.set, clock: s.w.Clock, attempts: s.attempts}
	ended, err := bf.Walk(s.w.ctx, cx, s.event)
	Expect(err).NotTo(HaveOccurred())
	return ended, cx
}

var _ = Describe("Every way build_failures can end", func() {
	It("has a scenario for every ending of the model, and only those", func() {
		var model, table []string
		for _, e := range buildfailures.Model.Endings() {
			model = append(model, e.Step+"/"+e.Rule)
		}
		for _, e := range endings {
			table = append(table, e.step+"/"+e.rule)
		}
		Expect(model).To(HaveLen(11))
		Expect(table).To(ConsistOf(model))
	})

	DescribeTable("ends a run from what GitHub, the registry and the fixer say",
		func(e ending) {
			s := e.arrange()
			ended, cx := walk(s)
			Expect(ended.Step + "/" + ended.Rule).To(Equal(e.step + "/" + e.rule))
			Expect(planned(ended.Decided.Effects)).To(MatchJSON(e.effects(s.w)))

			By("holding the pull request, and calling the fixer, only once the release has cooled")
			if e.step == "decide" {
				Expect(cx.holds).To(Equal([]string{repoName + "@" + s.w.head() + ":" + renovate.TrustContext}))
				Expect(cx.calls).To(Equal([]string{"fixer"}))
			} else {
				Expect(cx.holds).To(BeEmpty())
				Expect(cx.calls).To(BeEmpty())
			}

			By("naming the pull request in every question it asks")
			for _, made := range ended.Decided.Effects {
				if question, ok := made.(effect.Ask); ok {
					Expect(question.Pull).To(Equal(uint64(s.w.number)))
				}
			}
		},
		entries(),
	)

	It("awaits a person instead of asking twice about a release nothing dates", func() {
		s := undatedBuild()
		s.attempts = workflow.Attempts{Used: 1, Asked: true}
		ended, _ := walk(s)
		Expect(ended.Rule).To(Equal("undated"))
		Expect(planned(ended.Decided.Effects)).To(MatchJSON(`[` + await("a person to look at this update") + `]`))
	})
})
