-- Every row is scoped to one almanac: almanacs share a database and each sees every delivery.

CREATE SEQUENCE run_seq;

CREATE TABLE seen (
    almanac     text NOT NULL,
    source      text NOT NULL,
    delivery_id text NOT NULL,
    seen_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (almanac, source, delivery_id)
);

-- At most one waiting event per key, so a burst coalesces. A row whose key has a running run
-- is the dirty flag: it becomes claimable when that run ends.
CREATE TABLE pending (
    almanac   text NOT NULL,
    workflow  text NOT NULL,
    key       text NOT NULL,
    seq       bigserial NOT NULL,
    payload   jsonb NOT NULL,
    subjects  text[] NOT NULL,
    queued_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (almanac, workflow, key)
);

CREATE TABLE runs (
    id          text PRIMARY KEY,
    seq         bigint NOT NULL,
    almanac     text NOT NULL,
    workflow    text NOT NULL,
    key         text NOT NULL,
    payload     jsonb NOT NULL,
    status      text NOT NULL CHECK (status IN ('running', 'finished', 'interrupted')),
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

CREATE UNIQUE INDEX one_active_run_per_key ON runs (almanac, workflow, key) WHERE status = 'running';
CREATE INDEX runs_by_workflow ON runs (almanac, workflow);

CREATE TABLE run_subjects (
    run_id  text NOT NULL REFERENCES runs (id),
    subject text NOT NULL,
    PRIMARY KEY (run_id, subject)
);
CREATE INDEX run_subjects_by_subject ON run_subjects (subject);

CREATE TABLE run_log (
    run_id     text NOT NULL REFERENCES runs (id),
    name       text NOT NULL,
    occurrence integer NOT NULL,
    value      jsonb NOT NULL,
    PRIMARY KEY (run_id, name, occurrence)
);

CREATE TABLE tripped (
    almanac  text NOT NULL,
    workflow text NOT NULL,
    PRIMARY KEY (almanac, workflow)
);
