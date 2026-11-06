//! Reads the broker's log into a runtime. Delivery is at-least-once: `ingest` dedupes, so a crash
//! between ingesting and saving the cursor repeats a delivery harmlessly.
use crate::diag::Error;
use crate::runtime::{Envelope, Origin, Runtime};
use crate::store::{StoreError, Stored};
use sqlx::postgres::{PgListener, PgPool, PgPoolOptions};
use std::future::Future;
use std::time::Duration;

const BATCH: i64 = 100;
/// `NOTIFY` is only a hint, so this is how long a lost one can delay a delivery.
const POLL: Duration = Duration::from_secs(30);
const MAX_CONNECTIONS: u32 = 4;

pub struct Consumer {
    pool: PgPool,
    almanac: String,
    poll: Duration,
}

impl Consumer {
    /// Connects, and for a new almanac starts at the end of the log: history is `backfill`'s
    /// job, and replaying it here would spend budgets on old events.
    pub async fn connect(url: &str, almanac: &str) -> Stored<Self> {
        let pool = PgPoolOptions::new()
            .max_connections(MAX_CONNECTIONS)
            .connect(url)
            .await?;
        deliveries::migrate(&pool).await?;
        sqlx::query!(
            "INSERT INTO cursors (almanac, seq)
             SELECT $1, coalesce(max(seq), 0) FROM deliveries
             ON CONFLICT (almanac) DO NOTHING",
            almanac,
        )
        .execute(&pool)
        .await?;
        Ok(Self {
            pool,
            almanac: almanac.to_string(),
            poll: POLL,
        })
    }

    pub fn polling_every(mut self, poll: Duration) -> Self {
        self.poll = poll;
        self
    }

    async fn cursor(&self) -> Stored<i64> {
        let seq = sqlx::query_scalar!("SELECT seq FROM cursors WHERE almanac = $1", self.almanac)
            .fetch_one(&self.pool)
            .await?;
        Ok(seq)
    }

    async fn advance(&self, seq: i64) -> Stored<()> {
        sqlx::query!(
            "UPDATE cursors SET seq = $2 WHERE almanac = $1",
            self.almanac,
            seq,
        )
        .execute(&self.pool)
        .await?;
        Ok(())
    }

    /// Ingests everything after the cursor. Returns how many deliveries it read.
    pub async fn catch_up(&self, runtime: &Runtime) -> Stored<usize> {
        let mut read = 0;
        loop {
            let cursor = self.cursor().await?;
            let batch = deliveries::after(&self.pool, cursor, BATCH).await?;
            if batch.is_empty() {
                return Ok(read);
            }
            for logged in batch {
                let d = logged.delivery;
                // The broker verified the signature, but the body is still hostile: one that
                // does not parse is skipped, or it would stop this cursor forever.
                if let Ok(payload) = serde_json::from_slice(&d.body) {
                    let envelope = Envelope {
                        source: d.source,
                        delivery_id: d.delivery_id,
                        kind: d.kind,
                        depth: 0,
                        origin: Origin::Webhook,
                        payload,
                    };
                    runtime.ingest(envelope).await?;
                }
                self.advance(logged.seq).await?;
                read += 1;
            }
        }
    }

    /// Reads, runs, and applies until `stop` finishes, woken by `NOTIFY` or by polling. Work held
    /// by a budget is picked up on each wake, so polling also reopens a window.
    pub async fn follow(
        &self,
        runtime: &Runtime,
        stop: impl Future<Output = ()>,
    ) -> Result<(), Error> {
        let mut listener = PgListener::connect_with(&self.pool)
            .await
            .map_err(StoreError::from)?;
        listener
            .listen(deliveries::CHANNEL)
            .await
            .map_err(StoreError::from)?;
        tokio::pin!(stop);
        loop {
            self.catch_up(runtime).await?;
            runtime.drain().await?;
            runtime.deliver().await?;
            tokio::select! {
                () = &mut stop => return Ok(()),
                _ = listener.recv() => {}
                () = tokio::time::sleep(self.poll) => {}
            }
        }
    }
}
