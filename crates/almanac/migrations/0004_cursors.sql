-- How far each almanac has read the shared deliveries log.
CREATE TABLE cursors (
    almanac text PRIMARY KEY,
    seq     bigint NOT NULL
);
