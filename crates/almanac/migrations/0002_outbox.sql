-- A run's effects, written in the same transaction that ends it, so nothing is applied until
-- the decision is durable. `key` is the idempotency key handed to providers.
CREATE TABLE outbox (
    key        text PRIMARY KEY,
    almanac    text NOT NULL,
    run_id     text NOT NULL REFERENCES runs (id),
    seq        integer NOT NULL,
    effect     jsonb NOT NULL,
    status     text NOT NULL CHECK (status IN ('pending', 'applied', 'would-apply', 'failed')),
    attempts   integer NOT NULL DEFAULT 0,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    applied_at timestamptz
);

CREATE INDEX outbox_pending ON outbox (almanac, created_at) WHERE status = 'pending';
CREATE INDEX outbox_by_run ON outbox (run_id, seq);
