//! Stopping and restarting, with a real PostgreSQL. `make test-db` runs this.
use almanac::consumer::Consumer;
use almanac::daemon::{Health, run};
use almanac::prelude::*;
use almanac::runtime::Runtime;
use almanac::store::{PostgresStore, Store};
use almanac::testing::{FakeForge, ScriptedHarness};
use github::PullRequestOpened;
use serde_json::json;
use sqlx::postgres::PgPool;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;
use testcontainers_modules::postgres::Postgres;
use testcontainers_modules::testcontainers::ContainerAsync;
use testcontainers_modules::testcontainers::runners::AsyncRunner;
use tokio::io::copy_bidirectional;
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::Notify;

#[prompt(text = "Review this.")]
pub struct Prompt;

const AGENT: AgentSpec<Quarantined, Prompt> = AgentSpec::quarantined("agent");

clean! {
    pub enum Answer { Ok }
}

output! {
    pub struct Reply { pub answer: Answer }
}

pub struct Seen {
    repo: Slug,
    sha: Sha,
}

#[workflow(on = PullRequestOpened, key = "{repo}:{head_branch}", intent = "Hold, read, and attest.", may = [Attest], runs_per_hour = 1000, decide = attest)]
async fn holding(cx: &Cx, pr: &PullRequestOpened) -> Result<Seen, Halt> {
    cx.hold("almanac/trust", &pr.repo, &pr.head_sha).await?;
    let _: Untrusted<Reply> = cx.agent(AGENT).read(Prompt, &pr.title).await?;
    Ok(Seen {
        repo: pr.repo.clone(),
        sha: pr.head_sha.clone(),
    })
}

fn attest(seen: &Seen) -> Vec<Effect> {
    vec![Effect::Attest {
        context: "almanac/trust",
        state: AttestState::Success,
        repo: seen.repo.clone(),
        sha: seen.sha.clone(),
    }]
}

async fn database() -> (ContainerAsync<Postgres>, String) {
    let container = Postgres::default().start().await.unwrap();
    let port = container.get_host_port_ipv4(5432).await.unwrap();
    let url = format!("postgres://postgres:postgres@127.0.0.1:{port}/postgres");
    PostgresStore::connect(&url, "migrate").await.unwrap();
    (container, url)
}

fn opened(number: u64) -> Vec<u8> {
    json!({
        "number": number,
        "sender": {"login": "bot", "type": "Bot"},
        "repository": {"full_name": "owner/repo"},
        "pull_request": {
            "state": "open", "draft": false, "title": "Update", "body": null, "labels": [],
            "user": {"login": "bot"},
            "head": {"ref": format!("renovate/p{number}"), "sha": "a".repeat(40)},
        },
    })
    .to_string()
    .into_bytes()
}

async fn log(pool: &PgPool, number: u64) {
    let delivery = deliveries::Delivery {
        source: "github".to_string(),
        delivery_id: format!("d{number}"),
        kind: "github.pull_request.opened".to_string(),
        body: opened(number),
    };
    deliveries::append(pool, &delivery).await.unwrap();
}

fn reply() -> ScriptedHarness {
    ScriptedHarness::replying(r#"{"answer": "Ok"}"#)
}

fn runtime(url: &str, forge: &FakeForge, harness: ScriptedHarness) -> Runtime {
    let store: Arc<dyn Store> = Arc::new(PostgresStore::lazy(url, "daemon").unwrap());
    let forge: Arc<dyn Forge> = Arc::new(forge.clone());
    let providers = Providers::new()
        .provide::<dyn Forge>(forge)
        .harness(harness);
    Runtime::new(vec![Box::new(Holding)], providers)
        .with_store(store)
        .in_mode(Mode::Live)
}

async fn until(what: &str, check: impl Fn() -> bool) {
    for _ in 0..1000 {
        if check() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    panic!("timed out waiting for {what}");
}

fn state_of(forge: &FakeForge) -> Vec<AttestState> {
    forge
        .applied
        .lock()
        .unwrap()
        .iter()
        .map(|(_, s)| *s)
        .collect()
}

#[tokio::test]
async fn a_stop_during_a_run_starts_nothing_new_and_a_restart_finishes_the_work() {
    let (_container, url) = database().await;
    let pool = PgPool::connect(&url).await.unwrap();
    let consumer = Consumer::connect(&url, "daemon").await.unwrap();
    consumer
        .catch_up(&runtime(&url, &FakeForge::default(), reply()))
        .await
        .unwrap();
    log(&pool, 1).await;
    log(&pool, 2).await;

    let forge = FakeForge::default();
    let gate = Arc::new(Notify::new());
    let held = reply().gated(gate.clone());
    let first = runtime(&url, &forge, held.clone()).tightened(almanac::runtime::Limits {
        concurrency: 1,
        ..Default::default()
    });
    let stop = Arc::new(Notify::new());
    let asked = stop.clone();
    let stopping = async move { asked.notified().await };
    let health = Health::default();
    let driver = async {
        until("the first run to reach its agent", || held.calls() == 1).await;
        assert_eq!(
            state_of(&forge),
            [AttestState::Pending],
            "held before the agent ran"
        );
        stop.notify_one();
        tokio::time::sleep(Duration::from_millis(100)).await;
        gate.notify_one();
    };
    let (finished, ()) = tokio::join!(
        run(
            &first,
            &consumer,
            &health,
            Duration::from_secs(20),
            stopping
        ),
        driver
    );
    finished.unwrap();
    assert_eq!(held.calls(), 1, "no new run started after the stop");

    let second = runtime(&url, &forge, reply());
    second.recover().await.unwrap();
    second.drain().await.unwrap();
    second.deliver().await.unwrap();
    let applied = forge.applied.lock().unwrap();
    assert_eq!(applied.len(), 1, "one status per commit and context");
    assert_eq!(applied[0].1, AttestState::Success, "pending was cleared");
}

#[tokio::test]
async fn a_status_left_pending_by_a_killed_run_is_cleared_after_a_restart() {
    let (_container, url) = database().await;
    let pool = PgPool::connect(&url).await.unwrap();
    let consumer = Consumer::connect(&url, "daemon").await.unwrap();
    log(&pool, 1).await;

    let forge = FakeForge::default();
    let gate = Arc::new(Notify::new());
    let held = reply().gated(gate.clone());
    let first = runtime(&url, &forge, held.clone());
    consumer.catch_up(&first).await.unwrap();
    tokio::select! {
        _ = first.drain() => panic!("the gate holds the run open"),
        () = until("the run to hold", || held.calls() == 1) => {}
    }
    assert_eq!(state_of(&forge), [AttestState::Pending]);
    drop(first);

    let restarted = runtime(&url, &forge, reply());
    assert_eq!(restarted.recover().await.unwrap(), 1);
    restarted.drain().await.unwrap();
    restarted.deliver().await.unwrap();
    assert_eq!(state_of(&forge), [AttestState::Success]);
}

/// Forwards to the database until told to cut every connection.
async fn switchable_proxy(target: u16) -> (u16, Arc<AtomicBool>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let up = Arc::new(AtomicBool::new(true));
    let flag = up.clone();
    tokio::spawn(async move {
        while let Ok((mut inbound, _)) = listener.accept().await {
            if !flag.load(Ordering::SeqCst) {
                continue;
            }
            let flag = flag.clone();
            tokio::spawn(async move {
                let Ok(mut outbound) = TcpStream::connect(("127.0.0.1", target)).await else {
                    return;
                };
                tokio::select! {
                    _ = copy_bidirectional(&mut inbound, &mut outbound) => {}
                    () = async {
                        while flag.load(Ordering::SeqCst) {
                            tokio::time::sleep(Duration::from_millis(20)).await;
                        }
                    } => {}
                }
            });
        }
    });
    (port, up)
}

#[tokio::test]
async fn a_database_outage_backs_off_and_recovers_with_no_crash() {
    let (container, url) = database().await;
    let pool = PgPool::connect(&url).await.unwrap();
    let real = container.get_host_port_ipv4(5432).await.unwrap();
    let (port, up) = switchable_proxy(real).await;
    let through = format!("postgres://postgres:postgres@127.0.0.1:{port}/postgres");

    let forge = FakeForge::default();
    let consumer = Consumer::connect(&through, "daemon")
        .await
        .unwrap()
        .polling_every(Duration::from_millis(100));
    let rt = runtime(&through, &forge, reply());
    let health = Health::default();
    let stop = Arc::new(Notify::new());
    let asked = stop.clone();
    let stopping = async move { asked.notified().await };
    let driver = async {
        tokio::time::sleep(Duration::from_millis(300)).await;
        up.store(false, Ordering::SeqCst);
        until("the outage to be noticed", || !health.is_healthy()).await;
        log(&pool, 1).await;
        up.store(true, Ordering::SeqCst);
        until("recovery", || health.is_healthy()).await;
        until("the held delivery to run", || {
            !forge.applied.lock().unwrap().is_empty()
        })
        .await;
        stop.notify_one();
    };
    let (finished, ()) = tokio::join!(
        run(&rt, &consumer, &health, Duration::from_secs(5), stopping),
        driver
    );
    finished.unwrap();
}
