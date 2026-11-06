-- Budgets become sliding windows, so a trip is a moment and not a permanent state. A scope of
-- '*' is the overall budget. Outbox entries may belong to no run, as a budget alert does.
ALTER TABLE tripped ADD COLUMN tripped_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE pending ADD COLUMN lane smallint NOT NULL DEFAULT 0;
ALTER TABLE pending ADD COLUMN origin text NOT NULL DEFAULT 'webhook';
CREATE INDEX pending_claim_order ON pending (almanac, lane, seq);

ALTER TABLE runs ADD COLUMN origin text NOT NULL DEFAULT 'webhook';
CREATE INDEX runs_in_window ON runs (almanac, started_at);

ALTER TABLE outbox ALTER COLUMN run_id DROP NOT NULL;
