package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/config"
	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/internal/pgtest"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store/postgres"
	"github.com/ivy/almanac/workflow"
)

// The Rust specification is crates/definitions/tests/daemon.rs, and the routing half of
// crates/definitions/tests/broker_flow.rs.

// logger writes what the code under test logs to the spec's output.
func logger() *slog.Logger { return slog.New(slog.NewTextHandler(GinkgoWriter, nil)) }

// logged appends a delivery to the log, as the broker would.
func logged(ctx context.Context, log *deliveries.Log, id, kind string, body []byte) {
	GinkgoHelper()
	appended, err := log.Append(ctx, deliveries.Delivery{Source: "github", DeliveryID: id, Kind: kind, Body: body})
	Expect(err).NotTo(HaveOccurred())
	Expect(appended).To(BeTrue())
}

// liveRuntime is a live runtime for almanac on the database at url, reaching forge and
// harness. Making one again on the same database is a restart.
func liveRuntime(ctx context.Context, url, almanac string, forge *almanactest.FakeForge, harness *almanactest.ScriptedHarness, options ...runtime.Option) *runtime.Runtime {
	GinkgoHelper()
	st, err := postgres.Connect(ctx, url, almanac)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(st.Close)
	set := providers.New()
	providers.Provide[effect.Forge](set, forge)
	providers.Provide[effect.Tracker](set, &almanactest.FakeTracker{})
	set.UseHarness(harness)
	options = append([]runtime.Option{runtime.WithStore(st), runtime.InMode(effect.Live), runtime.WithLogger(logger())}, options...)
	return runtime.New([]workflow.Registered{holding}, set, options...)
}

// running runs d until the returned stop is called, and stop waits for it to return.
func running(d *app.Daemon) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer GinkgoRecover()
		defer close(done)
		d.Run(ctx)
	}()
	return func() {
		cancel()
		Eventually(done).WithTimeout(30 * time.Second).Should(BeClosed())
	}
}

var _ = Describe("The daemon", Label("db"), func() {
	var (
		ctx context.Context
		url string
		log *deliveries.Log
	)
	BeforeEach(func(sc SpecContext) {
		ctx = context.WithoutCancel(sc)
		url = pgtest.Database()
		var err error
		log, err = deliveries.Connect(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(log.Close)
	})

	cursor := func(almanac string) *deliveries.Cursor {
		GinkgoHelper()
		c, err := log.Cursor(ctx, almanac)
		Expect(err).NotTo(HaveOccurred())
		return c
	}

	It("a stop during a run starts nothing new, and a restart finishes the work", func() {
		c := cursor("daemon")
		logged(ctx, log, "d1", opened, pull(1))
		logged(ctx, log, "d2", opened, pull(2))

		forge := &almanactest.FakeForge{}
		gated := almanactest.Replying(okReply).Gated()
		first := liveRuntime(ctx, url, "daemon", forge, gated, runtime.Tightened(config.Limits{Concurrency: 1}))
		stop := running(&app.Daemon{Runtime: first, Cursor: c, Logger: logger(), Grace: 20 * time.Second})

		Eventually(gated.Calls).Should(Equal(1), "the first run reaches its agent")
		Expect(forge.Statuses()).To(Equal([]almanactest.Status{{On: held, State: effect.Pending}}), "held before the agent ran")
		stopped := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			stop()
			close(stopped)
		}()
		time.Sleep(100 * time.Millisecond)
		gated.Release()
		Eventually(stopped).Should(BeClosed())
		Expect(gated.Calls()).To(Equal(1), "no new run started after the stop")

		second := liveRuntime(ctx, url, "daemon", forge, almanactest.Replying(okReply))
		Expect(second.Recover(ctx)).To(BeZero(), "the run under way finished within its grace")
		Expect(second.Drain(ctx)).To(HaveLen(1), "the rest stayed queued for the next start")
		_, err := second.Deliver(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(forge.Statuses()).To(Equal([]almanactest.Status{{On: held, State: effect.Success}}), "one status per commit and context, and pending was cleared")
	})

	It("a status left pending by a killed run is cleared after a restart", func() {
		c := cursor("daemon")
		logged(ctx, log, "d1", opened, pull(1))

		forge := &almanactest.FakeForge{}
		gated := almanactest.Replying(okReply).Gated()
		first := liveRuntime(ctx, url, "daemon", forge, gated)
		Expect(c.CatchUp(ctx, app.Consume(first, logger()))).To(Equal(1))
		killed, kill := context.WithCancel(ctx)
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			_, _ = first.Drain(killed)
		}()
		Eventually(gated.Calls).Should(Equal(1))
		Expect(forge.Statuses()).To(Equal([]almanactest.Status{{On: held, State: effect.Pending}}))
		kill()
		Eventually(drained).Should(BeClosed())

		restarted := liveRuntime(ctx, url, "daemon", forge, almanactest.Replying(okReply))
		Expect(restarted.Recover(ctx)).To(Equal(1))
		Expect(restarted.Drain(ctx)).To(HaveLen(1))
		_, err := restarted.Deliver(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(forge.Statuses()).To(Equal([]almanactest.Status{{On: held, State: effect.Success}}))
	})

	It("a database outage backs off and recovers with no crash", func() {
		proxy := newProxy(url)
		DeferCleanup(proxy.close)
		through, err := deliveries.Connect(ctx, proxy.url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(through.Close)
		c, err := through.Cursor(ctx, "daemon")
		Expect(err).NotTo(HaveOccurred())
		c.Poll = 100 * time.Millisecond

		forge := &almanactest.FakeForge{}
		health := &app.Health{}
		stop := running(&app.Daemon{
			Runtime: liveRuntime(ctx, proxy.url, "daemon", forge, almanactest.Replying(okReply)),
			Cursor:  c, Health: health, Logger: logger(),
			Backoff: app.Backoff{First: 50 * time.Millisecond, Longest: 200 * time.Millisecond},
		})
		DeferCleanup(stop)
		Eventually(health.Healthy).Should(BeTrue())

		time.Sleep(300 * time.Millisecond)
		proxy.down()
		Eventually(health.Healthy).WithTimeout(10*time.Second).Should(BeFalse(), "the outage is noticed")
		logged(ctx, log, "d1", opened, pull(1))
		proxy.up()
		Eventually(health.Healthy).WithTimeout(10*time.Second).Should(BeTrue(), "recovered")
		Eventually(forge.Statuses).WithTimeout(10*time.Second).Should(
			Equal([]almanactest.Status{{On: held, State: effect.Success}}), "the delivery logged during the outage ran")
	})

	It("two almanacs both see a delivery, and only a matching one acts", func() {
		a, b := cursor("a"), cursor("b")
		forge := &almanactest.FakeForge{}
		runtimeA := liveRuntime(ctx, url, "a", forge, almanactest.Replying(okReply))
		st, err := postgres.Connect(ctx, url, "b")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		runtimeB := runtime.New(nil, providers.New(), runtime.WithStore(st), runtime.InMode(effect.Live))
		logged(ctx, log, "d1", opened, pull(1))

		Expect(a.CatchUp(ctx, app.Consume(runtimeA, logger()))).To(Equal(1))
		Expect(b.CatchUp(ctx, app.Consume(runtimeB, logger()))).To(Equal(1))
		Expect(runtimeA.Drain(ctx)).To(HaveLen(1))
		Expect(runtimeB.Drain(ctx)).To(BeEmpty())

		Expect(a.CatchUp(ctx, app.Consume(runtimeA, logger()))).To(BeZero(), "each sees it once")
		Expect(b.CatchUp(ctx, app.Consume(runtimeB, logger()))).To(BeZero(), "each sees it once")
	})

	It("ignores kinds it has no workflow for, and passes a payload that does not parse", func() {
		c := cursor("daemon")
		rt := liveRuntime(ctx, url, "daemon", &almanactest.FakeForge{}, almanactest.Replying(okReply))
		logged(ctx, log, "d1", "github.issues.opened", []byte(`{"action": "opened"}`))
		logged(ctx, log, "d2", opened, []byte(`{"number": "not a number"}`))
		logged(ctx, log, "d3", opened, pull(3))

		Expect(c.CatchUp(ctx, app.Consume(rt, logger()))).To(Equal(3), "none of them stops the cursor")
		Expect(c.CatchUp(ctx, app.Consume(rt, logger()))).To(BeZero())
		outcomes, err := rt.Drain(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(outcomes).To(ConsistOf(HaveField("Result", runtime.Ran)), "only the delivery a workflow handles runs")
	})

	It("a store that fails stops the cursor before the delivery, so it is read again", func() {
		c := cursor("daemon")
		st, err := postgres.Connect(ctx, url, "daemon")
		Expect(err).NotTo(HaveOccurred())
		rt := runtime.New([]workflow.Registered{holding}, providers.New(), runtime.WithStore(st))
		st.Close()
		logged(ctx, log, "d1", opened, pull(1))

		_, err = c.CatchUp(ctx, app.Consume(rt, logger()))
		Expect(err).To(HaveOccurred())
		Expect(c.Seq(ctx)).To(BeZero())
	})
})

// proxy forwards connections to a database until it is taken down, when it cuts every
// connection and refuses new ones.
type proxy struct {
	url      string
	listener net.Listener
	target   string
	isUp     atomic.Bool
	mu       sync.Mutex
	conns    []net.Conn
}

func newProxy(databaseURL string) *proxy {
	GinkgoHelper()
	u, err := url.Parse(databaseURL)
	Expect(err).NotTo(HaveOccurred())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	p := &proxy{listener: listener, target: u.Host}
	p.isUp.Store(true)
	u.Host = listener.Addr().String()
	p.url = u.String()
	go p.serve()
	return p
}

func (p *proxy) serve() {
	for {
		inbound, err := p.listener.Accept()
		if err != nil {
			return
		}
		if !p.isUp.Load() {
			_ = inbound.Close()
			continue
		}
		outbound, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = inbound.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, inbound, outbound)
		p.mu.Unlock()
		go pipe(inbound, outbound)
		go pipe(outbound, inbound)
	}
}

func pipe(to, from net.Conn) {
	_, _ = io.Copy(to, from)
	_ = to.Close()
	_ = from.Close()
}

func (p *proxy) down() {
	p.isUp.Store(false)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		_ = conn.Close()
	}
	p.conns = nil
}

func (p *proxy) up() { p.isUp.Store(true) }

func (p *proxy) close() {
	p.down()
	_ = p.listener.Close()
}
