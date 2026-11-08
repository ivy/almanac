package app_test

import (
	"context"
	"net/http"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/config"
	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/internal/pgtest"
)

// syncBuffer is output written from more than one goroutine.
type syncBuffer struct {
	mu   sync.Mutex
	text []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text = append(b.text, p...)
	_, _ = GinkgoWriter.Write(p)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.text)
}

// daemon is one process running the run verb in this one.
type daemon struct {
	stderr *syncBuffer
	code   chan int
	cancel context.CancelFunc
}

// start runs the run verb of a, with args after it, until stop.
func start(a app.Almanac, args ...string) *daemon {
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{stderr: &syncBuffer{}, code: make(chan int, 1), cancel: cancel}
	go func() {
		defer GinkgoRecover()
		d.code <- a.Execute(ctx, append([]string{"run"}, args...), GinkgoWriter, d.stderr)
	}()
	return d
}

// stop asks the daemon to stop, as SIGTERM does, and returns its exit status.
func (d *daemon) stop() int {
	d.cancel()
	var code int
	Eventually(d.code).WithTimeout(30 * time.Second).Should(Receive(&code))
	return code
}

// healthy waits until the daemon at address answers its health check.
func healthy(address string) {
	GinkgoHelper()
	Eventually(func() (int, error) {
		response, err := http.Get("http://" + address + "/healthz")
		if err != nil {
			return 0, err
		}
		return response.StatusCode, response.Body.Close()
	}).WithTimeout(30 * time.Second).Should(Equal(http.StatusOK))
}

var _ = Describe("run", func() {
	It("needs a database", func() {
		out := execute(newWorld().almanac(holding), "--format", "json", "run")
		Expect(out.code).To(Equal(1))
		Expect(decoded(out.stderr)).To(HaveKeyWithValue("code", "almanac::config::env_missing"))
		Expect(out.stderr).To(ContainSubstring("DATABASE_URL"))
	})

	It("fails at start-up with a diagnostic when a provider is missing", func() {
		setenv("DATABASE_URL", "postgres://unreachable.invalid/none")
		w := newWorld()
		w.fleet = nil
		out := execute(w.almanac(watching), "--format", "json", "run")
		Expect(out.code).To(Equal(1))
		Expect(decoded(out.stderr)).To(HaveKeyWithValue("code", "almanac::provider::missing"))
	})

	DescribeTable("answers health checks on loopback only",
		func(listen string) {
			setenv("DATABASE_URL", "postgres://unreachable.invalid/none")
			setenv("ALMANAC_LISTEN", listen)
			out := execute(newWorld().almanac(holding), "--format", "json", "run")
			Expect(out.code).To(Equal(1))
			Expect(decoded(out.stderr)).To(And(HaveKeyWithValue("code", "almanac::config::listen"),
				HaveKeyWithValue("message", "ALMANAC_LISTEN="+listen+" is not a loopback address and port")))
		},
		Entry("not every interface", "0.0.0.0:8790"),
		Entry("not a host's own address", "192.0.2.1:8790"),
		Entry("not a name", "localhost:8790"),
		Entry("not without a port", "127.0.0.1"),
	)

	It("answers on 127.0.0.1:8790 unless listen or ALMANAC_LISTEN says otherwise", func() {
		Expect(app.HealthAddress(config.Config{})).To(Equal("127.0.0.1:8790"))
		Expect(app.HealthAddress(config.Config{Listen: "[::1]:9000"})).To(Equal("[::1]:9000"))
	})

	Context("with a database", Label("db"), func() {
		var (
			ctx     context.Context
			w       *world
			log     *deliveries.Log
			address string
		)
		BeforeEach(func(sc SpecContext) {
			ctx = context.WithoutCancel(sc)
			w = newWorld()
			url := pgtest.Database()
			address = freeAddress()
			setenv("DATABASE_URL", url)
			setenv("ALMANAC_LISTEN", address)
			var err error
			log, err = deliveries.Connect(ctx, url)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(log.Close)
		})

		It("stopped mid-run, abandons the run after its grace, and the next start resumes it", func() {
			setenv("ALMANAC_MODE", "live")
			gated := almanactest.Replying(okReply).Gated()
			w.answering(gated)
			a := app.WithGrace(w.almanac(holding), 200*time.Millisecond)

			first := start(a)
			healthy(address)
			logged(ctx, log, "d1", opened, pull(1))
			Eventually(gated.Calls).Should(Equal(1), "the run reaches its agent")
			Expect(w.forge.Statuses()).To(Equal([]almanactest.Status{{On: held, State: effect.Pending}}))
			Expect(first.stop()).To(Equal(0), "a stop is not an error")
			Expect(first.stderr.String()).To(ContainSubstring("abandoning in-flight runs"))
			Expect(w.forge.Statuses()).To(Equal([]almanactest.Status{{On: held, State: effect.Pending}}), "nothing was decided")

			answering := almanactest.Replying(okReply)
			w.answering(answering)
			second := start(a)
			Eventually(w.forge.Statuses).Should(Equal([]almanactest.Status{{On: held, State: effect.Success}}), "the run resumed and finished")
			Expect(answering.Calls()).To(Equal(1))
			Expect(second.stop()).To(Equal(0))
			Expect(second.stderr.String()).To(MatchRegexp(`"msg":"running".*"requeued":1`))
			Expect(second.stderr.String()).To(ContainSubstring("stopped cleanly"))
		})

		// crates/almanac/src/daemon.rs logs "scheduled scan finished" with these fields, and
		// docs/operating.md tells the operator to watch for it.
		It("logs each scheduled scan with what it found and how long it took", func() {
			d := start(w.almanac(watching))
			healthy(address)
			Eventually(d.stderr.String).Should(MatchRegexp(
				`"level":"INFO","msg":"scheduled scan finished","found":1,"queued":1,"unchanged":0,"rejected":0,"skipped":false,"seconds":\d`))
			Expect(d.stop()).To(Equal(0))
		})

		It("follows the log, and stops cleanly once its run is done", func() {
			setenv("ALMANAC_MODE", "live")
			d := start(w.almanac(holding))
			healthy(address)
			logged(ctx, log, "d1", opened, pull(1))
			logged(ctx, log, "d2", "github.issues.opened", []byte(`{}`))
			Eventually(w.forge.Statuses).Should(Equal([]almanactest.Status{{On: held, State: effect.Success}}))
			Expect(d.stop()).To(Equal(0))
			Expect(d.stderr.String()).To(MatchRegexp(`"msg":"running","almanac":"almanac-spec","scope":"almanac-spec","mode":"live","read_only":false,"requeued":0,"listen":"` + address + `"`))
			Expect(d.stderr.String()).To(ContainSubstring("stopped cleanly"))
		})

		DescribeTable("applies effects only when ALMANAC_MODE is live, and --dry-run wins",
			func(mode string, args []string, scope string, applies bool) {
				if mode != "" {
					setenv("ALMANAC_MODE", mode)
				}
				d := start(w.almanac(holding), args...)
				healthy(address)
				logged(ctx, log, "d1", opened, pull(1))
				Eventually(d.stderr.String).Should(ContainSubstring(`"msg":"run finished"`))
				Expect(d.stop()).To(Equal(0))
				Expect(d.stderr.String()).To(ContainSubstring(`"scope":"` + scope + `"`))
				if applies {
					Expect(w.forge.Statuses()).To(Equal([]almanactest.Status{{On: held, State: effect.Success}}))
				} else {
					Expect(w.forge.Calls()).To(BeEmpty())
				}
			},
			Entry("live", "live", nil, "almanac-spec", true),
			Entry("live, with --dry-run", "live", []string{"--dry-run"}, "almanac-spec.dry-run", false),
			Entry("unset", "", nil, "almanac-spec.dry-run", false),
			Entry("anything but live", "Live", nil, "almanac-spec.dry-run", false),
		)

		It("calls no model and writes nothing when read-only, whatever the mode", func() {
			setenv("ALMANAC_MODE", "live")
			setenv("ALMANAC_READ_ONLY", "1")
			d := start(w.almanac(holding))
			healthy(address)
			logged(ctx, log, "d1", opened, pull(1))
			Eventually(d.stderr.String).Should(ContainSubstring(`"msg":"run failed"`))
			Expect(d.stop()).To(Equal(0))
			Expect(d.stderr.String()).To(ContainSubstring(`"mode":"dry-run","read_only":true`))
			Expect(w.forge.Calls()).To(BeEmpty())
			Expect(w.harness.Calls()).To(BeZero())
		})

		It("refuses a port another process holds", func() {
			busy := start(w.almanac(holding))
			healthy(address)
			out := execute(w.almanac(holding), "--format", "json", "run")
			Expect(out.code).To(Equal(1))
			Expect(decoded(out.stderr)).To(And(HaveKeyWithValue("code", "almanac::config::listen"),
				HaveKeyWithValue("message", "cannot listen on "+address)))
			Expect(busy.stop()).To(Equal(0))
		})
	})
})
