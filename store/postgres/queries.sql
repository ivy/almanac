-- name: MarkSeen :execrows
INSERT INTO seen (almanac, source, delivery_id) VALUES (@almanac, @source, @delivery_id)
ON CONFLICT DO NOTHING;

-- name: Enqueue :one
-- Queues an event, or replaces the one waiting for its key. The replacement keeps the sooner
-- lane and the deeper chain depth. Returns whether a row was inserted rather than replaced.
INSERT INTO pending (almanac, workflow, key, payload, subjects, lane, origin, depth)
VALUES (@almanac, @workflow, @key, @payload, @subjects, @lane, @origin, @depth)
ON CONFLICT (almanac, workflow, key)
DO UPDATE SET payload = EXCLUDED.payload, subjects = EXCLUDED.subjects,
              lane = LEAST(pending.lane, EXCLUDED.lane),
              origin = EXCLUDED.origin,
              depth = GREATEST(pending.depth, EXCLUDED.depth)
RETURNING (xmax = 0)::boolean AS inserted;

-- name: Running :one
SELECT EXISTS (
    SELECT 1 FROM runs
    WHERE almanac = @almanac AND workflow = @workflow AND key = @key AND status = 'running'
)::boolean AS running;

-- name: LockClaims :exec
-- Claims are rare and short, so one at a time keeps budget counts exact.
SELECT pg_advisory_xact_lock(hashtext(@almanac));

-- name: StartedSince :many
SELECT workflow, count(*)::integer AS started FROM runs
WHERE almanac = @almanac AND started_at > @since
GROUP BY workflow;

-- name: Held :many
-- How many of each workflow's keys could start a run now: none is running, and the last run's
-- effects have all been delivered.
SELECT p.workflow, count(*)::integer AS held FROM pending p
WHERE p.almanac = @almanac AND p.workflow = ANY(@workflows::text[])
  AND NOT EXISTS (
    SELECT 1 FROM runs r
    WHERE r.almanac = p.almanac AND r.workflow = p.workflow
      AND r.key = p.key AND r.status = 'running'
  )
  AND NOT EXISTS (
    SELECT 1 FROM outbox o JOIN runs r ON r.id = o.run_id
    WHERE o.status = 'pending' AND r.almanac = p.almanac
      AND r.workflow = p.workflow AND r.key = p.key
  )
GROUP BY p.workflow;

-- name: Trip :execrows
-- Records a trip unless one was reported since the window opened.
INSERT INTO tripped (almanac, workflow, tripped_at) VALUES (@almanac, @workflow, @now)
ON CONFLICT (almanac, workflow) DO UPDATE SET tripped_at = EXCLUDED.tripped_at
WHERE tripped.tripped_at <= @since;

-- name: NextClaimable :one
-- The next event to start: live before scanned, then oldest first.
SELECT p.workflow, p.key, p.payload, p.subjects, p.origin, p.depth
FROM pending p
WHERE p.almanac = @almanac
  AND p.workflow = ANY(@workflows::text[])
  AND NOT EXISTS (
    SELECT 1 FROM runs r
    WHERE r.almanac = p.almanac AND r.workflow = p.workflow
      AND r.key = p.key AND r.status = 'running'
  )
  AND NOT EXISTS (
    SELECT 1 FROM outbox o JOIN runs r ON r.id = o.run_id
    WHERE o.status = 'pending' AND r.almanac = p.almanac
      AND r.workflow = p.workflow AND r.key = p.key
  )
ORDER BY p.lane, p.seq
LIMIT 1;

-- name: StartRun :one
INSERT INTO runs (almanac, workflow, key, payload, origin, depth, status, started_at)
VALUES (@almanac, @workflow, @key, @payload, @origin, @depth, 'running', @started_at)
RETURNING id;

-- name: Dequeue :exec
DELETE FROM pending WHERE almanac = @almanac AND workflow = @workflow AND key = @key;

-- name: Link :exec
INSERT INTO run_subjects (run_id, subject)
SELECT @run_id, subject FROM unnest(@subjects::text[]) AS subject
ON CONFLICT DO NOTHING;

-- name: FinishRun :one
-- Ends a run if it is running, and says whether this almanac has the run at all.
WITH finished AS (
    UPDATE runs SET status = 'finished', finished_at = now()
    WHERE id = @run_id AND almanac = @almanac AND status = 'running'
    RETURNING id
)
SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = @run_id AND r.almanac = @almanac)::boolean AS known;

-- name: AddEffect :exec
INSERT INTO outbox (key, almanac, run_id, seq, effect, status)
VALUES (@key, @almanac, @run_id::text, @seq, @effect, @status);

-- name: Note :exec
INSERT INTO outbox (key, almanac, seq, effect, status)
VALUES (@key, @almanac, 0, @effect, @status)
ON CONFLICT (key) DO NOTHING;

-- name: Undelivered :many
SELECT key, run_id, effect, status, attempts FROM outbox
WHERE almanac = @almanac AND status = 'pending'
ORDER BY created_at, run_id, seq;

-- name: Outbox :many
SELECT key, run_id, effect, status, attempts FROM outbox
WHERE almanac = @almanac AND run_id = @run_id::text
ORDER BY seq;

-- name: Delivered :exec
UPDATE outbox SET status = 'applied', applied_at = now()
WHERE key = @key AND almanac = @almanac;

-- name: DeliveryFailed :exec
UPDATE outbox
SET attempts = attempts + 1,
    last_error = @reason::text,
    status = CASE WHEN attempts + 1 >= @give_up::integer THEN 'failed' ELSE status END
WHERE key = @key AND almanac = @almanac;

-- name: Interrupt :many
-- Ends the runs a crash left running, in the order they started.
WITH interrupted AS (
    UPDATE runs SET status = 'interrupted', finished_at = now()
    WHERE almanac = @almanac AND status = 'running'
    RETURNING seq, id, workflow, key, payload, origin, depth
)
SELECT id, workflow, key, payload, origin, depth FROM interrupted ORDER BY seq;

-- name: Requeue :exec
-- Queues an interrupted run's event again, with the subjects it was linked to, unless a newer
-- event already waits for its key.
INSERT INTO pending (almanac, workflow, key, payload, subjects, lane, origin, depth)
SELECT @almanac, @workflow, @key, @payload,
       coalesce(array_agg(s.subject ORDER BY s.subject), '{}')::text[], @lane, @origin, @depth
FROM run_subjects s WHERE s.run_id = @run_id
ON CONFLICT (almanac, workflow, key) DO NOTHING;

-- name: Run :one
SELECT id, workflow, key, payload, origin, depth FROM runs
WHERE id = @run_id AND almanac = @almanac;

-- name: RunsSince :many
SELECT id, workflow, key, payload, origin, depth FROM runs
WHERE almanac = @almanac AND started_at >= @since
ORDER BY seq;

-- name: Recorded :one
SELECT value FROM run_log WHERE run_id = @run_id AND name = @name AND occurrence = @occurrence;

-- name: Record :exec
INSERT INTO run_log (run_id, name, occurrence, value) VALUES (@run_id, @name, @occurrence, @value)
ON CONFLICT (run_id, name, occurrence) DO UPDATE SET value = EXCLUDED.value;

-- name: Remember :exec
INSERT INTO ledger (almanac, workflow, key, kind, depth, at)
VALUES (@almanac, @workflow, @key, @kind, @depth, @at);

-- name: Traces :many
SELECT kind, depth, at FROM ledger
WHERE almanac = @almanac AND workflow = @workflow AND key = @key AND at > @since
ORDER BY at;

-- name: RunsFor :many
SELECT r.id FROM run_subjects s JOIN runs r ON r.id = s.run_id
WHERE s.subject = @subject AND r.almanac = @almanac
ORDER BY r.seq;
