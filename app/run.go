package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ivy/almanac/config"
	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store/postgres"
)

// DefaultListen is where a daemon answers health checks unless listen or ALMANAC_LISTEN says
// otherwise.
const DefaultListen = "127.0.0.1:8790"

// listenSetting names where the listen address is set, for a person who must change it.
const listenSetting = "ALMANAC_LISTEN"

// healthAddress is where the daemon answers health checks: loopback only, because anything
// that must reach it from elsewhere goes through something in front of it.
func healthAddress(cfg config.Config) (string, error) {
	listen := cfg.Listen
	if listen == "" {
		listen = DefaultListen
	}
	address, err := netip.ParseAddrPort(listen)
	if err != nil || !address.Addr().IsLoopback() {
		return "", diag.NotLoopback(listenSetting, listen, DefaultListen)
	}
	return listen, nil
}

// daemonLogger logs JSON to stderr, for the journal, or text when a person is watching.
func daemonLogger(stderr io.Writer) *slog.Logger {
	if f, ok := stderr.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return slog.New(slog.NewTextHandler(stderr, nil))
		}
	}
	return slog.New(slog.NewJSONHandler(stderr, nil))
}

// tellManager tells systemd how the daemon is doing, when systemd started it.
func tellManager(logger *slog.Logger, state string) {
	if _, err := daemon.SdNotify(false, state); err != nil {
		logger.Warn("could not notify the service manager", "error", err)
	}
}

// daemon is the run verb: it stays up, following the broker's log, until ctx ends.
//
// Everything that can be refused at once is checked before anything connects: config, the
// models, the providers, DATABASE_URL and the listen address. Reaching the database is then
// waited out with backoff, and so is recovering the runs a previous process left running.
func (a *Almanac) daemon(ctx context.Context, stderr io.Writer, dryRun bool) error {
	logger := daemonLogger(stderr)
	slog.SetDefault(logger)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switches := runtime.SwitchesFrom(dryRun, os.LookupEnv)
	workflows, err := a.workflows()
	if err != nil {
		return err
	}
	set, err := a.providers(ctx, Setting{Name: a.Name, Config: cfg, Switches: switches})
	if err != nil {
		return err
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return diag.MissingEnv("DATABASE_URL", "the PostgreSQL database shared with the broker")
	}
	listen, err := healthAddress(cfg)
	if err != nil {
		return err
	}
	// A pool connects when first used, so nothing reaches the database before Validate.
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return diag.Store(err)
	}
	defer pool.Close()
	scope := switches.Mode.Scope(a.Name)
	rt := runtime.New(workflows, set,
		runtime.WithStore(postgres.New(pool, scope)),
		runtime.Switched(switches),
		runtime.Tightened(cfg.Tighten(a.limits())),
		runtime.WithLogger(logger),
	)
	if err := rt.Validate(); err != nil {
		return err
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return diag.CannotListen(listenSetting, listen, err)
	}
	health := &Health{}
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", health)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("stopped answering health checks", "error", err)
		}
	}()
	defer func() {
		closing, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(closing)
	}()

	var cursor *deliveries.Cursor
	connected := DefaultBackoff.retry(ctx, logger, "connecting to the store", func(ctx context.Context) error {
		if err := postgres.Migrate(ctx, pool); err != nil {
			return err
		}
		log := deliveries.New(pool)
		if err := log.Migrate(ctx); err != nil {
			return err
		}
		cursor, err = log.Cursor(ctx, scope)
		return err
	})
	var requeued int
	recovered := connected && DefaultBackoff.retry(ctx, logger, "recovering", func(ctx context.Context) error {
		requeued, err = rt.Recover(ctx)
		return err
	})
	if !recovered {
		// Asked to stop before it could start: nothing ran, so there is nothing to finish.
		return nil
	}

	logger.Info("running", "almanac", a.Name, "scope", scope, "mode", rt.Mode().String(),
		"read_only", rt.IsReadOnly(), "requeued", requeued, "listen", listener.Addr().String())
	tellManager(logger, daemon.SdNotifyReady)
	d := &Daemon{Runtime: rt, Cursor: cursor, Health: health, Logger: logger, Grace: a.graceOrDefault()}
	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		tellManager(logger, daemon.SdNotifyStopping)
		close(stopped)
	}()
	d.Run(ctx)
	<-stopped
	return nil
}
