-- +goose Up

-- Every row is scoped to one almanac: almanacs share a database and each sees every delivery.
-- Run IDs come from one sequence, so they are unique across almanacs.
CREATE SEQUENCE run_seq;

-- Deliveries already admitted. A delivery counts as seen only once its work is stored with it.
CREATE TABLE seen (
    almanac     text NOT NULL,
    source      text NOT NULL,
    delivery_id text NOT NULL,
    seen_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (almanac, source, delivery_id)
);

-- At most one waiting event per key, so a burst coalesces. A row whose key has a running run
-- is the dirty flag: it becomes claimable when that run ends. A lower lane is claimed first.
CREATE TABLE pending (
    almanac   text NOT NULL,
    workflow  text NOT NULL,
    key       text NOT NULL,
    seq       bigserial NOT NULL,
    payload   jsonb NOT NULL,
    subjects  text[] NOT NULL,
    lane      smallint NOT NULL,
    origin    text NOT NULL CHECK (origin IN ('webhook', 'scan', 'simulated')),
    depth     integer NOT NULL,
    queued_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (almanac, workflow, key)
);

CREATE INDEX pending_claim_order ON pending (almanac, lane, seq);

CREATE TABLE runs (
    seq         bigint NOT NULL DEFAULT nextval('run_seq'),
    id          text GENERATED ALWAYS AS ('run-' || seq::text) STORED PRIMARY KEY,
    almanac     text NOT NULL,
    workflow    text NOT NULL,
    key         text NOT NULL,
    payload     jsonb NOT NULL,
    origin      text NOT NULL CHECK (origin IN ('webhook', 'scan', 'simulated')),
    depth       integer NOT NULL,
    status      text NOT NULL CHECK (status IN ('running', 'finished', 'interrupted')),
    started_at  timestamptz NOT NULL,
    finished_at timestamptz
);

-- One active run per key, enforced by the database whatever a store does.
CREATE UNIQUE INDEX one_active_run_per_key ON runs (almanac, workflow, key) WHERE status = 'running';
-- Budgets count the runs started in a window.
CREATE INDEX runs_in_window ON runs (almanac, started_at);

CREATE TABLE run_subjects (
    run_id  text NOT NULL REFERENCES runs (id),
    subject text NOT NULL,
    PRIMARY KEY (run_id, subject)
);

CREATE INDEX run_subjects_by_subject ON run_subjects (subject);

-- What each run read, by name and occurrence, so a replay reaches the same decision.
CREATE TABLE run_log (
    run_id     text NOT NULL REFERENCES runs (id),
    name       text NOT NULL,
    occurrence integer NOT NULL,
    value      jsonb NOT NULL,
    PRIMARY KEY (run_id, name, occurrence)
);

-- When each budget last tripped, so a trip is reported once per window. A workflow of '*' is
-- the budget across all workflows.
CREATE TABLE tripped (
    almanac    text NOT NULL,
    workflow   text NOT NULL,
    tripped_at timestamptz NOT NULL,
    PRIMARY KEY (almanac, workflow)
);

-- A run's effects, written in the same transaction that ends it, so nothing is applied until
-- the decision is durable. `key` is the idempotency key handed to providers. An entry with no
-- run is one no run produced, such as a budget alert. A dry run's entries are 'would-apply',
-- which delivery never selects.
CREATE TABLE outbox (
    key        text PRIMARY KEY,
    almanac    text NOT NULL,
    run_id     text REFERENCES runs (id),
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

-- What Almanac recently did to each key. Webhooks do not say what caused them, so an event after
-- one of these inherits its depth, whoever sent it.
CREATE TABLE ledger (
    almanac  text NOT NULL,
    workflow text NOT NULL,
    key      text NOT NULL,
    kind     text NOT NULL,
    depth    integer NOT NULL,
    at       timestamptz NOT NULL
);

CREATE INDEX ledger_by_key ON ledger (almanac, workflow, key, at);

-- +goose Down

DROP TABLE ledger;
DROP TABLE outbox;
DROP TABLE tripped;
DROP TABLE run_log;
DROP TABLE run_subjects;
DROP TABLE runs;
DROP TABLE pending;
DROP TABLE seen;
DROP SEQUENCE run_seq;
