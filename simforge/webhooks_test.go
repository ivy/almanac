package simforge_test

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/simforge"
)

var _ = Describe("Webhooks", func() {
	It("signs as GitHub's documented example does", func() {
		// https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries#testing-the-webhook-payload-validation
		Expect(simforge.Sign("It's a Secret to Everybody", []byte("Hello, World!"))).
			To(Equal("sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"))
	})

	It("signs a delivery over its exact body, so a broker's constant-time check accepts it", func(ctx SpecContext) {
		sim, number := scripted()
		hook := newReceiver()
		sim.DeliverTo(hook.url, "secret")

		guid := sim.PullRequest(ctx, repo, number, "opened")

		got := hook.all()
		Expect(got).To(HaveLen(1))
		Expect(got[0].event).To(Equal("pull_request"))
		Expect(got[0].guid).To(Equal(guid))
		Expect(verified([]byte("secret"), got[0].body, got[0].signature)).To(BeTrue())
		Expect(verified([]byte("another secret"), got[0].body, got[0].signature)).To(BeFalse())
		tampered := append([]byte(nil), got[0].body...)
		tampered[len(tampered)-2] ^= 1
		Expect(verified([]byte("secret"), tampered, got[0].signature)).To(BeFalse())
		Expect(verified([]byte("secret"), got[0].body, strings.TrimPrefix(got[0].signature, "sha256="))).To(BeFalse())
	})

	It("numbers delivery IDs as GUIDs", func(ctx SpecContext) {
		sim, number := scripted()
		Expect(sim.PullRequest(ctx, repo, number, "opened")).To(MatchRegexp(`^00000000-0000-4000-8000-\d{12}$`))
	})

	It("can lose, duplicate and reorder deliveries", func(ctx SpecContext) {
		sim, number := scripted()
		hook := newReceiver()
		sim.DeliverTo(hook.url, "s")
		sim.NextDeliveries(simforge.Lost, simforge.Duplicated, simforge.Held, simforge.Held)

		lost := sim.PullRequest(ctx, repo, number, "opened")
		duplicated := sim.PullRequest(ctx, repo, number, "synchronize")
		first := sim.PullRequest(ctx, repo, number, "edited")
		second := sim.PullRequest(ctx, repo, number, "labeled")
		Expect(hook.guids()).To(Equal([]string{duplicated, duplicated}))
		sim.ReleaseHeld(ctx)

		Expect(hook.guids()).NotTo(ContainElement(lost))
		Expect(hook.guids()).To(Equal([]string{duplicated, duplicated, second, first}))
	})

	It("lists a lost delivery as failed, and redelivers it with its ID", func(ctx SpecContext) {
		sim, number := scripted()
		hook := newReceiver()
		sim.DeliverTo(hook.url, "s")
		sim.NextDeliveries(simforge.Lost)
		guid := sim.PullRequest(ctx, repo, number, "opened")

		listed := getJSON(sim, "/app/hook/deliveries")
		Expect(at(listed, 0, "guid")).To(Equal(guid))
		Expect(at(listed, 0, "status_code")).To(BeEquivalentTo(502))
		Expect(at(listed, 0, "status")).To(Equal("failed"))
		Expect(at(listed, 0, "event")).To(Equal("pull_request"))
		Expect(at(listed, 0, "action")).To(Equal("opened"))
		Expect(at(listed, 0, "repository_id")).To(BeEquivalentTo(1))
		Expect(hook.all()).To(BeEmpty())

		id := at(listed, 0, "id")
		response, _ := call(http.MethodPost, fmt.Sprintf("%s/app/hook/deliveries/%v/attempts", sim.URL(), id), nil)
		Expect(response.StatusCode).To(Equal(http.StatusAccepted))
		Expect(hook.guids()).To(Equal([]string{guid}))

		listed = getJSON(sim, "/app/hook/deliveries")
		Expect(at(listed, 0, "guid")).To(Equal(guid))
		Expect(at(listed, 0, "redelivery")).To(BeTrue())
		Expect(at(listed, 0, "status")).To(Equal("OK"))
		Expect(at(listed, 0, "id")).NotTo(Equal(id))
	})

	It("refuses to redeliver a delivery it never made", func() {
		sim, _ := scripted()
		response, _ := call(http.MethodPost, sim.URL()+"/app/hook/deliveries/99/attempts", nil)
		Expect(response.StatusCode).To(Equal(http.StatusNotFound))
	})

	It("records the receiver's answer, and an event with nowhere to go as failed", func(ctx SpecContext) {
		sim, number := scripted()
		sim.PullRequest(ctx, repo, number, "opened")
		hook := newReceiver()
		hook.status = http.StatusServiceUnavailable
		sim.DeliverTo(hook.url, "s")
		sim.PullRequest(ctx, repo, number, "edited")

		listed := getJSON(sim, "/app/hook/deliveries")
		Expect(at(listed, 0, "status_code")).To(BeEquivalentTo(503))
		Expect(at(listed, 1, "status_code")).To(BeEquivalentTo(502))
		Expect(hook.all()).To(HaveLen(1))
	})

	It("pages the delivery log by cursor, newest first", func(ctx SpecContext) {
		sim, number := scripted()
		var guids []string
		for range 3 {
			guids = append(guids, sim.PullRequest(ctx, repo, number, "edited"))
		}
		response, body := call(http.MethodGet, sim.URL()+"/app/hook/deliveries?per_page=2", nil)
		Expect(at(decoded(body), 0, "guid")).To(Equal(guids[2]))
		next := nextLink(response)
		Expect(next).To(ContainSubstring("cursor=2"))
		_, body = call(http.MethodGet, next, nil)
		second := decoded(body)
		Expect(second).To(HaveLen(1))
		Expect(at(second, 0, "guid")).To(Equal(guids[0]))
	})

	Describe("payloads", func() {
		var (
			sim    *simforge.Forge
			number int
			hook   *receiver
		)

		BeforeEach(func() {
			sim, number = scripted()
			hook = newReceiver()
			sim.DeliverTo(hook.url, "s")
		})

		It("names the label a labeled event added", func(ctx SpecContext) {
			sim.AddLabel(repo, number, "security")
			sim.PullRequest(ctx, repo, number, "labeled")
			payload := hook.payload(0)
			Expect(at(payload, "action")).To(Equal("labeled"))
			Expect(at(payload, "label", "name")).To(Equal("security"))
			Expect(at(payload, "pull_request", "labels", 0, "name")).To(Equal("security"))
			Expect(at(payload, "sender", "login")).To(Equal(simforge.Renovate))
			Expect(at(payload, "sender", "type")).To(Equal("Bot"))
		})

		It("says where a synchronized branch moved from and to", func(ctx SpecContext) {
			first := sim.Pull(repo, number).HeadSHA
			sim.PullRequest(ctx, repo, number, "synchronize")
			Expect(at(hook.payload(0), "before")).To(Equal(strings.Repeat("0", 40)))
			Expect(at(hook.payload(0), "after")).To(Equal(first))

			moved := strings.Repeat("b", 40)
			sim.MoveBranch(repo, number, moved, "someone else's commit")
			sim.PullRequest(ctx, repo, number, "synchronize")
			Expect(at(hook.payload(1), "before")).To(Equal(first))
			Expect(at(hook.payload(1), "after")).To(Equal(moved))
			Expect(at(hook.payload(1), "pull_request", "head", "sha")).To(Equal(moved))
		})

		It("carries the pull request a REST read returns", func(ctx SpecContext) {
			sim.PullRequest(ctx, repo, number, "opened")
			rest := getJSON(sim, fmt.Sprintf("/repos/%s/pulls/%d", repo, number))
			Expect(at(hook.payload(0), "pull_request")).To(Equal(rest))
			Expect(at(hook.payload(0), "number")).To(BeEquivalentTo(number))
			Expect(at(hook.payload(0), "repository", "full_name")).To(Equal(repo))
		})

		It("reports a completed check suite", func(ctx SpecContext) {
			sha := sim.Pull(repo, number).HeadSHA
			suite := sim.AddSuite(repo, simforge.FinishedSuite(sha, "renovate/x", "failure", "boom"))
			sim.SuiteCompleted(ctx, repo, suite)
			got := hook.all()
			Expect(got[0].event).To(Equal("check_suite"))
			payload := hook.payload(0)
			Expect(at(payload, "action")).To(Equal("completed"))
			Expect(at(payload, "check_suite", "id")).To(BeEquivalentTo(suite))
			Expect(at(payload, "check_suite", "conclusion")).To(Equal("failure"))
			Expect(at(payload, "check_suite", "head_branch")).To(Equal("renovate/x"))
			Expect(at(payload, "check_suite", "head_sha")).To(Equal(sha))
			Expect(at(payload, "sender", "login")).To(Equal("github-actions[bot]"))
		})

		It("reports a commit status with the branches at that commit", func(ctx SpecContext) {
			sha := sim.Pull(repo, number).HeadSHA
			sim.StatusChanged(ctx, repo, sha, "ci/build", "failure")
			got := hook.all()
			Expect(got[0].event).To(Equal("status"))
			payload := hook.payload(0)
			Expect(at(payload, "state")).To(Equal("failure"))
			Expect(at(payload, "context")).To(Equal("ci/build"))
			Expect(at(payload, "sha")).To(Equal(sha))
			Expect(at(payload, "branches")).To(Equal([]any{map[string]any{"name": "renovate/lodash-4.x"}}))
			Expect(at(payload, "commit", "commit", "message")).To(Equal("Update"))
			Expect(payload).NotTo(HaveKey("action"))
		})

		It("lists a status event's action as null", func(ctx SpecContext) {
			sim.StatusChanged(ctx, repo, "abc", "ci", "success")
			Expect(at(getJSON(sim, "/app/hook/deliveries"), 0, "action")).To(BeNil())
		})
	})
})
