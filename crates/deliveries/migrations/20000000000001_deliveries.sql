-- Every webhook received, for every almanac. It is append-only and knows nothing about workflows.
-- The body is kept as bytes, exactly as it arrived.
CREATE TABLE deliveries (
    seq         bigserial PRIMARY KEY,
    source      text NOT NULL,
    delivery_id text NOT NULL,
    kind        text NOT NULL,
    body        bytea NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source, delivery_id)
);
