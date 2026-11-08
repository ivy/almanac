package dsl_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Each entry is a Rust compile-fail case, named as its file is, and the Go that case becomes
// in a workflow or its vocabulary. The Go compiler refuses each one, so `make go-lint` does.
// docs/safety-rules.md says what every case became.
var _ = Describe("The Go compiler, for the Rust compile-fail cases", func() {
	DescribeTable("refuses, when the package is built,",
		func(body, because string) {
			errs := typeErrors(body)
			Expect(errs).NotTo(BeEmpty(), "it compiled")
			Expect(strings.Join(errs, "\n")).To(ContainSubstring(because))
		},
		Entry("call_model_without_cx: a pass to a harness made outside package agent",
			`_ = agent.Pass{issued: true}`,
			"issued"),
		Entry("checked_cannot_be_forged: a checked patch made outside package patch",
			`_ = patch.Checked{patch: hostile.New(patch.Patch{})}`,
			"patch"),
		Entry("cooled_cannot_be_forged: a cooldown proof made outside package cooled",
			`_ = cooled.Cooled{proven: true}`,
			"proven"),
		Entry("effect_has_no_merge: a merge effect",
			`_ = effect.Merge{}`,
			"undefined: effect.Merge"),
		Entry("unknown_effect: may with a kind there is no effect of",
			`_ = May(Merges)`,
			"undefined: Merges"),
		Entry("file_title_is_a_string: an issue whose title is a string",
			`_ = effect.File{Title: "x"}`,
			"as hostile.Title value"),
		Entry("file_title_is_a_string: a title format that is not a constant",
			`format := "Build failed"; _ = File(Fact[hostile.Slug]("repo"), Title(format))`,
			"cannot use format"),
		Entry("hostile_effect_title: a status context taken from a hostile fact",
			`_ = Attest(Hostile[string]("title"), Pending, Fact[hostile.Slug]("repo"), Fact[hostile.Sha]("sha"))`,
			"cannot use Hostile[string]"),
		Entry("key_on_untrusted_text: a key made of a hostile fact",
			`_ = Key(Hostile[hostile.Slug]("repo"))`,
			"does not implement dsl.Clean"),
		Entry("manual_clean_impl: a type declaring itself clean",
			`var _ hostile.Clean = 1`,
			"cannot use type hostile.Clean outside a type constraint"),
		Entry("narrow_to_number: narrowing to a number",
			`_ = hostile.Classify(hostile.New("x"), func(t string) uint64 { return uint64(len(t)) })`,
			"C (type uint64) does not satisfy hostile.Finite"),
		Entry("narrow_to_slug: narrowing to validated text",
			`_ = hostile.Classify(hostile.New("x"), func(t string) hostile.Slug { return hostile.MustParseSlug(t) })`,
			"C (type hostile.Slug) does not satisfy hostile.Finite"),
		Entry("narrow_to_string: narrowing to the text itself",
			`_ = hostile.Narrow(hostile.New("x"), func(t string) string { return t })`,
			"does not match"),
		Entry("narrow_to_string: classifying into the text itself",
			`_ = hostile.Classify(hostile.New("x"), func(t string) string { return t })`,
			"C (type string) does not satisfy hostile.Finite"),
		Entry("payload_field_rejects_string: a payload field read as a plain string",
			`var p *wire.Payload; _ = wire.Field[string](p, "/title")`,
			"string does not satisfy hostile.Clean"),
		Entry("payload_text_is_tainted: payload text taken as a plain string",
			`var p *wire.Payload; var title string = p.Text("/title"); _ = title`,
			"cannot use p.Text"),
		Entry("policy_cannot_take_cx: a predicate that takes the run's context",
			`_ = vocab.Predicate(renovate.Vocabulary, "demo.sneaky", "sneaky", func(cx workflow.Cx, p renovate.Pull) bool { return cx != nil })`,
			"does not match"),
		Entry("read_private_field: reading inside a hostile value",
			`_ = hostile.New("x").box`,
			"unexported field box"),
		Entry("reader_cannot_act: a quarantined agent acting as a worker",
			`_ = Act("act", "Act", Fact[renovate.Review]("review"), renovate.Reviewer)`,
			"does not match inferred type vocab.WorkerPart"),
		Entry("reader_cannot_act: a quarantined agent's Act",
			`var p agent.Prompt; _, _ = agent.NewQuarantined("reader", p).Act(nil, true)`,
			"has no field or method Act"),
		Entry("fixer_needs_cooled: a fixer's call with no cooldown proof",
			`var p agent.Prompt; _, _ = agent.NewFixer("fixer", p).Repair(agent.Vars{}, nil)`,
			"not enough arguments"),
		Entry("prompt_rejects_hostile_text: a prompt variable filled from a hostile fact",
			`_ = Var("repo", Hostile[hostile.Slug]("repo"))`,
			"does not match inferred type dsl.CleanFact"),
		Entry("subject_on_untrusted_text: a subject made of a hostile fact",
			`_ = RepoSubject(Hostile[hostile.Slug]("repo"))`,
			"cannot use Hostile[hostile.Slug]"),
		Entry("title_from_string: a title filled with a string",
			`_ = Ask(Fact[hostile.Slug]("repo"), Title("%s failed"), "x")`,
			`cannot use "x"`),
		Entry("title_from_string: a title whose format is a variable",
			`name := "%s"; _ = hostile.Titlef(name)`,
			"cannot use name"),
		Entry("title_from_untrusted: a title filled with a hostile fact",
			`_ = Ask(Fact[hostile.Slug]("repo"), Title("%s failed"), Hostile[string]("logs"))`,
			"does not implement dsl.Clean"),
		Entry("unknown_option: a workflow option there is no part for",
			`_ = Define("w", Retries(3))`,
			"undefined: Retries"),
		Entry("untrusted_patch_to_push: a push of a patch no policy checked",
			`_ = Push(Hostile[patch.Patch]("patch"), Fact[hostile.Slug]("repo"), Fact[uint64]("n"), Fact[hostile.Slug]("branch"), "fix")`,
			"cannot use Hostile[patch.Patch]"),
		Entry("untrusted_patch_to_push: an effect pushing a hostile patch",
			`_ = effect.Push{Patch: hostile.New(patch.Patch{})}`,
			"cannot use hostile.New"),
		Entry("wrong_event_type: a step reading another event's type",
			`_ = Observe("pull", "Read", Fact[renovate.Pull]("pull"), renovate.PullForBranch, EventOf(renovate.SecurityPull))`,
			"does not match"),
		Entry("wrong_prompt_for_agent: a call that chooses its prompt",
			`var p agent.Prompt; _, _ = agent.NewQuarantined("reader", p).Read(p, agent.Vars{}, true)`,
			"too many arguments"),
	)

	It("refuses an effect declared outside package effect (effect_has_no_merge)", func() {
		errs := sourceErrors(`package probe

import "github.com/ivy/almanac/effect"

type Merge struct{}

func (Merge) Kind() effect.Kind { return effect.KindPush }

var _ effect.Effect = Merge{}
`)
		Expect(strings.Join(errs, "\n")).To(ContainSubstring("Merge does not implement effect.Effect (missing method"))
	})

	It("refuses the seal Rust left public (hole_private_seal), because it is an internal package", func() {
		errs := sourceErrors(`package probe

import "github.com/ivy/almanac/hostile/internal/seal"

var _ = seal.Open
`)
		Expect(strings.Join(errs, "\n")).To(ContainSubstring("use of internal package"))
	})

	DescribeTable("accepts the Rust pass cases",
		func(body string) { Expect(typeErrors(body)).To(BeEmpty()) },
		Entry("ok_narrow_to_bool",
			`_ = hostile.Narrow(hostile.New("x"), func(t string) bool { return len(t) > 3 })`),
		Entry("ok_checked_patch_to_push",
			`_ = Push(Checked, Fact[hostile.Slug]("repo"), Fact[uint64]("n"), Fact[hostile.Slug]("branch"), "fix")`),
		Entry("ok_fixer_with_cooled",
			`var p agent.Prompt; var proof cooled.Cooled; _, _ = agent.NewFixer("fixer", p).Repair(agent.Vars{}, nil, proof)`),
		Entry("effect_is_closed",
			`var e effect.Effect = effect.Await{}; switch e.(type) { case effect.Attest, effect.Await, effect.File, effect.Ask, effect.Push: }`),
		Entry("ok_workflow_macro",
			`_ = Define("w", Intent("x"), On(renovate.BuildChanged), Key(Field[hostile.Slug](EventOf(renovate.BuildChanged), "repo")), May(Attests), Limits(PerHour(1)), Decide("d", "D", Otherwise("o", "O")))`),
	)
})
