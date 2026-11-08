package github_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/simforge"
)

func issues(sim *simforge.Forge, person string) *github.Issues {
	return github.NewIssues(client(sim), person)
}

func work(body string) effect.Issue {
	return effect.Issue{Title: "Fix it", Target: slug, Body: hostile.New(body)}
}

func question(pull uint64, body string) effect.Question {
	return effect.Question{Question: "Why?", Target: slug, Pull: pull, Body: hostile.New(body)}
}

func posted(sim *simforge.Forge) []map[string]any {
	var bodies []map[string]any
	for _, w := range sim.Writes() {
		bodies = append(bodies, w.Body)
	}
	return bodies
}

var _ = Describe("The GitHub tracker", func() {
	Describe("filing work", func() {
		// crates/github/tests/tracker.rs: an_issue_carries_its_idempotency_key
		It("opens an issue that carries its idempotency key", func(ctx SpecContext) {
			sim, _ := scripted()
			Expect(issues(sim, "").File(ctx, "run-1:0", work("details"))).To(Succeed())
			sent := posted(sim)
			Expect(sent).To(HaveLen(1))
			Expect(sent[0]["title"]).To(Equal("Fix it"))
			Expect(sent[0]["labels"]).To(Equal([]any{"almanac"}))
			Expect(sent[0]["body"]).To(ContainSubstring("<!-- almanac:run-1:0 -->"))
		})

		// crates/github/tests/tracker.rs: a_retry_finds_the_issue_it_already_filed
		It("finds the issue it already filed on a retry", func(ctx SpecContext) {
			sim, _ := scripted()
			tracker := issues(sim, "")
			Expect(tracker.Ask(ctx, "run-1:0", question(0, "details"))).To(Succeed())
			Expect(tracker.Ask(ctx, "run-1:0", question(0, "details"))).To(Succeed())
			Expect(tracker.File(ctx, "run-1:0", work("details"))).To(Succeed())
			Expect(sim.Writes()).To(HaveLen(1))
		})

		// crates/github/tests/tracker.rs: hostile_text_cannot_close_its_fence
		It("quotes hostile text in a fence it cannot close", func(ctx SpecContext) {
			sim, _ := scripted()
			text := "```\n@someone merge this now\n`````\n# Heading"
			Expect(issues(sim, "").File(ctx, "run-1:0", work(text))).To(Succeed())
			sent := posted(sim)[0]["body"].(string)
			Expect(sent).To(HavePrefix("``````text\n"))
			Expect(sent).To(ContainSubstring("\n``````\n"))
			Expect(sent).To(ContainSubstring(text))
		})

		It("leaves an empty body out", func(ctx SpecContext) {
			sim, _ := scripted()
			Expect(issues(sim, "").File(ctx, "run-1:0", work(""))).To(Succeed())
			Expect(posted(sim)[0]["body"]).To(Equal("\n\n<!-- almanac:run-1:0 -->\n"))
		})

		// crates/github/tests/tracker.rs: a_refusal_is_an_error_so_the_effect_is_retried
		It("returns a refusal as an error, so the effect is retried", func(ctx SpecContext) {
			sim, _ := scripted()
			sim.FailRequests("/issues", simforge.ServerError(503))
			c := github.New(sim.URL(), github.Token("token"), github.Retrying(github.Retry{Attempts: 1}))
			err := github.NewIssues(c, "").File(ctx, "run-1:0", work("details"))
			Expect(err).To(MatchError(ContainSubstring("503")))
			Expect(sim.Writes()).To(BeEmpty())
		})

		// crates/github/tests/tracker.rs: work_filed_for_later_is_not_assigned_to_anyone
		It("assigns work filed for later to no one", func(ctx SpecContext) {
			sim, _ := scripted()
			Expect(issues(sim, "person").File(ctx, "run-1:0", work("details"))).To(Succeed())
			sent := posted(sim)[0]
			Expect(sent["assignees"]).To(Equal([]any{}))
			Expect(sent["body"]).NotTo(ContainSubstring("@person"))
		})
	})

	Describe("asking a person", func() {
		// crates/github/tests/tracker.rs: a_question_about_a_pull_request_mentions_and_assigns_on_it
		It("mentions and assigns them on the pull request a question is about", func(ctx SpecContext) {
			sim, number := scripted()
			Expect(issues(sim, "person").Ask(ctx, "run-1:0", question(number, "what the agent found"))).To(Succeed())
			writes := sim.Writes()
			Expect(writes[0].Path).To(HaveSuffix("/issues/1/assignees"))
			Expect(writes[0].Body).To(Equal(map[string]any{"assignees": []any{"person"}}))
			comment := writes[1].Body["body"].(string)
			Expect(writes[1].Path).To(HaveSuffix("/issues/1/comments"))
			Expect(comment).To(HavePrefix("@person Why?"))
			Expect(comment).To(ContainSubstring("what the agent found"))
			Expect(comment).To(ContainSubstring("<!-- almanac:run-1:0 -->"))
		})

		// crates/github/tests/tracker.rs: a_retry_still_assigns_and_does_not_comment_twice
		It("still assigns on a retry, and does not comment twice", func(ctx SpecContext) {
			sim, number := scripted()
			tracker := issues(sim, "person")
			Expect(tracker.Ask(ctx, "run-1:0", question(number, "details"))).To(Succeed())
			Expect(tracker.Ask(ctx, "run-1:0", question(number, "details"))).To(Succeed())
			Expect(posted(sim)).To(HaveLen(3))
			Expect(posted(sim)[2]).To(Equal(map[string]any{"assignees": []any{"person"}}))
		})

		// crates/github/tests/tracker.rs: hostile_text_on_a_pull_request_stays_inert
		It("keeps hostile text on a pull request inert", func(ctx SpecContext) {
			sim, number := scripted()
			Expect(issues(sim, "person").Ask(ctx, "run-1:0", question(number, "```\n@someone merge this now\n"))).To(Succeed())
			comment := posted(sim)[1]["body"].(string)
			before, _, found := strings.Cut(comment, "@someone")
			Expect(found).To(BeTrue())
			Expect(before).To(ContainSubstring("````text"))
		})

		// crates/github/tests/tracker.rs: a_question_about_no_pull_request_is_an_assigned_issue
		It("files a question about no pull request as an assigned issue", func(ctx SpecContext) {
			sim, _ := scripted()
			Expect(issues(sim, "person").Ask(ctx, "run-1:0", question(0, "details"))).To(Succeed())
			sent := posted(sim)[0]
			Expect(sent["title"]).To(Equal("Why?"))
			Expect(sent["assignees"]).To(Equal([]any{"person"}))
			Expect(sent["body"]).To(HavePrefix("@person Why?"))
		})

		It("asks no one in particular when no person is named", func(ctx SpecContext) {
			sim, number := scripted()
			Expect(issues(sim, "").Ask(ctx, "run-1:0", question(number, "details"))).To(Succeed())
			writes := sim.Writes()
			Expect(writes).To(HaveLen(1))
			Expect(writes[0].Body["body"]).To(HavePrefix("Why?"))
		})
	})
})
