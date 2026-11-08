// Package postgres is a store.Store in PostgreSQL, shared by the almanacs of
// one deployment and separated by almanac.
package postgres

//go:generate sqlc generate

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/postgres/internal/queries"
	"github.com/ivy/almanac/workflow"
)

// migrationLock serialises migrations from almanacs starting at once. It
// spells "almstore".
const migrationLock int64 = 0x616c_6d73_746f_7265

// versionTable records which migrations have run. It is the store's own, so
// the deliveries log can share the database.
const versionTable = "store_schema_version"

// overall is the tripped row for the budget across all workflows.
const overall = "*"

//go:embed migrations/*.sql
var migrations embed.FS

// Store is one almanac's state in PostgreSQL.
type Store struct {
	pool    *pgxpool.Pool
	q       *queries.Queries
	almanac string
}

var _ store.Store = (*Store)(nil)

// New is almanac's state in the database pool reaches. The schema must
// already be up to date: see Migrate. Several stores may share one pool.
func New(pool *pgxpool.Pool, almanac string) *Store {
	return &Store{pool: pool, q: queries.New(pool), almanac: almanac}
}

// Connect reaches the database at url, brings the schema up to date, and
// returns almanac's state in it. Close the store when done with it.
func Connect(ctx context.Context, url, almanac string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, diag.Store(err)
	}
	s := New(pool, almanac)
	if err := s.Ready(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the store's pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Ready is nil when the database answers.
func (s *Store) Ready(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return diag.Store(err)
	}
	return nil
}

// Migrate brings the store's schema up to date. It is safe to call from
// several processes at once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if err := migrate(ctx, pool); err != nil {
		return diag.Store(fmt.Errorf("cannot migrate the store: %w", err))
	}
	return nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(migrationLock), lock.WithLockTimeout(1, 300))
	if err != nil {
		return err
	}
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, dir,
		goose.WithTableName(versionTable),
		goose.WithSessionLocker(locker),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

// inTx runs f in a transaction, committed only if f returns nil.
func (s *Store) inTx(ctx context.Context, f func(q *queries.Queries) error) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(s.q.WithTx(tx))
	})
	if err != nil {
		return diag.Store(err)
	}
	return nil
}

// Admit implements store.Store.
func (s *Store) Admit(ctx context.Context, source, deliveryID string, admission store.Admission) (store.Ingest, error) {
	var ingest store.Ingest
	err := s.inTx(ctx, func(q *queries.Queries) error {
		fresh, err := q.MarkSeen(ctx, queries.MarkSeenParams{Almanac: s.almanac, Source: source, DeliveryID: deliveryID})
		if err != nil {
			return err
		}
		if fresh == 0 {
			ingest = store.Duplicate
			return nil
		}
		event := admission.Queue
		if event == nil {
			ingest = admission.Drop
			return nil
		}
		ingest, err = s.enqueue(ctx, q, *event)
		return err
	})
	if err != nil {
		return "", err
	}
	return ingest, nil
}

func (s *Store) enqueue(ctx context.Context, q *queries.Queries, event store.Event) (store.Ingest, error) {
	payload, err := store.Storable(event.Payload)
	if err != nil {
		return "", err
	}
	subjects, err := texts(event.Subjects)
	if err != nil {
		return "", err
	}
	inserted, err := q.Enqueue(ctx, queries.EnqueueParams{
		Almanac:  s.almanac,
		Workflow: event.Workflow,
		Key:      event.Key,
		Payload:  payload,
		Subjects: subjects,
		Lane:     int16(event.Origin.Lane()),
		Origin:   event.Origin.String(),
		Depth:    int32(event.Depth),
	})
	if err != nil {
		return "", err
	}
	running, err := q.Running(ctx, queries.RunningParams{Almanac: s.almanac, Workflow: event.Workflow, Key: event.Key})
	switch {
	case err != nil:
		return "", err
	case running:
		return store.MarkedDirty, nil
	case inserted:
		return store.Queued, nil
	default:
		return store.Coalesced, nil
	}
}

// Claim implements store.Store.
func (s *Store) Claim(ctx context.Context, request store.ClaimRequest) (store.Claim, error) {
	var claim store.Claim
	err := s.inTx(ctx, func(q *queries.Queries) error {
		var err error
		claim, err = s.claim(ctx, q, request)
		return err
	})
	if err != nil {
		return store.Claim{}, err
	}
	return claim, nil
}

func (s *Store) claim(ctx context.Context, q *queries.Queries, request store.ClaimRequest) (store.Claim, error) {
	if err := q.LockClaims(ctx, s.almanac); err != nil {
		return store.Claim{}, err
	}
	since := request.Now.Add(-request.Window)
	workflows := make([]string, len(request.Budgets))
	for i, b := range request.Budgets {
		workflows[i] = b.Workflow
	}
	counts, err := q.StartedSince(ctx, queries.StartedSinceParams{Almanac: s.almanac, Since: since})
	if err != nil {
		return store.Claim{}, err
	}
	waiting, err := q.Held(ctx, queries.HeldParams{Almanac: s.almanac, Workflows: workflows})
	if err != nil {
		return store.Claim{}, err
	}
	started, total := map[string]int{}, 0
	for _, c := range counts {
		started[c.Workflow] = int(c.Started)
		total += int(c.Started)
	}
	held, waitingTotal := map[string]int{}, 0
	for _, w := range waiting {
		held[w.Workflow] = int(w.Held)
		waitingTotal += int(w.Held)
	}
	if limit, capped := request.Overall.Value(); capped && total >= limit {
		if waitingTotal == 0 {
			return store.Claim{}, nil
		}
		reported, err := s.trip(ctx, q, overall, request.Now, since)
		if err != nil || !reported {
			return store.Claim{}, err
		}
		return store.Claim{Tripped: &store.Tripped{Held: waitingTotal}}, nil
	}
	var open []string
	for _, b := range request.Budgets {
		if started[b.Workflow] < b.Limit {
			open = append(open, b.Workflow)
			continue
		}
		if held[b.Workflow] == 0 {
			continue
		}
		reported, err := s.trip(ctx, q, b.Workflow, request.Now, since)
		if err != nil {
			return store.Claim{}, err
		}
		if reported {
			return store.Claim{Tripped: &store.Tripped{Workflow: b.Workflow, Held: held[b.Workflow]}}, nil
		}
	}
	next, err := q.NextClaimable(ctx, queries.NextClaimableParams{Almanac: s.almanac, Workflows: open})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Claim{}, nil
	}
	if err != nil {
		return store.Claim{}, err
	}
	origin, err := store.ParseOrigin(next.Origin)
	if err != nil {
		return store.Claim{}, err
	}
	id, err := q.StartRun(ctx, queries.StartRunParams{
		Almanac:   s.almanac,
		Workflow:  next.Workflow,
		Key:       next.Key,
		Payload:   next.Payload,
		Origin:    next.Origin,
		Depth:     next.Depth,
		StartedAt: request.Now,
	})
	if err != nil {
		return store.Claim{}, err
	}
	if err := q.Dequeue(ctx, queries.DequeueParams{Almanac: s.almanac, Workflow: next.Workflow, Key: next.Key}); err != nil {
		return store.Claim{}, err
	}
	if err := q.Link(ctx, queries.LinkParams{RunID: id, Subjects: next.Subjects}); err != nil {
		return store.Claim{}, err
	}
	return store.Claim{Run: &store.Run{
		ID:       id,
		Workflow: next.Workflow,
		Key:      next.Key,
		Payload:  next.Payload,
		Origin:   origin,
		Depth:    int(next.Depth),
	}}, nil
}

// trip records a trip of scope's budget, unless one was reported in this
// window, and says whether it did.
func (s *Store) trip(ctx context.Context, q *queries.Queries, scope string, now, since time.Time) (bool, error) {
	n, err := q.Trip(ctx, queries.TripParams{Almanac: s.almanac, Workflow: scope, Now: now, Since: since})
	return n > 0, err
}

// Remember implements store.Store.
func (s *Store) Remember(ctx context.Context, workflow, key string, trace store.Trace) error {
	err := s.q.Remember(ctx, queries.RememberParams{
		Almanac:  s.almanac,
		Workflow: workflow,
		Key:      key,
		Kind:     trace.Kind.String(),
		Depth:    int32(trace.Depth),
		At:       trace.At,
	})
	if err != nil {
		return diag.Store(err)
	}
	return nil
}

// Traces implements store.Store.
func (s *Store) Traces(ctx context.Context, workflow, key string, since time.Time) ([]store.Trace, error) {
	rows, err := s.q.Traces(ctx, queries.TracesParams{Almanac: s.almanac, Workflow: workflow, Key: key, Since: since})
	if err != nil {
		return nil, diag.Store(err)
	}
	traces := make([]store.Trace, len(rows))
	for i, r := range rows {
		if err := traces[i].Kind.UnmarshalText([]byte(r.Kind)); err != nil {
			return nil, diag.Store(err)
		}
		traces[i].Depth = int(r.Depth)
		traces[i].At = r.At
	}
	return traces, nil
}

// Note implements store.Store.
func (s *Store) Note(ctx context.Context, key string, value json.RawMessage, mode effect.Mode) error {
	value, err := store.Storable(value)
	if err == nil {
		err = s.q.Note(ctx, queries.NoteParams{
			Key:     key,
			Almanac: s.almanac,
			Effect:  value,
			Status:  string(store.StatusIn(mode)),
		})
	}
	if err != nil {
		return diag.Store(err)
	}
	return nil
}

// Release implements store.Store.
func (s *Store) Release(ctx context.Context, runID string, links []workflow.Subject, effects []json.RawMessage, mode effect.Mode) error {
	subjects, err := texts(links)
	if err != nil {
		return diag.Store(err)
	}
	return s.inTx(ctx, func(q *queries.Queries) error {
		known, err := q.FinishRun(ctx, queries.FinishRunParams{RunID: runID, Almanac: s.almanac})
		if err != nil {
			return err
		}
		if !known {
			return fmt.Errorf("no run %q to release", runID)
		}
		if err := q.Link(ctx, queries.LinkParams{RunID: runID, Subjects: subjects}); err != nil {
			return err
		}
		for i, e := range effects {
			value, err := store.Storable(e)
			if err != nil {
				return err
			}
			err = q.AddEffect(ctx, queries.AddEffectParams{
				Key:     fmt.Sprintf("%s:%d", runID, i),
				Almanac: s.almanac,
				RunID:   runID,
				Seq:     int32(i),
				Effect:  value,
				Status:  string(store.StatusIn(mode)),
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// Undelivered implements store.Store.
func (s *Store) Undelivered(ctx context.Context) ([]store.Entry, error) {
	rows, err := s.q.Undelivered(ctx, s.almanac)
	if err != nil {
		return nil, diag.Store(err)
	}
	entries := make([]store.Entry, len(rows))
	for i, r := range rows {
		if entries[i], err = entry(queries.OutboxRow(r)); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// Outbox implements store.Store.
func (s *Store) Outbox(ctx context.Context, runID string) ([]store.Entry, error) {
	rows, err := s.q.Outbox(ctx, queries.OutboxParams{Almanac: s.almanac, RunID: runID})
	if err != nil {
		return nil, diag.Store(err)
	}
	entries := make([]store.Entry, len(rows))
	for i, r := range rows {
		if entries[i], err = entry(r); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func entry(r queries.OutboxRow) (store.Entry, error) {
	status, err := store.ParseStatus(r.Status)
	if err != nil {
		return store.Entry{}, diag.Store(err)
	}
	return store.Entry{
		Key:      r.Key,
		RunID:    r.RunID.String,
		Effect:   r.Effect,
		Status:   status,
		Attempts: int(r.Attempts),
	}, nil
}

// Delivered implements store.Store.
func (s *Store) Delivered(ctx context.Context, key string) error {
	if err := s.q.Delivered(ctx, queries.DeliveredParams{Key: key, Almanac: s.almanac}); err != nil {
		return diag.Store(err)
	}
	return nil
}

// DeliveryFailed implements store.Store. The reason is kept with the entry;
// a U+0000 in it, which PostgreSQL's text refuses, becomes U+FFFD.
func (s *Store) DeliveryFailed(ctx context.Context, key, reason string, giveUp int) error {
	err := s.q.DeliveryFailed(ctx, queries.DeliveryFailedParams{
		Reason:  strings.ReplaceAll(reason, "\x00", "�"),
		GiveUp:  int32(giveUp),
		Key:     key,
		Almanac: s.almanac,
	})
	if err != nil {
		return diag.Store(err)
	}
	return nil
}

// Recover implements store.Store.
func (s *Store) Recover(ctx context.Context) (int, error) {
	var interrupted int
	err := s.inTx(ctx, func(q *queries.Queries) error {
		runs, err := q.Interrupt(ctx, s.almanac)
		if err != nil {
			return err
		}
		interrupted = len(runs)
		for _, run := range runs {
			origin, err := store.ParseOrigin(run.Origin)
			if err != nil {
				return err
			}
			err = q.Requeue(ctx, queries.RequeueParams{
				Almanac:  s.almanac,
				Workflow: run.Workflow,
				Key:      run.Key,
				Payload:  run.Payload,
				Lane:     int16(origin.Lane()),
				Origin:   run.Origin,
				Depth:    run.Depth,
				RunID:    run.ID,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return interrupted, nil
}

// Run implements store.Store.
func (s *Store) Run(ctx context.Context, runID string) (store.Run, bool, error) {
	r, err := s.q.Run(ctx, queries.RunParams{RunID: runID, Almanac: s.almanac})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Run{}, false, nil
	}
	if err != nil {
		return store.Run{}, false, diag.Store(err)
	}
	origin, err := store.ParseOrigin(r.Origin)
	if err != nil {
		return store.Run{}, false, diag.Store(err)
	}
	return store.Run{
		ID:       r.ID,
		Workflow: r.Workflow,
		Key:      r.Key,
		Payload:  r.Payload,
		Origin:   origin,
		Depth:    int(r.Depth),
	}, true, nil
}

// Recorded implements store.Store.
func (s *Store) Recorded(ctx context.Context, runID, name string, occurrence int) (json.RawMessage, bool, error) {
	value, err := s.q.Recorded(ctx, queries.RecordedParams{RunID: runID, Name: name, Occurrence: int32(occurrence)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, diag.Store(err)
	}
	return value, true, nil
}

// Record implements store.Store.
func (s *Store) Record(ctx context.Context, runID, name string, occurrence int, value json.RawMessage) error {
	value, err := store.Storable(value)
	if err == nil {
		err = s.q.Record(ctx, queries.RecordParams{RunID: runID, Name: name, Occurrence: int32(occurrence), Value: value})
	}
	if err != nil {
		return diag.Store(err)
	}
	return nil
}

// RunsFor implements store.Store.
func (s *Store) RunsFor(ctx context.Context, subject workflow.Subject) ([]string, error) {
	ids, err := s.q.RunsFor(ctx, queries.RunsForParams{Subject: subject.String(), Almanac: s.almanac})
	if err != nil {
		return nil, diag.Store(err)
	}
	return ids, nil
}

// texts is subjects as the store keys them.
func texts(subjects []workflow.Subject) ([]string, error) {
	out := make([]string, len(subjects))
	for i, s := range subjects {
		text, err := s.MarshalText()
		if err != nil {
			return nil, err
		}
		out[i] = string(text)
	}
	return out, nil
}
