//! The log every almanac reads, shared so the broker and the library agree on it while the broker
//! knows nothing of workflows.
//!
//! Appends are serialised, so `seq` order is commit order. A reader's cursor depends on that:
//! with concurrent appends, a row could commit behind a cursor that had already passed its `seq`,
//! and it would never be read.
use sqlx::postgres::PgPool;

/// Woken on every append. It is only a hint: a wake-up can be lost, and the cursor is the truth.
pub const CHANNEL: &str = "almanac_deliveries";
const APPEND_LOCK: i64 = 0x0061_6c6d_616e_6163;

#[derive(Clone, Debug, PartialEq)]
pub struct Delivery {
    pub source: String,
    pub delivery_id: String,
    pub kind: String,
    pub body: Vec<u8>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Logged {
    pub seq: i64,
    pub delivery: Delivery,
}

/// Brings the log's schema up to date. It shares a database with other migrations, so it ignores
/// versions it does not know.
pub async fn migrate(pool: &PgPool) -> Result<(), sqlx::migrate::MigrateError> {
    let mut migrator = sqlx::migrate!("./migrations");
    migrator.set_ignore_missing(true);
    migrator.run(pool).await
}

/// Appends a delivery, or does nothing if it is already logged. Returns whether it was new.
pub async fn append(pool: &PgPool, d: &Delivery) -> Result<bool, sqlx::Error> {
    let mut tx = pool.begin().await?;
    sqlx::query("SELECT pg_advisory_xact_lock($1)")
        .bind(APPEND_LOCK)
        .execute(&mut *tx)
        .await?;
    let appended = sqlx::query(
        "INSERT INTO deliveries (source, delivery_id, kind, body) VALUES ($1, $2, $3, $4)
         ON CONFLICT (source, delivery_id) DO NOTHING",
    )
    .bind(&d.source)
    .bind(&d.delivery_id)
    .bind(&d.kind)
    .bind(&d.body)
    .execute(&mut *tx)
    .await?
    .rows_affected()
        > 0;
    if appended {
        sqlx::query("SELECT pg_notify($1, '')")
            .bind(CHANNEL)
            .execute(&mut *tx)
            .await?;
    }
    tx.commit().await?;
    Ok(appended)
}

/// Deliveries after `cursor`, oldest first.
pub async fn after(pool: &PgPool, cursor: i64, limit: i64) -> Result<Vec<Logged>, sqlx::Error> {
    let rows: Vec<(i64, String, String, String, Vec<u8>)> = sqlx::query_as(
        "SELECT seq, source, delivery_id, kind, body FROM deliveries
         WHERE seq > $1 ORDER BY seq LIMIT $2",
    )
    .bind(cursor)
    .bind(limit)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(seq, source, delivery_id, kind, body)| Logged {
            seq,
            delivery: Delivery {
                source,
                delivery_id,
                kind,
                body,
            },
        })
        .collect())
}
