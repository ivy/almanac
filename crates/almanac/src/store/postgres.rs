//! Durable state, shared by the almanacs of one deployment and separated by `almanac`.
use super::{Admission, Claim, ClaimRequest, Entry, EntryStatus, Run, Store, StoreError, Stored};
use crate::runtime::{Ingest, Origin};
use crate::workflow::Subject;
use futures::future::BoxFuture;
use serde_json::Value;
use sqlx::postgres::{PgPool, PgPoolOptions};
use time::OffsetDateTime;

const MAX_CONNECTIONS: u32 = 4;
/// The `tripped` row for the budget across all workflows.
const OVERALL: &str = "*";

impl From<sqlx::Error> for StoreError {
    fn from(e: sqlx::Error) -> Self {
        Self(e.to_string())
    }
}

impl From<sqlx::migrate::MigrateError> for StoreError {
    fn from(e: sqlx::migrate::MigrateError) -> Self {
        Self(e.to_string())
    }
}

pub struct PostgresStore {
    pool: PgPool,
    almanac: String,
}

impl PostgresStore {
    /// Connects and brings the schema up to date.
    pub async fn connect(url: &str, almanac: &str) -> Stored<Self> {
        let pool = options().connect(url).await?;
        // The database also holds the broker's log, whose migrations are not in this set.
        let mut migrator = sqlx::migrate!("./migrations");
        migrator.set_ignore_missing(true);
        migrator.run(&pool).await?;
        Ok(Self {
            pool,
            almanac: almanac.to_string(),
        })
    }

    /// Connects on first use, to a schema something else has already migrated.
    pub fn lazy(url: &str, almanac: &str) -> Stored<Self> {
        Ok(Self {
            pool: options().connect_lazy(url)?,
            almanac: almanac.to_string(),
        })
    }

    async fn admit_tx(
        &self,
        source: &str,
        delivery_id: &str,
        admission: Admission,
    ) -> Stored<Ingest> {
        let mut tx = self.pool.begin().await?;
        let fresh = sqlx::query!(
            "INSERT INTO seen (almanac, source, delivery_id) VALUES ($1, $2, $3)
             ON CONFLICT DO NOTHING",
            self.almanac,
            source,
            delivery_id,
        )
        .execute(&mut *tx)
        .await?
        .rows_affected();
        if fresh == 0 {
            return Ok(Ingest::Duplicate);
        }
        let ingest = match admission {
            Admission::Drop(ingest) => ingest,
            Admission::Queue(pending) => {
                let subjects: Vec<String> = pending.subjects.iter().map(texted).collect();
                let inserted = sqlx::query!(
                    r#"INSERT INTO pending (almanac, workflow, key, payload, subjects, lane, origin)
                       VALUES ($1, $2, $3, $4, $5, $6, $7)
                       ON CONFLICT (almanac, workflow, key)
                       DO UPDATE SET payload = EXCLUDED.payload, subjects = EXCLUDED.subjects,
                                     lane = LEAST(pending.lane, EXCLUDED.lane),
                                     origin = EXCLUDED.origin
                       RETURNING (xmax = 0) AS "inserted!""#,
                    self.almanac,
                    pending.workflow,
                    pending.key,
                    without_nul(pending.payload.clone()),
                    &subjects,
                    i16::from(pending.origin.lane()),
                    pending.origin.as_str(),
                )
                .fetch_one(&mut *tx)
                .await?
                .inserted;
                let running = sqlx::query_scalar!(
                    r#"SELECT EXISTS (
                         SELECT 1 FROM runs
                         WHERE almanac = $1 AND workflow = $2 AND key = $3 AND status = 'running'
                       ) AS "running!""#,
                    self.almanac,
                    pending.workflow,
                    pending.key,
                )
                .fetch_one(&mut *tx)
                .await?;
                match (running, inserted) {
                    (true, _) => Ingest::MarkedDirty,
                    (false, true) => Ingest::Queued,
                    (false, false) => Ingest::Coalesced,
                }
            }
        };
        tx.commit().await?;
        Ok(ingest)
    }

    async fn claim_tx(&self, request: ClaimRequest<'_>) -> Stored<Claim> {
        let mut tx = self.pool.begin().await?;
        // Claims are rare and short, so one at a time keeps budget counts exact.
        sqlx::query("SELECT pg_advisory_xact_lock(hashtext($1))")
            .bind(&self.almanac)
            .execute(&mut *tx)
            .await?;
        let now = request.now;
        let since = now - request.window;
        let workflows: Vec<String> = request
            .budgets
            .iter()
            .map(|b| b.workflow.to_string())
            .collect();
        let counts = sqlx::query!(
            r#"SELECT workflow, count(*) AS "started!" FROM runs
               WHERE almanac = $1 AND started_at > $2
               GROUP BY workflow"#,
            self.almanac,
            since,
        )
        .fetch_all(&mut *tx)
        .await?;
        let waiting = sqlx::query!(
            r#"SELECT p.workflow, count(*) AS "held!" FROM pending p
               WHERE p.almanac = $1 AND p.workflow = ANY($2)
                 AND NOT EXISTS (
                   SELECT 1 FROM runs r
                   WHERE r.almanac = p.almanac AND r.workflow = p.workflow
                     AND r.key = p.key AND r.status = 'running'
                 )
               GROUP BY p.workflow"#,
            self.almanac,
            &workflows,
        )
        .fetch_all(&mut *tx)
        .await?;
        let started = |workflow: &str| {
            let found = counts.iter().find(|c| c.workflow == workflow);
            found.map_or(0, |c| c.started)
        };
        let held = |workflow: &str| {
            let found = waiting.iter().find(|w| w.workflow == workflow);
            found.map_or(0, |w| w.held) as usize
        };
        if let Some(limit) = request.overall
            && counts.iter().map(|c| c.started).sum::<i64>() >= i64::from(limit)
        {
            let total = workflows.iter().map(|w| held(w)).sum();
            let claim = if total > 0 && self.report(&mut tx, OVERALL, now, since).await? {
                Claim::Tripped {
                    workflow: None,
                    held: total,
                }
            } else {
                Claim::Idle
            };
            tx.commit().await?;
            return Ok(claim);
        }
        let mut open = vec![];
        for budget in request.budgets {
            if started(budget.workflow) < i64::from(budget.limit) {
                open.push(budget.workflow.to_string());
                continue;
            }
            let waiting = held(budget.workflow);
            if waiting > 0 && self.report(&mut tx, budget.workflow, now, since).await? {
                tx.commit().await?;
                return Ok(Claim::Tripped {
                    workflow: Some(budget.workflow.to_string()),
                    held: waiting,
                });
            }
        }
        let candidate = sqlx::query!(
            "SELECT p.workflow, p.key, p.payload, p.subjects, p.origin
             FROM pending p
             WHERE p.almanac = $1
               AND p.workflow = ANY($2)
               AND NOT EXISTS (
                 SELECT 1 FROM runs r
                 WHERE r.almanac = p.almanac AND r.workflow = p.workflow
                   AND r.key = p.key AND r.status = 'running'
               )
             ORDER BY p.lane, p.seq
             LIMIT 1",
            self.almanac,
            &open,
        )
        .fetch_optional(&mut *tx)
        .await?;
        let Some(next) = candidate else {
            tx.commit().await?;
            return Ok(Claim::Idle);
        };
        let id = sqlx::query_scalar!(
            r#"INSERT INTO runs (id, seq, almanac, workflow, key, payload, status, started_at, origin)
               SELECT 'run-' || n, n, $1, $2, $3, $4, 'running', $5, $6
               FROM nextval('run_seq') AS n
               RETURNING id"#,
            self.almanac,
            next.workflow,
            next.key,
            next.payload,
            now,
            next.origin,
        )
        .fetch_one(&mut *tx)
        .await?;
        sqlx::query!(
            "DELETE FROM pending WHERE almanac = $1 AND workflow = $2 AND key = $3",
            self.almanac,
            next.workflow,
            next.key,
        )
        .execute(&mut *tx)
        .await?;
        link(&mut tx, &id, &next.subjects).await?;
        tx.commit().await?;
        Ok(Claim::Run(Run {
            id,
            workflow: next.workflow,
            key: next.key,
            payload: next.payload,
            origin: Origin::parse(&next.origin)?,
        }))
    }

    /// Records a trip unless one was reported in this window.
    async fn report(
        &self,
        tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
        scope: &str,
        now: OffsetDateTime,
        since: OffsetDateTime,
    ) -> Stored<bool> {
        let reported = sqlx::query!(
            "INSERT INTO tripped (almanac, workflow, tripped_at) VALUES ($1, $2, $3)
             ON CONFLICT (almanac, workflow) DO UPDATE SET tripped_at = EXCLUDED.tripped_at
             WHERE tripped.tripped_at <= $4",
            self.almanac,
            scope,
            now,
            since,
        )
        .execute(&mut **tx)
        .await?
        .rows_affected();
        Ok(reported > 0)
    }

    async fn release_tx(
        &self,
        run_id: &str,
        links: &[Subject],
        effects: Vec<Value>,
        status: EntryStatus,
    ) -> Stored<()> {
        let mut tx = self.pool.begin().await?;
        sqlx::query!(
            "UPDATE runs SET status = 'finished', finished_at = now()
             WHERE id = $1 AND status = 'running'",
            run_id,
        )
        .execute(&mut *tx)
        .await?;
        let links: Vec<String> = links.iter().map(texted).collect();
        link(&mut tx, run_id, &links).await?;
        for (i, effect) in effects.into_iter().enumerate() {
            sqlx::query!(
                "INSERT INTO outbox (key, almanac, run_id, seq, effect, status)
                 VALUES ($1, $2, $3, $4, $5, $6)",
                format!("{run_id}:{i}"),
                self.almanac,
                run_id,
                i as i32,
                without_nul(effect),
                status.as_str(),
            )
            .execute(&mut *tx)
            .await?;
        }
        tx.commit().await?;
        Ok(())
    }

    async fn recover_tx(&self) -> Stored<usize> {
        let mut tx = self.pool.begin().await?;
        let interrupted = sqlx::query!(
            "UPDATE runs SET status = 'interrupted', finished_at = now()
             WHERE almanac = $1 AND status = 'running'
             RETURNING id, workflow, key, payload, origin",
            self.almanac,
        )
        .fetch_all(&mut *tx)
        .await?;
        for run in &interrupted {
            sqlx::query!(
                "INSERT INTO pending (almanac, workflow, key, payload, subjects, lane, origin)
                 SELECT $1, $2, $3, $4,
                        coalesce(array_agg(subject), '{}'), $6, $7
                 FROM run_subjects WHERE run_id = $5
                 ON CONFLICT (almanac, workflow, key) DO NOTHING",
                self.almanac,
                run.workflow,
                run.key,
                run.payload,
                run.id,
                i16::from(Origin::parse(&run.origin)?.lane()),
                run.origin,
            )
            .execute(&mut *tx)
            .await?;
        }
        tx.commit().await?;
        Ok(interrupted.len())
    }
}

/// PostgreSQL's `jsonb` refuses U+0000, and a payload is hostile text. Left in, one crafted
/// title would fail its delivery forever.
fn without_nul(value: Value) -> Value {
    match value {
        Value::String(s) => Value::String(s.replace('\0', "\u{fffd}")),
        Value::Array(items) => Value::Array(items.into_iter().map(without_nul).collect()),
        Value::Object(fields) => Value::Object(
            fields
                .into_iter()
                .map(|(k, v)| (k.replace('\0', "\u{fffd}"), without_nul(v)))
                .collect(),
        ),
        other => other,
    }
}

fn entry(
    key: String,
    run_id: Option<String>,
    effect: Value,
    status: &str,
    attempts: i32,
) -> Stored<Entry> {
    Ok(Entry {
        key,
        run_id,
        effect,
        status: EntryStatus::parse(status)?,
        attempts: attempts as u32,
    })
}

fn options() -> PgPoolOptions {
    PgPoolOptions::new().max_connections(MAX_CONNECTIONS)
}

fn texted(subject: &Subject) -> String {
    subject.as_str().to_string()
}

async fn link(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    run_id: &str,
    subjects: &[String],
) -> Stored<()> {
    sqlx::query!(
        "INSERT INTO run_subjects (run_id, subject)
         SELECT $1, subject FROM unnest($2::text[]) AS subject
         ON CONFLICT DO NOTHING",
        run_id,
        subjects,
    )
    .execute(&mut **tx)
    .await?;
    Ok(())
}

impl Store for PostgresStore {
    fn admit<'a>(
        &'a self,
        source: &'a str,
        delivery_id: &'a str,
        admission: Admission,
    ) -> BoxFuture<'a, Stored<Ingest>> {
        Box::pin(self.admit_tx(source, delivery_id, admission))
    }

    fn claim<'a>(&'a self, request: ClaimRequest<'a>) -> BoxFuture<'a, Stored<Claim>> {
        Box::pin(self.claim_tx(request))
    }

    fn note<'a>(
        &'a self,
        key: &'a str,
        effect: Value,
        status: EntryStatus,
    ) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            sqlx::query!(
                "INSERT INTO outbox (key, almanac, seq, effect, status)
                 VALUES ($1, $2, 0, $3, $4)
                 ON CONFLICT (key) DO NOTHING",
                key,
                self.almanac,
                without_nul(effect),
                status.as_str(),
            )
            .execute(&self.pool)
            .await?;
            Ok(())
        })
    }

    fn release<'a>(
        &'a self,
        run_id: &'a str,
        links: &'a [Subject],
        effects: Vec<Value>,
        status: EntryStatus,
    ) -> BoxFuture<'a, Stored<()>> {
        Box::pin(self.release_tx(run_id, links, effects, status))
    }

    fn undelivered(&self) -> BoxFuture<'_, Stored<Vec<Entry>>> {
        Box::pin(async move {
            let rows = sqlx::query!(
                "SELECT key, run_id, effect, status, attempts FROM outbox
                 WHERE almanac = $1 AND status = 'pending'
                 ORDER BY created_at, run_id, seq",
                self.almanac,
            )
            .fetch_all(&self.pool)
            .await?;
            rows.into_iter()
                .map(|r| entry(r.key, r.run_id, r.effect, &r.status, r.attempts))
                .collect()
        })
    }

    fn outbox<'a>(&'a self, run_id: &'a str) -> BoxFuture<'a, Stored<Vec<Entry>>> {
        Box::pin(async move {
            let rows = sqlx::query!(
                "SELECT key, run_id, effect, status, attempts FROM outbox
                 WHERE almanac = $1 AND run_id = $2
                 ORDER BY seq",
                self.almanac,
                run_id,
            )
            .fetch_all(&self.pool)
            .await?;
            rows.into_iter()
                .map(|r| entry(r.key, r.run_id, r.effect, &r.status, r.attempts))
                .collect()
        })
    }

    fn delivered<'a>(&'a self, key: &'a str) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            sqlx::query!(
                "UPDATE outbox SET status = 'applied', applied_at = now()
                 WHERE key = $1 AND almanac = $2",
                key,
                self.almanac,
            )
            .execute(&self.pool)
            .await?;
            Ok(())
        })
    }

    fn delivery_failed<'a>(
        &'a self,
        key: &'a str,
        error: &'a str,
        give_up: u32,
    ) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            sqlx::query!(
                "UPDATE outbox
                 SET attempts = attempts + 1,
                     last_error = $3,
                     status = CASE WHEN attempts + 1 >= $4 THEN 'failed' ELSE status END
                 WHERE key = $1 AND almanac = $2",
                key,
                self.almanac,
                error.replace('\0', "\u{fffd}"),
                give_up as i32,
            )
            .execute(&self.pool)
            .await?;
            Ok(())
        })
    }

    fn recover(&self) -> BoxFuture<'_, Stored<usize>> {
        Box::pin(self.recover_tx())
    }

    fn run<'a>(&'a self, run_id: &'a str) -> BoxFuture<'a, Stored<Option<Run>>> {
        Box::pin(async move {
            let row = sqlx::query!(
                "SELECT id, workflow, key, payload, origin FROM runs WHERE id = $1 AND almanac = $2",
                run_id,
                self.almanac,
            )
            .fetch_optional(&self.pool)
            .await?;
            row.map(|r| {
                Ok(Run {
                    id: r.id,
                    workflow: r.workflow,
                    key: r.key,
                    payload: r.payload,
                    origin: Origin::parse(&r.origin)?,
                })
            })
            .transpose()
        })
    }

    fn recorded<'a>(
        &'a self,
        run_id: &'a str,
        name: &'a str,
        occurrence: usize,
    ) -> BoxFuture<'a, Stored<Option<Value>>> {
        Box::pin(async move {
            let value = sqlx::query_scalar!(
                "SELECT value FROM run_log WHERE run_id = $1 AND name = $2 AND occurrence = $3",
                run_id,
                name,
                occurrence as i32,
            )
            .fetch_optional(&self.pool)
            .await?;
            Ok(value)
        })
    }

    fn record<'a>(
        &'a self,
        run_id: &'a str,
        name: &'a str,
        occurrence: usize,
        value: Value,
    ) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            sqlx::query!(
                "INSERT INTO run_log (run_id, name, occurrence, value) VALUES ($1, $2, $3, $4)
                 ON CONFLICT (run_id, name, occurrence) DO UPDATE SET value = EXCLUDED.value",
                run_id,
                name,
                occurrence as i32,
                without_nul(value),
            )
            .execute(&self.pool)
            .await?;
            Ok(())
        })
    }

    fn runs_for<'a>(&'a self, subject: &'a Subject) -> BoxFuture<'a, Stored<Vec<String>>> {
        Box::pin(async move {
            let ids = sqlx::query_scalar!(
                "SELECT r.id FROM run_subjects s JOIN runs r ON r.id = s.run_id
                 WHERE s.subject = $1 AND r.almanac = $2
                 ORDER BY r.seq",
                subject.as_str(),
                self.almanac,
            )
            .fetch_all(&self.pool)
            .await?;
            Ok(ids)
        })
    }
}
