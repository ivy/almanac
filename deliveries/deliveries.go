// Package deliveries is the log every almanac reads: each webhook the broker
// accepted, in the order it was accepted, with a durable cursor per almanac.
// The broker and the almanacs share it, and it knows nothing about workflows.
//
// Appends are serialised, so seq order is commit order. A cursor depends on
// that: with concurrent appends, a row could commit behind a cursor that had
// already passed its seq, and it would never be read.
package deliveries

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/ivy/almanac/diag"
)

// Channel is notified on every append. A notification is only a hint: one can
// be lost, and the cursor is the truth.
const Channel = "almanac_deliveries"

// appendLock serialises appends. It spells "almanac".
const appendLock int64 = 0x0061_6c6d_616e_6163

// migrationLock serialises migrations from a broker and almanacs starting at
// once. It spells "almdeliv".
const migrationLock int64 = 0x616c_6d64_656c_6976

// versionTable records which migrations have run. It is this package's own,
// so other migrations can share the database.
const versionTable = "deliveries_schema_version"

//go:embed migrations/*.sql
var migrations embed.FS

// Delivery is one webhook as the broker accepted it. Body is exactly the bytes
// that arrived, and still hostile.
type Delivery struct {
	Source     string
	DeliveryID string
	Kind       string
	Body       []byte
}

// Logged is a delivery and its place in the log.
type Logged struct {
	Seq int64
	Delivery
}

// Log is the deliveries log in PostgreSQL.
type Log struct {
	pool *pgxpool.Pool
}

// New is the log in the database pool reaches. Call [Log.Migrate] before
// using it.
func New(pool *pgxpool.Pool) *Log {
	return &Log{pool: pool}
}

// Connect reaches the database at url and brings the log's schema up to date.
// Close the log when done with it.
func Connect(ctx context.Context, url string) (*Log, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, diag.Store(err)
	}
	log := New(pool)
	if err := log.Ready(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := log.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return log, nil
}

// Close closes the log's pool.
func (l *Log) Close() {
	l.pool.Close()
}

// Migrate brings the log's schema up to date. It is safe to call from several
// processes at once.
func (l *Log) Migrate(ctx context.Context) error {
	if err := l.migrate(ctx); err != nil {
		return diag.Store(fmt.Errorf("cannot migrate the deliveries log: %w", err))
	}
	return nil
}

func (l *Log) migrate(ctx context.Context) error {
	db := stdlib.OpenDBFromPool(l.pool)
	defer func() { _ = db.Close() }()
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(migrationLock), lock.WithLockTimeout(1, 300))
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrationsDir(),
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

// Ready is nil when the database answers.
func (l *Log) Ready(ctx context.Context) error {
	if err := l.pool.Ping(ctx); err != nil {
		return diag.Store(err)
	}
	return nil
}

// Append logs a delivery, or does nothing if one with its source and ID is
// already logged. It reports whether the delivery was new.
func (l *Log) Append(ctx context.Context, d Delivery) (appended bool, err error) {
	err = pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", appendLock); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`INSERT INTO deliveries (source, delivery_id, kind, body) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (source, delivery_id) DO NOTHING`,
			d.Source, d.DeliveryID, d.Kind, d.Body)
		if err != nil {
			return err
		}
		appended = tag.RowsAffected() > 0
		if !appended {
			return nil
		}
		_, err = tx.Exec(ctx, "SELECT pg_notify($1, '')", Channel)
		return err
	})
	if err != nil {
		return false, diag.Store(err)
	}
	return appended, nil
}

// After returns up to limit deliveries with a seq greater than seq, oldest
// first.
func (l *Log) After(ctx context.Context, seq int64, limit int) ([]Logged, error) {
	rows, err := l.pool.Query(ctx,
		`SELECT seq, source, delivery_id, kind, body FROM deliveries
		 WHERE seq > $1 ORDER BY seq LIMIT $2`,
		seq, limit)
	if err != nil {
		return nil, diag.Store(err)
	}
	logged, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Logged, error) {
		var l Logged
		err := row.Scan(&l.Seq, &l.Source, &l.DeliveryID, &l.Kind, &l.Body)
		return l, err
	})
	if err != nil {
		return nil, diag.Store(err)
	}
	return logged, nil
}

// migrationsDir is the migrations, as goose reads them.
func migrationsDir() fs.FS {
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return dir
}
