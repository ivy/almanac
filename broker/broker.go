// Package broker is the one place webhooks arrive. It verifies a delivery,
// appends it to the shared deliveries log, and acknowledges. It knows nothing
// about workflows, so adding one never touches the broker or GitHub.
package broker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ivy/almanac/deliveries"
)

// MaxBody is the largest payload the broker reads. GitHub caps payloads at
// 25 MB.
const MaxBody = 25 * 1024 * 1024

// AppendTimeout is how long the broker waits on the log before it answers
// 503. GitHub gives up on a delivery after 10 seconds, so an answer after
// that is never seen.
const AppendTimeout = 5 * time.Second

// Source is a provider of webhooks: what it is called and how to trust what it
// sends. Another provider is another Source, served at /webhooks/<name>, with
// no change to the log or to any almanac.
type Source interface {
	Name() string
	// Accept returns the delivery a request carries, or a Refusal.
	Accept(header http.Header, body []byte) (deliveries.Delivery, error)
}

// Refusal is why a request was refused. It holds nothing from the request.
type Refusal int

// The ways a request is refused.
const (
	Unsigned Refusal = iota + 1
	BadSignature
	Malformed
)

func (r Refusal) Error() string {
	switch r {
	case Unsigned:
		return "unsigned"
	case BadSignature:
		return "bad signature"
	case Malformed:
		return "malformed"
	}
	return "refused"
}

func (r Refusal) status() int {
	if r == Malformed {
		return http.StatusBadRequest
	}
	return http.StatusUnauthorized
}

// Log is where accepted deliveries go. [deliveries.Log] is one.
type Log interface {
	// Append logs a delivery, reporting whether it was new.
	Append(ctx context.Context, d deliveries.Delivery) (bool, error)
	// Ready is nil when the log can take an append.
	Ready(ctx context.Context) error
}

// New is a broker that takes deliveries from source into log, and records
// what it does to logger. It serves:
//
//   - POST /webhooks/<source>: 202 once a delivery is logged, or was already;
//     401 unsigned or with a bad signature; 400 malformed; 413 over [MaxBody];
//     503 when the log cannot take it.
//   - GET /healthz: 200 while the process is up.
//   - GET /readyz: 200 when the log is ready, 503 when it is not.
func New(source Source, log Log, logger *slog.Logger) *Broker {
	b := &Broker{source: source, log: log, logger: logger, appendTimeout: AppendTimeout}
	b.mux.HandleFunc("POST /webhooks/"+source.Name(), b.receive)
	b.mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	b.mux.HandleFunc("GET /readyz", b.ready)
	return b
}

// Broker is an http.Handler for one source.
type Broker struct {
	source        Source
	log           Log
	logger        *slog.Logger
	appendTimeout time.Duration
	mux           http.ServeMux
}

// WithAppendTimeout replaces [AppendTimeout].
func (b *Broker) WithAppendTimeout(d time.Duration) *Broker {
	b.appendTimeout = d
	return b
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mux.ServeHTTP(w, r)
}

func (b *Broker) receive(w http.ResponseWriter, r *http.Request) {
	source := b.source.Name()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			b.logger.Warn("refused a delivery", "source", source, "refusal", "too large")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		b.logger.Warn("could not read a delivery", "source", source, "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	delivery, err := b.source.Accept(r.Header, body)
	if err != nil {
		var refusal Refusal
		if !errors.As(err, &refusal) {
			refusal = Malformed
		}
		b.logger.Warn("refused a delivery", "source", source, "refusal", refusal.Error(), "bytes", len(body))
		w.WriteHeader(refusal.status())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), b.appendTimeout)
	defer cancel()
	appended, err := b.log.Append(ctx, delivery)
	if err != nil {
		// GitHub records the failure, and a redelivery or a catch-up scan
		// brings the event back.
		b.logger.Error("could not append a delivery", "source", source, "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	b.logger.Info("accepted a delivery", "source", source, "id", delivery.DeliveryID, "kind", delivery.Kind, "appended", appended)
	w.WriteHeader(http.StatusAccepted)
}

func (b *Broker) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), b.appendTimeout)
	defer cancel()
	if err := b.log.Ready(ctx); err != nil {
		b.logger.Warn("not ready", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}
