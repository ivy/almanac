package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
)

const (
	// DefaultGrace is how long an in-flight run gets to finish once a daemon is asked to stop.
	// A run abandoned then is requeued when the daemon starts again, so it is repeated and not
	// lost.
	DefaultGrace = 30 * time.Second
	// DefaultTick is how often a daemon asks whether a scheduled scan is due. The scan
	// interval itself is a limit, scan_interval_minutes.
	DefaultTick = 30 * time.Second
)

// Backoff is how long to wait between attempts at something that is down: First, then twice
// as long each time, up to Longest. An outage is waited out, so a supervisor never sees a crash
// loop for a database that is restarting, and nothing hammers what is down.
type Backoff struct {
	First   time.Duration
	Longest time.Duration
}

// DefaultBackoff starts at a second and waits at most a minute.
var DefaultBackoff = Backoff{First: time.Second, Longest: time.Minute}

// retry calls attempt until it succeeds or ctx ends, and reports whether it succeeded.
func (b Backoff) retry(ctx context.Context, logger *slog.Logger, what string, attempt func(context.Context) error) bool {
	wait := b.First
	for {
		err := attempt(ctx)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		logger.Warn(what+" failed, retrying", "error", err, "wait", wait.String())
		if !sleep(ctx, wait) {
			return false
		}
		wait = min(wait*2, b.Longest)
	}
}

// sleep waits for d, and reports false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Health is whether a daemon is reaching its store. A daemon is unhealthy until it has
// connected, and while it has lost the store.
type Health struct{ healthy atomic.Bool }

// Set records whether the daemon is reaching its store.
func (h *Health) Set(healthy bool) { h.healthy.Store(healthy) }

// Healthy reports whether the daemon is reaching its store.
func (h *Health) Healthy() bool { return h.healthy.Load() }

// ServeHTTP answers a health check: 200 while healthy, and 503 otherwise.
func (h *Health) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if h.Healthy() {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("cannot reach the store\n"))
}

// Consume is a deliveries.Handler that ingests each delivery into rt as a webhook. Delivery
// is at-least-once, and rt dedupes by source and delivery ID, so a delivery handed over twice
// runs once.
//
// A delivery that can never be taken, such as one of a kind no workflow handles or one whose
// payload does not parse, is a value Ingest returns, and the cursor moves past it. Only a
// failure that may pass, such as the store being down, is returned, so the delivery is read
// again.
func Consume(rt *runtime.Runtime, logger *slog.Logger) deliveries.Handler {
	return func(ctx context.Context, d deliveries.Logged) error {
		ingest, err := rt.Ingest(ctx, runtime.Envelope{
			Source:     d.Source,
			DeliveryID: d.DeliveryID,
			Kind:       d.Kind,
			Origin:     store.Webhook,
			Payload:    d.Body,
		})
		if err != nil {
			return err
		}
		// The body is hostile and never logged. Kind and ID are identifiers the broker checked.
		level := slog.LevelInfo
		switch ingest {
		case store.Unrouted, store.Duplicate, store.Echo:
			level = slog.LevelDebug
		case store.Rejected:
			level = slog.LevelWarn
		}
		logger.Log(ctx, level, "took a delivery", "seq", d.Seq, "source", d.Source, "delivery", d.DeliveryID, "kind", d.Kind, "ingest", string(ingest))
		return nil
	}
}

// Daemon follows the deliveries log into a runtime and runs what arrives, runs scheduled
// scans beside it, and stops cleanly.
type Daemon struct {
	Runtime *runtime.Runtime
	// Cursor is the almanac's place in the log. Its Poll is how long a lost notification can
	// delay a delivery.
	Cursor *deliveries.Cursor
	// Health is set false while the store is lost, and true again once it answers. Nil keeps
	// no health.
	Health *Health
	// Logger logs what the daemon does. Nil logs to slog.Default.
	Logger *slog.Logger
	// Grace is how long an in-flight run gets once a stop is asked for. Zero is DefaultGrace.
	Grace time.Duration
	// Tick is how often to ask whether a scheduled scan is due. Zero is DefaultTick.
	Tick time.Duration
	// Backoff is how to wait out a lost store. The zero Backoff is DefaultBackoff.
	Backoff Backoff
}

// Run follows the log until ctx ends. Then no new run starts, and a run under way gets Grace
// to finish. A run still going after that is abandoned, left running in the store, and requeued
// by Recover when the almanac next starts.
//
// Work runs under a context of its own, so that a stop does not cut it short: a run is
// abandoned only when Grace runs out.
func (d *Daemon) Run(ctx context.Context) {
	d.defaults()
	work, abandon := context.WithCancel(context.WithoutCancel(ctx))
	defer abandon()
	// following ends only once claiming has stopped, so nothing new starts between a stop and
	// the runtime hearing of it.
	following, stopFollowing := context.WithCancel(context.WithoutCancel(ctx))
	defer stopFollowing()

	var wg sync.WaitGroup
	wg.Go(func() { d.follow(following, work) })
	// Scanning runs beside the log, so a slow scan never delays a live webhook.
	wg.Go(func() { d.scan(following, work) })
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	<-ctx.Done()
	d.Logger.Info("stopping: no new runs will start")
	d.Runtime.StopClaiming()
	stopFollowing()
	select {
	case <-done:
		d.Logger.Info("stopped cleanly")
	case <-time.After(d.Grace):
		d.Logger.Warn("abandoning in-flight runs: they will be requeued on restart")
		abandon()
		<-done
	}
}

func (d *Daemon) defaults() {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Health == nil {
		d.Health = &Health{}
	}
	if d.Grace <= 0 {
		d.Grace = DefaultGrace
	}
	if d.Tick <= 0 {
		d.Tick = DefaultTick
	}
	if d.Backoff == (Backoff{}) {
		d.Backoff = DefaultBackoff
	}
}

// follow follows the log until following ends, waiting out a lost store. Ingesting and
// settling run under work.
func (d *Daemon) follow(following, work context.Context) {
	handle := Consume(d.Runtime, d.Logger)
	take := func(_ context.Context, logged deliveries.Logged) error { return handle(work, logged) }
	settle := func(context.Context) error {
		_, err := d.Runtime.Settle(work)
		return err
	}
	d.Health.Set(true)
	wait := d.Backoff.First
	for {
		err := d.Cursor.Follow(following, take, settle)
		if err == nil || following.Err() != nil {
			return
		}
		d.Health.Set(false)
		d.Logger.Warn("lost the store, retrying", "error", err, "wait", wait.String())
		if !sleep(following, wait) {
			return
		}
		wait = min(wait*2, d.Backoff.Longest)
		if d.Runtime.Ping(following) == nil {
			d.Health.Set(true)
			wait = d.Backoff.First
		}
	}
}

// scan runs a scheduled scan whenever one is due, until following ends. What a scan queues
// runs when the log next wakes, at most the cursor's Poll later.
//
// Each scan is logged, with what it found and how long it took, so a quiet daemon can be told
// from one whose scanning has stopped. docs/operating.md tells the operator to watch for it.
func (d *Daemon) scan(following, work context.Context) {
	for {
		started := time.Now()
		report, ran, err := d.Runtime.Tick(work)
		switch {
		case err != nil:
			d.Logger.Warn("a scheduled scan failed", "error", err)
		case ran:
			d.Logger.Info("scheduled scan finished", "found", report.Found, "queued", report.Queued,
				"unchanged", report.Unchanged, "rejected", report.Rejected, "skipped", report.Skipped,
				"seconds", time.Since(started).Seconds())
		}
		if !sleep(following, d.Tick) {
			return
		}
	}
}
