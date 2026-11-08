package definitions_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanactest/definitions"
	"github.com/ivy/almanac/engine"
	"github.com/ivy/almanac/github/events"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/workflow"
)

// receiver keeps the body of every webhook posted to it.
type receiver struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *receiver) ServeHTTP(_ http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, body)
}

func (r *receiver) first() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	Expect(r.bodies).NotTo(BeEmpty())
	return r.bodies[0]
}

// rest is the pull request as GitHub's REST API returns it, and its head.
func rest(ctx context.Context, sim *simforge.Forge, number int) ([]byte, hostile.Sha) {
	GinkgoHelper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sim.URL()+"/repos/"+repo+"/pulls/"+strconv.Itoa(number), nil)
	Expect(err).NotTo(HaveOccurred())
	request.Header.Set("Authorization", "Bearer token")
	response, err := http.DefaultClient.Do(request)
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	Expect(err).NotTo(HaveOccurred())
	var pull struct {
		Head struct {
			Sha hostile.Sha `json:"sha"`
		} `json:"head"`
	}
	Expect(json.Unmarshal(body, &pull)).To(Succeed())
	return body, pull.Head.Sha
}

var _ = Describe("A scan", func() {
	It("and a webhook key identically", func(ctx SpecContext) {
		sim := simforge.Start()
		DeferCleanup(sim.Close)
		sim.AddRepo(simforge.PublicRepo(repo))
		number := sim.AddPull(repo, simforge.RenovatePull("renovate/lodash-4.x", "Update lodash"))
		webhooks := &receiver{}
		server := httptest.NewServer(webhooks)
		DeferCleanup(server.Close)
		sim.DeliverTo(server.URL, "s")
		sim.PullRequest(ctx, repo, number, "opened")

		body, head := rest(ctx, sim, number)
		scanned, err := workflow.ScannedPull[events.PullRequestOpened](hostile.MustParseSlug(repo), uint64(number), head, body)
		Expect(err).NotTo(HaveOccurred())

		updates := runnable(definitions.DependencyUpdates)[0]
		fromWebhook, err := updates.Identify(webhooks.first())
		Expect(err).NotTo(HaveOccurred())
		fromScan, err := updates.Identify(scanned.Payload)
		Expect(err).NotTo(HaveOccurred())
		Expect(fromScan.Key).To(Equal(fromWebhook.Key))
		Expect(fromScan.Subjects).To(Equal(fromWebhook.Subjects))
	})

	It("needs what it reaches, which is in the manifest once and checked at start-up", func() {
		scans, err := engine.New(scanning, scanOnlyNeeds)
		Expect(err).NotTo(HaveOccurred())
		set := providers.Provide(providers.New(), pullsClient{})
		rt := runtime.New([]workflow.Registered{scans}, set)

		Expect(rt.Validate()).To(MatchError(ContainSubstring("scan_only_needs needs a provider for definitions_test.logDir")))
		Expect(rt.Manifest()[0].Scans).To(BeTrue(), "the manifest says a workflow scans")
		Expect(names(rt.Manifest()[0].Requires)).To(ConsistOf("definitions_test.logDir", "definitions_test.pullsClient"))
	})
})
