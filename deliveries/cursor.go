package deliveries

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ivy/almanac/diag"
)

// DefaultPoll is how often a follower reads the log with no notification, so
// it is the longest a lost notification can delay a delivery.
const DefaultPoll = 30 * time.Second

// batch is how many deliveries a cursor reads at a time.
const batch = 100

// Handler takes one delivery from the log. Returning an error stops the
// cursor before it passes the delivery, so the delivery is read again later.
// A handler must not return an error for a delivery it can never take, or the
// cursor stops there for good.
type Handler func(ctx context.Context, d Logged) error

// Cursor is one almanac's place in the log, kept in the database so that an
// almanac that was down carries on where it stopped. Delivery is
// at-least-once: a crash between handling a delivery and saving the cursor
// hands it over again, so a handler must take a repeat harmlessly.
type Cursor struct {
	log     *Log
	almanac string

	// Poll is how often Follow reads the log when no notification wakes it.
	Poll time.Duration
}

// Cursor opens almanac's cursor. A new almanac starts at the end of the log:
// history is backfill's job, and reading it here would spend budgets on old
// events.
func (l *Log) Cursor(ctx context.Context, almanac string) (*Cursor, error) {
	_, err := l.pool.Exec(ctx,
		`INSERT INTO cursors (almanac, seq)
		 SELECT $1, coalesce(max(seq), 0) FROM deliveries
		 ON CONFLICT (almanac) DO NOTHING`,
		almanac)
	if err != nil {
		return nil, diag.Store(err)
	}
	return &Cursor{log: l, almanac: almanac, Poll: DefaultPoll}, nil
}

// Seq is the seq of the last delivery this cursor has passed.
func (c *Cursor) Seq(ctx context.Context) (int64, error) {
	var seq int64
	err := c.log.pool.QueryRow(ctx, "SELECT seq FROM cursors WHERE almanac = $1", c.almanac).Scan(&seq)
	if err != nil {
		return 0, diag.Store(err)
	}
	return seq, nil
}

func (c *Cursor) advance(ctx context.Context, seq int64) error {
	_, err := c.log.pool.Exec(ctx, "UPDATE cursors SET seq = $2 WHERE almanac = $1", c.almanac, seq)
	if err != nil {
		return diag.Store(err)
	}
	return nil
}

// CatchUp hands every delivery after the cursor to handle, oldest first, and
// moves the cursor past each one handled. It returns how many deliveries it
// passed.
//
// The broker refuses a body that is not JSON, but the log is not the broker's
// alone. Such a body is passed without reaching handle, or it would stop the
// cursor for good.
func (c *Cursor) CatchUp(ctx context.Context, handle Handler) (int, error) {
	passed := 0
	for {
		seq, err := c.Seq(ctx)
		if err != nil {
			return passed, err
		}
		logged, err := c.log.After(ctx, seq, batch)
		if err != nil || len(logged) == 0 {
			return passed, err
		}
		for _, d := range logged {
			if json.Valid(d.Body) {
				if err := handle(ctx, d); err != nil {
					return passed, err
				}
			} else {
				slog.WarnContext(ctx, "passed a delivery that is not JSON", "almanac", c.almanac, "seq", d.Seq)
			}
			if err := c.advance(ctx, d.Seq); err != nil {
				return passed, err
			}
			passed++
		}
	}
}

// Follow catches up, calls settle, and waits, until ctx is done. A
// notification on [Channel] wakes it at once, and it reads the log every
// [Cursor.Poll] whether woken or not, so a lost notification only delays a
// delivery. Settle may be nil; an almanac uses it to run what it took in, and
// to pick up work a budget held back.
//
// Follow returns nil once ctx is done, and the first error from the log,
// handle or settle otherwise.
func (c *Cursor) Follow(ctx context.Context, handle Handler, settle func(context.Context) error) error {
	w := &waker{log: c.log}
	defer w.close()
	// Listen before the first read, so nothing appended after it goes unnoticed.
	w.listen(ctx)
	for {
		if _, err := c.CatchUp(ctx, handle); err != nil {
			return stopped(ctx, err)
		}
		if settle != nil {
			if err := settle(ctx); err != nil {
				return stopped(ctx, err)
			}
		}
		if err := w.wait(ctx, c.Poll); err != nil {
			return stopped(ctx, err)
		}
	}
}

// stopped is nil when err only says that ctx is done.
func stopped(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// waker listens on Channel on a connection of its own, outside the pool, so no
// other query ever runs on a listening connection.
type waker struct {
	log  *Log
	conn *pgx.Conn
}

// listen opens the listening connection. If it cannot, the follower polls
// until the next attempt.
func (w *waker) listen(ctx context.Context) {
	conn, err := pgx.ConnectConfig(ctx, w.log.pool.Config().ConnConfig)
	if err == nil {
		_, err = conn.Exec(ctx, "LISTEN "+Channel)
	}
	if err != nil {
		slog.WarnContext(ctx, "cannot listen for deliveries, so polling for them", "error", err)
		if conn != nil {
			_ = conn.Close(context.WithoutCancel(ctx))
		}
		return
	}
	w.conn = conn
}

// wait returns on a notification, after poll, or with ctx's error once it is
// done. With no listening connection it first tries to open one, and returns
// at once if it can, so the caller reads whatever was appended meanwhile.
func (w *waker) wait(ctx context.Context, poll time.Duration) error {
	if w.conn == nil {
		w.listen(ctx)
		if w.conn != nil {
			return nil
		}
	}
	waiting, cancel := context.WithTimeout(ctx, poll)
	defer cancel()
	if w.conn == nil {
		<-waiting.Done()
		return ctx.Err()
	}
	_, err := w.conn.WaitForNotification(waiting)
	switch {
	case err == nil, ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return nil
	default:
		// The connection broke. Read the log now, and listen again next time.
		slog.WarnContext(ctx, "lost the connection listening for deliveries", "error", err)
		w.close()
		return nil
	}
}

func (w *waker) close() {
	if w.conn != nil {
		_ = w.conn.Close(context.Background())
		w.conn = nil
	}
}
