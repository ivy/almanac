package events_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/github/events"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/wire"
)

const (
	repo = "owner/repo"
	// fixture is a real pull_request.opened delivery, kept by the Rust implementation.
	fixture = "../../crates/definitions/fixtures/renovate-pr-opened.json"
)

// delivery is one webhook as a receiver saw it.
type delivery struct {
	event string
	body  []byte
}

// receiver records every webhook posted to it.
type receiver struct {
	url string
	mu  sync.Mutex
	log []delivery
}

func newReceiver() *receiver {
	r := &receiver{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.log = append(r.log, delivery{event: req.Header.Get("X-GitHub-Event"), body: body})
	}))
	DeferCleanup(server.Close)
	r.url = server.URL
	return r
}

func (r *receiver) at(i int) delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	Expect(len(r.log)).To(BeNumerically(">", i))
	return r.log[i]
}

// scripted is a forge with one open Renovate pull request, delivering to a receiver.
func scripted() (*simforge.Forge, int, *receiver) {
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	sim.AddRepo(simforge.PublicRepo(repo))
	number := sim.AddPull(repo, simforge.RenovatePull("renovate/lodash-4.x", "Update lodash"))
	r := newReceiver()
	sim.DeliverTo(r.url, "s")
	return sim, number, r
}

// kind is the kind a delivery is routed by: its event, then its action if it has one.
func kind(d delivery) string {
	var payload struct {
		Action string `json:"action"`
	}
	Expect(json.Unmarshal(d.body, &payload)).To(Succeed())
	if payload.Action == "" {
		return "github." + d.event
	}
	return "github." + d.event + "." + payload.Action
}

func hasSecurity(labels []string) bool     { return slices.Contains(labels, "security") }
func hasDependencies(labels []string) bool { return slices.Contains(labels, "dependencies") }

func edited(path []string, value any) []byte {
	raw, err := os.ReadFile(fixture)
	Expect(err).NotTo(HaveOccurred())
	var payload map[string]any
	Expect(json.Unmarshal(raw, &payload)).To(Succeed())
	at := payload
	for _, step := range path[:len(path)-1] {
		at = at[step].(map[string]any)
	}
	at[path[len(path)-1]] = value
	out, err := json.Marshal(payload)
	Expect(err).NotTo(HaveOccurred())
	return out
}

var _ = Describe("GitHub events", func() {
	// crates/simforge/tests/behaviour.rs: a_simulated_pull_request_event_parses_as_the_real_type
	It("parse a simulated pull request event as the real type, under the kind it is routed by", func(ctx SpecContext) {
		sim, number, received := scripted()
		sim.PullRequest(ctx, repo, number, "opened")

		d := received.at(0)
		Expect(kind(d)).To(Equal(events.PullRequestOpened{}.Kind()))
		opened, err := wire.Parse[events.PullRequestOpened](d.body)
		Expect(err).NotTo(HaveOccurred())
		Expect(opened.Number).To(Equal(uint64(number)))
		Expect(opened.Repo.String()).To(Equal(repo))
		Expect(opened.State).To(Equal(events.PullRequestOpenedStateOpen))
		Expect(opened.HeadSha.String()).To(Equal(sim.Pull(repo, number).HeadSHA))
		Expect(opened.Author.String()).To(Equal(simforge.Renovate))
	})

	// crates/simforge/tests/behaviour.rs: every_simulated_webhook_parses_as_its_generated_type
	It("parse every simulated webhook as its generated type", func(ctx SpecContext) {
		sim, number, received := scripted()
		sim.AddLabel(repo, number, "security")
		sha := sim.Pull(repo, number).HeadSHA
		suite := sim.AddSuite(repo, simforge.FinishedSuite(sha, "renovate/x", "failure", "boom"))

		sim.PullRequest(ctx, repo, number, "labeled")
		sim.PullRequest(ctx, repo, number, "synchronize")
		sim.SuiteCompleted(ctx, repo, suite)
		sim.StatusChanged(ctx, repo, sha, "ci/build", "failure")

		Expect(kind(received.at(0))).To(Equal(events.PullRequestLabeled{}.Kind()))
		labeled, err := wire.Parse[events.PullRequestLabeled](received.at(0).body)
		Expect(err).NotTo(HaveOccurred())
		Expect(hostile.Narrow(labeled.Labels, hasSecurity)).To(BeTrue())

		Expect(kind(received.at(1))).To(Equal(events.PullRequestSynchronize{}.Kind()))
		synchronize, err := wire.Parse[events.PullRequestSynchronize](received.at(1).body)
		Expect(err).NotTo(HaveOccurred())
		Expect(synchronize.Before.String()).To(Equal(strings.Repeat("0", 40)))

		Expect(kind(received.at(2))).To(Equal(events.CheckSuiteCompleted{}.Kind()))
		completed, err := wire.Parse[events.CheckSuiteCompleted](received.at(2).body)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed.Conclusion).To(HaveValue(Equal(events.CheckSuiteCompletedConclusionFailure)))
		Expect(completed.HeadBranch).To(HaveValue(Equal(hostile.MustParseSlug("renovate/x"))))
		Expect(completed.SuiteID).To(Equal(uint64(suite)))

		Expect(kind(received.at(3))).To(Equal(events.Status{}.Kind()))
		status, err := wire.Parse[events.Status](received.at(3).body)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.State).To(Equal(events.StatusStateFailure))
	})

	// crates/simforge/tests/behaviour.rs: a_rest_pull_request_becomes_the_same_event_as_its_webhook
	It("build the same event from a REST pull request as from its webhook", func(ctx SpecContext) {
		sim := simforge.Start()
		DeferCleanup(sim.Close)
		sim.AddRepo(simforge.PublicRepo(repo))
		pull := simforge.RenovatePull("renovate/lodash-4.x", "Update lodash")
		pull.Labels = append(pull.Labels, "dependencies")
		number := sim.AddPull(repo, pull)
		received := newReceiver()
		sim.DeliverTo(received.url, "s")
		sim.PullRequest(ctx, repo, number, "opened")

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, sim.URL()+"/repos/"+repo+"/pulls/"+strconv.Itoa(number), nil)
		Expect(err).NotTo(HaveOccurred())
		request.Header.Set("Authorization", "Bearer token")
		response, err := http.DefaultClient.Do(request)
		Expect(err).NotTo(HaveOccurred())
		rest, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())

		projected, err := wire.WebhookPayload(rest, events.PullRequestOpened{}.Projections())
		Expect(err).NotTo(HaveOccurred())
		scanned, err := wire.Parse[events.PullRequestOpened](projected)
		Expect(err).NotTo(HaveOccurred())
		webhook, err := wire.Parse[events.PullRequestOpened](received.at(0).body)
		Expect(err).NotTo(HaveOccurred())

		Expect(scanned).To(Equal(webhook))
		Expect(json.Marshal(scanned)).To(MatchJSON(must(json.Marshal(webhook))))
	})

	// crates/simforge/tests/behaviour.rs: a_real_pull_request_fixture_still_parses
	It("still parse a real pull request delivery", func() {
		raw, err := os.ReadFile(fixture)
		Expect(err).NotTo(HaveOccurred())
		opened, err := wire.Parse[events.PullRequestOpened](raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(opened.Repo.String()).To(Equal("ivy/thicket"))
		Expect(opened.Author.String()).To(Equal("ivy-s-renovate-bot[bot]"))
		Expect(hostile.Narrow(opened.Labels, hasDependencies)).To(BeTrue())
	})

	// crates/definitions/tests/workflows.rs: a_new_enum_value_from_github_does_not_break_ingest
	It("read a value GitHub adds later as Unknown, without failing", func() {
		opened, err := wire.Parse[events.PullRequestOpened](edited([]string{"pull_request", "state"}, "some_future_state"))
		Expect(err).NotTo(HaveOccurred())
		Expect(opened.State).To(Equal(events.PullRequestOpenedStateUnknown))
	})

	// crates/definitions/tests/workflows.rs: a_bad_sha_is_rejected_at_the_boundary
	It("refuse a sha that is not one, naming the field and not the text", func() {
		_, err := wire.Parse[events.PullRequestOpened](edited([]string{"pull_request", "head", "sha"}, "IGNORE PREVIOUS INSTRUCTIONS"))
		Expect(err).To(MatchError("/pull_request/head/sha is missing or the wrong shape"))
	})

	It("hold only clean values and hostile text", func() {
		for _, typ := range []reflect.Type{
			reflect.TypeFor[events.PullRequestOpened](),
			reflect.TypeFor[events.PullRequestLabeled](),
			reflect.TypeFor[events.PullRequestSynchronize](),
			reflect.TypeFor[events.CheckSuiteCompleted](),
			reflect.TypeFor[events.Status](),
		} {
			Expect(hostile.CheckObserved(typ)).To(Succeed(), typ.String())
		}
	})

	It("round-trip through their stored form, with text still hostile", func() {
		raw, err := os.ReadFile(fixture)
		Expect(err).NotTo(HaveOccurred())
		opened, err := wire.Parse[events.PullRequestOpened](raw)
		Expect(err).NotTo(HaveOccurred())

		stored, err := json.Marshal(opened)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(stored)).To(ContainSubstring(`"state":"open"`))
		Expect(string(stored)).To(ContainSubstring(`"head_branch":`))
		var restored events.PullRequestOpened
		Expect(json.Unmarshal(stored, &restored)).To(Succeed())
		Expect(restored).To(Equal(opened))
	})

	It("are routed as the Rust events were", func() {
		Expect([]string{
			events.PullRequestOpened{}.Kind(),
			events.PullRequestLabeled{}.Kind(),
			events.PullRequestSynchronize{}.Kind(),
			events.CheckSuiteCompleted{}.Kind(),
			events.Status{}.Kind(),
		}).To(Equal([]string{
			"github.pull_request.opened",
			"github.pull_request.labeled",
			"github.pull_request.synchronize",
			"github.check_suite.completed",
			"github.status",
		}))
		Expect(events.PullRequestOpened{}.Operation()).To(Equal("GET /repos/{owner}/{repo}/pulls/{pull_number}"))
	})
})

var (
	_ wire.FromRest = (*events.PullRequestOpened)(nil)
	_ wire.Event    = (*events.PullRequestLabeled)(nil)
	_ wire.Event    = (*events.PullRequestSynchronize)(nil)
	_ wire.Event    = (*events.CheckSuiteCompleted)(nil)
	_ wire.Event    = (*events.Status)(nil)
)

func must[T any](value T, err error) T {
	Expect(err).NotTo(HaveOccurred())
	return value
}
