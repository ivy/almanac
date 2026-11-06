//! Needs podman. `make test-db` runs this.
use almanac::runtime::{Ingest, Origin};
use almanac::store::{Admission, Budget, Claim, ClaimRequest, Pending, PostgresStore, Store};
use almanac::testing::conformance;
use serde_json::json;
use sqlx::postgres::PgPool;
use std::sync::Arc;
use std::time::Duration;
use testcontainers_modules::postgres::Postgres;
use testcontainers_modules::testcontainers::ContainerAsync;
use testcontainers_modules::testcontainers::runners::AsyncRunner;

async fn database() -> (ContainerAsync<Postgres>, String) {
    let container = Postgres::default().start().await.unwrap();
    let port = container.get_host_port_ipv4(5432).await.unwrap();
    let url = format!("postgres://postgres:postgres@127.0.0.1:{port}/postgres");
    PostgresStore::connect(&url, "migrate").await.unwrap();
    (container, url)
}

fn queue(key: &str, payload: serde_json::Value) -> Admission {
    Admission::Queue(Pending {
        workflow: "w".to_string(),
        key: key.to_string(),
        payload,
        subjects: vec![],
        origin: Origin::Webhook,
        depth: 0,
    })
}

const BUDGET: &[Budget] = &[Budget {
    workflow: "w",
    limit: 10,
}];

#[tokio::test]
async fn postgres_store_conforms() {
    let (_container, url) = database().await;
    conformance::all(|scope| Arc::new(PostgresStore::lazy(&url, scope).unwrap()) as Arc<dyn Store>)
        .await;
}

#[tokio::test]
async fn the_database_itself_refuses_a_second_active_run_for_a_key() {
    let (_container, url) = database().await;
    let pool = PgPool::connect(&url).await.unwrap();
    let insert = |id: &'static str| {
        sqlx::query(
            "INSERT INTO runs (id, seq, almanac, workflow, key, payload, status)
             VALUES ($1, 1, 'a', 'w', 'k', 'null', 'running')",
        )
        .bind(id)
        .execute(&pool)
    };
    insert("first").await.unwrap();
    let refused = insert("second").await.unwrap_err().to_string();
    assert!(refused.contains("one_active_run_per_key"), "{refused}");
}

#[tokio::test]
async fn a_delivery_that_failed_to_persist_is_accepted_when_redelivered() {
    let (_container, url) = database().await;
    let store = PostgresStore::lazy(&url, "redelivery").unwrap();
    store
        .admit("t", "d1", queue("a key PostgreSQL refuses \0", json!(1)))
        .await
        .unwrap_err();
    let again = store.admit("t", "d1", queue("k", json!(1))).await.unwrap();
    assert_eq!(again, Ingest::Queued);
}

#[tokio::test]
async fn a_nul_in_hostile_text_cannot_wedge_ingest_or_recording() {
    let (_container, url) = database().await;
    let store = PostgresStore::lazy(&url, "nul").unwrap();
    let hostile = json!({"title": "a\u{0}b", "nested": ["\u{0}"]});
    let admitted = store.admit("t", "d1", queue("k", hostile.clone())).await;
    assert_eq!(admitted, Ok(Ingest::Queued));
    let request = ClaimRequest {
        budgets: BUDGET,
        overall: None,
        window: Duration::from_secs(3600),
        now: time::OffsetDateTime::now_utc(),
    };
    let Claim::Run(run) = store.claim(request).await.unwrap() else {
        panic!("expected a run");
    };
    assert_eq!(
        run.payload,
        json!({"title": "a\u{fffd}b", "nested": ["\u{fffd}"]})
    );
    store.record(&run.id, "read", 1, hostile).await.unwrap();
}
