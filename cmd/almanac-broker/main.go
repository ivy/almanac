// Command almanac-broker is the one webhook ingress for every almanac. Run it
// behind a public HTTPS ingress: it listens only on loopback.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/spf13/cobra"

	"github.com/ivy/almanac/broker"
	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/diag"
)

// defaultListen is the Go broker's address, beside the Rust broker's 8787.
const defaultListen = "127.0.0.1:8789"

// stopGrace is how long requests in flight get once asked to stop. systemd
// waits 30 seconds before it kills the process.
const stopGrace = 20 * time.Second

func main() {
	if err := command().Execute(); err != nil {
		var d *diag.Error
		if errors.As(err, &d) {
			fmt.Fprint(os.Stderr, diag.Render(d, diag.Human))
		} else {
			fmt.Fprintf(os.Stderr, "almanac-broker: %v\n", err)
		}
		os.Exit(1)
	}
}

func command() *cobra.Command {
	root := &cobra.Command{
		Use:   "almanac-broker",
		Short: "Receive webhooks, verify them, and append them to the deliveries log",
		Long: `almanac-broker receives GitHub webhooks on POST /webhooks/github, verifies each
signature against ALMANAC_WEBHOOK_SECRET in constant time, appends the delivery
to the deliveries log in the PostgreSQL at DATABASE_URL, and answers 202.
GET /healthz answers while it runs, and GET /readyz once the database does.

It listens on ALMANAC_BROKER_LISTEN, ` + defaultListen + ` unless set, and only on
loopback: put an HTTPS ingress in front of it.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context())
		},
	}
	root.AddCommand(replayCommand())
	return root
}

func replayCommand() *cobra.Command {
	var url string
	cmd := &cobra.Command{
		Use:   "replay DIR --url URL",
		Short: "Sign recorded deliveries and post them to a broker",
		Long: `replay signs each recorded delivery in DIR with ALMANAC_WEBHOOK_SECRET and posts
it to URL, a broker's webhook endpoint such as
http://` + defaultListen + `/webhooks/github, as GitHub would. A broker logs a
delivery once, so replaying a directory twice is harmless.

` + broker.RecordedFormat,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			secret, err := required("ALMANAC_WEBHOOK_SECRET", "the secret the receiving broker verifies signatures with")
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: 30 * time.Second}
			return broker.Replay(cmd.Context(), client, args[0], url, secret, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "the broker's webhook endpoint")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}

func serve(ctx context.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	secret, err := required("ALMANAC_WEBHOOK_SECRET", "the webhook secret set on the GitHub App")
	if err != nil {
		return err
	}
	databaseURL, err := required("DATABASE_URL", "the PostgreSQL that holds the deliveries log")
	if err != nil {
		return err
	}
	listen := os.Getenv("ALMANAC_BROKER_LISTEN")
	if listen == "" {
		listen = defaultListen
	}
	if err := loopback(listen); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	log, err := deliveries.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer log.Close()
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w; stop whatever holds it, or set ALMANAC_BROKER_LISTEN", listen, err)
	}
	server := &http.Server{
		Handler:           broker.New(broker.NewGitHub(secret), log, logger),
		ReadHeaderTimeout: 5 * time.Second,
		// GitHub waits 10 seconds for an answer, so a slower request is abandoned.
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logger.Info("listening", "address", listener.Addr().String())
	if _, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		logger.Warn("could not notify the service manager", "error", err)
	}

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	logger.Info("stopping")
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
	defer cancel()
	return server.Shutdown(shutdown)
}

// loopback refuses an address that is not a loopback IP and port.
func loopback(listen string) error {
	address, err := netip.ParseAddrPort(listen)
	if err != nil {
		return fmt.Errorf("ALMANAC_BROKER_LISTEN=%s is not an IP address and port; set it to one such as %s", listen, defaultListen)
	}
	if !address.Addr().IsLoopback() {
		return fmt.Errorf("ALMANAC_BROKER_LISTEN=%s is not a loopback address, and the broker must sit behind an HTTPS ingress; set it to one such as %s", listen, defaultListen)
	}
	return nil
}

func required(name, purpose string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", diag.MissingEnv(name, purpose)
	}
	return value, nil
}
