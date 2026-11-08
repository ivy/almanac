package broker_test

import (
	"context"
	"log/slog"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/broker"
	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/simforge"
)

const repo = "owner/repo"

// world is simforge delivering to a broker over a log in PostgreSQL.
type world struct {
	url string
	log *deliveries.Log
	sim *simforge.Forge
}

func newWorld(ctx context.Context) *world {
	GinkgoHelper()
	url := database()
	log, err := deliveries.Connect(ctx, url)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(log.Close)
	logger := slog.New(slog.NewTextHandler(GinkgoWriter, nil))
	server := httptest.NewServer(broker.New(broker.NewGitHub(secret), log, logger))
	DeferCleanup(server.Close)
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	sim.AddRepo(simforge.PublicRepo(repo))
	sim.DeliverTo(server.URL+"/webhooks/github", secret)
	return &world{url: url, log: log, sim: sim}
}

func (w *world) opened(ctx context.Context, branch string) {
	number := w.sim.AddPull(repo, simforge.RenovatePull(branch, "Update"))
	w.sim.PullRequest(ctx, repo, number, "opened")
}

func (w *world) logged(ctx context.Context) int {
	GinkgoHelper()
	logged, err := w.log.After(ctx, 0, 1000)
	Expect(err).NotTo(HaveOccurred())
	return len(logged)
}

// almanac opens a cursor as an almanac named name would, on a connection of
// its own.
func (w *world) almanac(ctx context.Context, name string) (*deliveries.Cursor, func()) {
	GinkgoHelper()
	log, err := deliveries.Connect(ctx, w.url)
	Expect(err).NotTo(HaveOccurred())
	cursor, err := log.Cursor(ctx, name)
	Expect(err).NotTo(HaveOccurred())
	return cursor, log.Close
}

// kinds catches up and returns the kinds of what it read.
func kinds(ctx context.Context, cursor *deliveries.Cursor) []string {
	GinkgoHelper()
	var read []string
	_, err := cursor.CatchUp(ctx, func(_ context.Context, d deliveries.Logged) error {
		read = append(read, d.Kind)
		return nil
	})
	Expect(err).NotTo(HaveOccurred())
	return read
}

var _ = Describe("The broker and the log", Label("db"), func() {
	It("acknowledgement never waits on any almanac", func(ctx SpecContext) {
		w := newWorld(ctx)
		w.opened(ctx, "renovate/a")
		Expect(w.logged(ctx)).To(Equal(1), "acknowledged with no almanac running")
	})

	It("two almanacs both see a delivery, once each", func(ctx SpecContext) {
		w := newWorld(ctx)
		a, closeA := w.almanac(ctx, "a")
		defer closeA()
		b, closeB := w.almanac(ctx, "b")
		defer closeB()
		w.opened(ctx, "renovate/a")

		Expect(kinds(ctx, a)).To(Equal([]string{"github.pull_request.opened"}))
		Expect(kinds(ctx, b)).To(Equal([]string{"github.pull_request.opened"}))
		Expect(kinds(ctx, a)).To(BeEmpty(), "each sees it once")
		Expect(kinds(ctx, b)).To(BeEmpty(), "each sees it once")
	})

	It("an almanac that was stopped catches up from its cursor", func(ctx SpecContext) {
		w := newWorld(ctx)
		_, stop := w.almanac(ctx, "b")
		stop()
		for _, branch := range []string{"renovate/a", "renovate/b", "renovate/c"} {
			w.opened(ctx, branch)
		}
		cursor, stop := w.almanac(ctx, "b")
		defer stop()
		Expect(kinds(ctx, cursor)).To(HaveLen(3))
	})

	It("a redelivery reaches the log once", func(ctx SpecContext) {
		w := newWorld(ctx)
		number := w.sim.AddPull(repo, simforge.RenovatePull("renovate/a", "Update"))
		w.sim.NextDeliveries(simforge.Duplicated)
		w.sim.PullRequest(ctx, repo, number, "opened")
		Expect(w.logged(ctx)).To(Equal(1))
	})
})
