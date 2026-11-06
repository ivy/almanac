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

ALTER TABLE pending ADD COLUMN depth integer NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN depth integer NOT NULL DEFAULT 0;
