package definitions_test

import (
	"context"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/almanactest/definitions"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/broker"
	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/internal/pgtest"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/store/postgres"
)

// secret is the webhook secret simforge signs with and the broker checks.
const secret = "a webhook secret"

// forge is simforge delivering, through the broker, to the deliveries log in PostgreSQL.
type forge struct {
	ctx context.Context
	url string
	log *deliveries.Log
	sim *simforge.Forge
}

func newForge(ctx context.Context) *forge {
	GinkgoHelper()
	url := pgtest.Database()
	log, err := deliveries.Connect(ctx, url)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(log.Close)
	server := httptest.NewServer(broker.New(broker.NewGitHub(secret), log, logger()))
	DeferCleanup(server.Close)
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	sim.AddRepo(simforge.PublicRepo(repo))
	sim.DeliverTo(server.URL+"/webhooks/github", secret)
	return &forge{ctx: ctx, url: url, log: log, sim: sim}
}

// opened opens a Renovate pull request from branch, and GitHub sends its webhook.
func (f *forge) opened(branch string) {
	number := f.sim.AddPull(repo, simforge.RenovatePull(branch, "Update"))
	f.sim.PullRequest(f.ctx, repo, number, "opened")
}

// almanac is an almanac named name running models: its cursor on the log, and its runtime on
// its own state, with every fake it needs.
type almanac struct {
	ctx     context.Context
	cursor  *deliveries.Cursor
	runtime *runtime.Runtime
}

func (f *forge) almanac(name string, models ...model.Workflow) *almanac {
	GinkgoHelper()
	cursor, err := f.log.Cursor(f.ctx, name)
	Expect(err).NotTo(HaveOccurred())
	st, err := postgres.Connect(f.ctx, f.url, name)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(st.Close)
	set := providers.New()
	providers.Provide[definitions.Registry](set, &definitions.FakeRegistry{})
	set.UseHarness(almanactest.Replying(review("NoConcerns")))
	return &almanac{ctx: f.ctx, cursor: cursor, runtime: runtime.New(runnable(models...), set,
		runtime.WithStore(st), runtime.WithLogger(logger()))}
}

// catchUp takes what the almanac has not read from the log, and says how much that was.
func (a *almanac) catchUp() int {
	GinkgoHelper()
	read, err := a.cursor.CatchUp(a.ctx, app.Consume(a.runtime, logger()))
	Expect(err).NotTo(HaveOccurred())
	return read
}

// runs drains the almanac and counts the runs that decided.
func (a *almanac) runs() int {
	GinkgoHelper()
	outcomes, err := a.runtime.Drain(a.ctx)
	Expect(err).NotTo(HaveOccurred())
	ran := 0
	for _, o := range outcomes {
		if o.Result == runtime.Ran {
			ran++
		}
	}
	return ran
}

var _ = Describe("The samples behind the broker and the log", Label("db"), func() {
	It("two almanacs both see a delivery, and only a matching one acts", func(ctx SpecContext) {
		f := newForge(ctx)
		a := f.almanac("a", definitions.DependencyUpdates)
		b := f.almanac("b")
		f.opened("renovate/a")

		Expect(a.catchUp()).To(Equal(1))
		Expect(b.catchUp()).To(Equal(1))
		Expect(a.runs()).To(Equal(1))
		Expect(b.runs()).To(BeZero())

		Expect(a.catchUp()).To(BeZero(), "each sees it once")
		Expect(b.catchUp()).To(BeZero(), "each sees it once")
	})

	It("an almanac that was stopped catches up from its cursor, and runs what it missed", func(ctx SpecContext) {
		f := newForge(ctx)
		f.almanac("b", definitions.DependencyUpdates)
		for _, branch := range []string{"renovate/a", "renovate/b", "renovate/c"} {
			f.opened(branch)
		}
		restarted := f.almanac("b", definitions.DependencyUpdates)
		Expect(restarted.catchUp()).To(Equal(3))
		Expect(restarted.runs()).To(Equal(3))
	})

	It("a new almanac starts at the end of the log", func(ctx SpecContext) {
		f := newForge(ctx)
		f.opened("renovate/old")
		Expect(f.almanac("new", definitions.DependencyUpdates).catchUp()).To(BeZero())
	})
})
