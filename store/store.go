// Package store is everything an almanac must remember across a restart: the
// deliveries it has seen, work waiting for its key, the runs it started and
// what each recorded, the effects runs decided, and what was done to each key.
//
// Store is the interface the runtime uses. store/memory and store/postgres
// implement it, and both pass the conformance suite in store/storetest, so
// the runtime cannot tell them apart.
//
// Every row belongs to one almanac's scope. Almanacs share a database, and each
// sees every delivery, so a store handle is opened for one scope (see
// effect.Mode.Scope, which keeps a dry run's rows apart from a live run's).
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ivy/almanac/config"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/workflow"
)

// Store is the runtime's durable state. Every error is a *diag.Error with the
// code almanac::store::failed.
type Store interface {
	// Admit marks a delivery seen and queues its work, as one atomic step, so
	// a failure leaves nothing behind and a redelivery is accepted. A delivery
	// already seen is Duplicate, whatever its admission.
	Admit(ctx context.Context, source, deliveryID string, admission Admission) (Ingest, error)

	// Claim starts the next claimable event: live events before scanned ones,
	// then oldest first. Only workflows in the request's budgets are
	// considered. A key with a running run, or with effects of its last run
	// still undelivered, is not claimable.
	Claim(ctx context.Context, request ClaimRequest) (Claim, error)

	// Remember notes something a run did to its key.
	Remember(ctx context.Context, workflow, key string, trace Trace) error

	// Traces is what was done to a key after since, oldest first.
	Traces(ctx context.Context, workflow, key string, since time.Time) ([]Trace, error)

	// Note records an effect no run produced, such as a budget alert. A
	// repeated key changes nothing. In a dry run it is stored as WouldApply.
	Note(ctx context.Context, key string, effect json.RawMessage, mode effect.Mode) error

	// Release ends a run, links what it learned, and records its effects, as
	// one atomic step. Effects are keyed "{runID}:{index}". In live mode they
	// wait for delivery; in a dry run they are stored as WouldApply and never
	// delivered. A pending event for the run's key becomes claimable.
	Release(ctx context.Context, runID string, links []workflow.Subject, effects []json.RawMessage, mode effect.Mode) error

	// Undelivered is the effects waiting to be applied, oldest first. It never
	// returns an effect a dry run recorded.
	Undelivered(ctx context.Context) ([]Entry, error)

	// Outbox is a run's recorded effects, in order, whatever their status.
	Outbox(ctx context.Context, runID string) ([]Entry, error)

	// Delivered marks an effect applied. Marking it again changes nothing.
	Delivered(ctx context.Context, key string) error

	// DeliveryFailed counts a failed attempt to apply an effect. At giveUp
	// attempts the effect is Failed and stops being retried.
	DeliveryFailed(ctx context.Context, key, reason string, giveUp int) error

	// Recover requeues the runs a crash left running, unless a newer event
	// for the key is already waiting. It returns how many runs it ended.
	Recover(ctx context.Context) (int, error)

	// Run is the run with an ID, and false if this scope has none.
	Run(ctx context.Context, runID string) (Run, bool, error)

	// RunsSince is every run this scope started at or after since, whatever
	// became of it, oldest first. It is how a dry run's decisions are read
	// back across keys, with Outbox and Recorded for each.
	RunsSince(ctx context.Context, since time.Time) ([]Run, error)

	// Recorded is the value a run recorded under name for the occurrence-th
	// time, and false if it recorded none.
	Recorded(ctx context.Context, runID, name string, occurrence int) (json.RawMessage, bool, error)

	// Record keeps what a run read under name for the occurrence-th time,
	// replacing any value already there. The run must exist.
	Record(ctx context.Context, runID, name string, occurrence int, value json.RawMessage) error

	// RunsFor is the IDs of the runs linked to a subject, oldest first, each
	// once.
	RunsFor(ctx context.Context, subject workflow.Subject) ([]string, error)
}

// Ingest is what became of a delivery.
type Ingest string

const (
	// Duplicate is a delivery already seen.
	Duplicate Ingest = "duplicate"
	// Echo is Almanac's own action coming back. It never starts a run.
	Echo Ingest = "echo"
	// ChainStopped is an event too far down a chain of Almanac's own effects.
	ChainStopped Ingest = "chain-stopped"
	// Unrouted is a delivery no workflow handles.
	Unrouted Ingest = "unrouted"
	// Ignored is an event a workflow handles and did not want.
	Ignored Ingest = "ignored"
	// Rejected is a delivery that could not be read.
	Rejected Ingest = "rejected"
	// Queued is an event waiting for its key.
	Queued Ingest = "queued"
	// Coalesced is an event that replaced one already waiting for its key.
	Coalesced Ingest = "coalesced"
	// MarkedDirty is an event for a key with a run under way. The key runs
	// once more when that run ends.
	MarkedDirty Ingest = "marked-dirty"
)

// Admission is what to do with a delivery, decided before it reaches the
// store. Make one with Queue or Drop.
type Admission struct {
	// Queue is the work to queue. When nil, the delivery is remembered and
	// Drop is reported.
	Queue *Event
	// Drop is what a delivery that queues nothing became.
	Drop Ingest
}

// Queue is an admission that queues an event.
func Queue(e Event) Admission { return Admission{Queue: &e} }

// Drop is an admission that remembers the delivery and reports ingest.
func Drop(ingest Ingest) Admission { return Admission{Drop: ingest} }

// Event is work waiting for its key to be free. At most one waits per key:
// a later event replaces it, keeping the earlier lane if that was sooner and
// the deeper chain depth.
type Event struct {
	Workflow string
	Key      string
	Payload  json.RawMessage
	Subjects []workflow.Subject
	Origin   Origin
	// Depth is how many of Almanac's own effects led here.
	Depth int
}

// Origin is where an event came from. It is independent of whether effects
// are live.
type Origin int

const (
	Webhook Origin = iota
	Scan
	Simulated
)

var origins = map[Origin]string{Webhook: "webhook", Scan: "scan", Simulated: "simulated"}

// String is the origin's stored name, such as "webhook".
func (o Origin) String() string {
	if name, ok := origins[o]; ok {
		return name
	}
	return fmt.Sprintf("Origin(%d)", int(o))
}

// ParseOrigin reads an origin's stored name, and refuses any other.
func ParseOrigin(name string) (Origin, error) {
	for o, n := range origins {
		if n == name {
			return o, nil
		}
	}
	return 0, fmt.Errorf("unknown origin %q", name)
}

// Lane orders claims: a lower lane is claimed first, so a backfill can never
// delay a live event.
func (o Origin) Lane() int {
	if o == Scan {
		return 1
	}
	return 0
}

// Run is a claimed event, under way or finished.
type Run struct {
	ID       string
	Workflow string
	Key      string
	Payload  json.RawMessage
	Origin   Origin
	Depth    int
}

// Trace is something Almanac did to a key. A webhook does not say what caused
// it, so an event that follows a trace inherits its depth.
type Trace struct {
	Kind  effect.Kind
	Depth int
	At    time.Time
}

// Budget is how many runs a workflow may start in a window.
type Budget struct {
	Workflow string
	Limit    int
}

// ClaimRequest is what a claim may start, and when it is asked.
type ClaimRequest struct {
	Budgets []Budget
	// Overall caps the runs all workflows together may start in a window.
	Overall config.Cap
	Window  time.Duration
	Now     time.Time
}

// Claim is what a claim found. The zero Claim is idle: nothing to start.
type Claim struct {
	// Run is the run started, or nil.
	Run *Run
	// Tripped is a budget that was reached with work waiting, or nil. It is
	// reported once per window, so a person is asked once.
	Tripped *Tripped
}

// Idle is whether the claim found nothing to do.
func (c Claim) Idle() bool { return c.Run == nil && c.Tripped == nil }

// Tripped is a budget reached with work waiting.
type Tripped struct {
	// Workflow is the workflow whose budget was reached, and empty for the
	// overall budget.
	Workflow string
	// Held is how many keys are waiting under the budget.
	Held int
}

// Entry is an effect in the outbox.
type Entry struct {
	// Key is the idempotency key handed to providers, stable across retries.
	Key string
	// RunID is the run that produced the effect, and empty for one no run
	// produced, such as a budget alert.
	RunID    string
	Effect   json.RawMessage
	Status   Status
	Attempts int
}

// Status is where an outbox entry is in delivery.
type Status string

const (
	// Pending waits to be applied.
	Pending Status = "pending"
	// Applied was applied.
	Applied Status = "applied"
	// WouldApply was recorded by a dry run, and is never delivered.
	WouldApply Status = "would-apply"
	// Failed gave up after repeated failures.
	Failed Status = "failed"
)

// ParseStatus reads a stored status, and refuses any other.
func ParseStatus(text string) (Status, error) {
	switch s := Status(text); s {
	case Pending, Applied, WouldApply, Failed:
		return s, nil
	}
	return "", fmt.Errorf("unknown outbox status %q", text)
}

// StatusIn is the status effects are stored with in mode: Pending when live,
// and WouldApply in a dry run, so delivery can never select them.
func StatusIn(mode effect.Mode) Status {
	if mode == effect.Live {
		return Pending
	}
	return WouldApply
}
