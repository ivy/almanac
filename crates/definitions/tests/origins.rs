//! A webhook, a scan, and a simulation must be the same event to a workflow.
use almanac::clock::FakeClock;
use almanac::prelude::*;
use almanac::runtime::{Envelope, Ingest, Origin, Outcome, Runtime};
use almanac::store::{EntryStatus, MemoryStore, Store};
use almanac::testing::ScriptedHarness;
use github::PullRequestOpened;
use github::client::{Auth, GithubClient};
use serde_json::Value;
use simforge::{Pull, RENOVATE, Repo, SimForge};
use std::sync::Arc;

const OWNER: &str = "owner";
const REPO: &str = "owner/repo";

clean! {
    pub struct Seen { pub labeled: bool, pub draft: bool }
}

async fn list_open(scanner: &Scanner) -> Result<Vec<Scanned<PullRequestOpened>>, StepError> {
    let client = scanner.get::<GithubClient>()?;
    let mut found = vec![];
    for repo in client.repositories(OWNER).await? {
        let name = repo["full_name"].as_str().unwrap_or_default();
        let slug: Slug = name.parse()?;
        if scanner.repo().is_some_and(|wanted| *wanted != slug) {
            continue;
        }
        for pull in client.pulls_by(name, RENOVATE).await? {
            let head: Sha = pull["head"]["sha"].as_str().unwrap_or_default().parse()?;
            let number = pull["number"].as_u64().unwrap_or_default();
            found.push(Scanned::pull(&slug, number, &head, &pull));
        }
    }
    Ok(found)
}

#[workflow(
    on = PullRequestOpened,
    key = "{repo}:{head_branch}",
    intent = "Note what a pull request looks like.",
    may = [Await, Ask],
    runs_per_hour = 100,
    scan = list_open,
    decide = respond,
)]
async fn watcher(_cx: &Cx, pr: &PullRequestOpened) -> Result<Seen, Halt> {
    Ok(Seen {
        labeled: pr
            .labels
            .narrow(|l| l.iter().any(|name| name == "dependencies")),
        draft: pr.draft,
    })
}

fn respond(seen: &Seen) -> Vec<Effect> {
    match seen.labeled {
        true => vec![Effect::Await { until: "labeled" }],
        false => vec![Effect::Await { until: "unlabeled" }],
    }
}

async fn fleet(count: usize) -> SimForge {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public(REPO));
    for i in 0..count {
        let mut pull = Pull::renovate(&format!("renovate/p{i}"), "Update");
        pull.labels.push("dependencies".to_string());
        sim.add_pull(REPO, pull);
    }
    sim
}

fn runtime(sim: &SimForge, store: Arc<dyn Store>) -> Runtime {
    let client = GithubClient::new(sim.base_url(), Auth::Token("t".to_string()));
    let providers = Providers::new()
        .provide::<GithubClient>(Arc::new(client))
        .harness(ScriptedHarness::default());
    Runtime::new(vec![Box::new(Watcher)], providers).with_store(store)
}

fn memory() -> Arc<dyn Store> {
    Arc::new(MemoryStore::default())
}

async fn rest(sim: &SimForge, number: u64) -> Value {
    let client = GithubClient::new(sim.base_url(), Auth::Token("t".to_string()));
    client.pull(REPO, number).await.unwrap()
}

fn event(origin: Origin, delivery: &str, payload: Value) -> Envelope {
    Envelope {
        source: "t".to_string(),
        delivery_id: delivery.to_string(),
        kind: "github.pull_request.opened".to_string(),
        depth: 0,
        origin,
        payload,
    }
}

#[tokio::test]
async fn backfilling_existing_pull_requests_records_a_decision_for_each_and_writes_nothing() {
    let sim = fleet(28).await;
    let rt = runtime(&sim, memory());
    let report = rt.backfill(None).await.unwrap();
    assert_eq!((report.found, report.queued), (28, 28));
    let outcomes = rt.drain().await.unwrap();
    assert_eq!(outcomes.len(), 28);
    for outcome in &outcomes {
        let Outcome::Ran { run_id, .. } = outcome else {
            panic!("{outcome:?}");
        };
        let recorded = rt.effects_of(run_id).await.unwrap();
        assert_eq!(recorded[0].status, EntryStatus::WouldApply);
    }
    assert_eq!(rt.deliver().await.unwrap(), vec![]);
    assert_eq!(sim.writes(), vec![]);
}

#[tokio::test]
async fn rerunning_a_backfill_starts_no_new_runs() {
    let sim = fleet(3).await;
    let rt = runtime(&sim, memory());
    rt.backfill(None).await.unwrap();
    rt.drain().await.unwrap();
    let again = rt.backfill(None).await.unwrap();
    assert_eq!((again.found, again.unchanged, again.queued), (3, 3, 0));
    assert!(rt.drain().await.unwrap().is_empty());
}

#[tokio::test]
async fn a_new_commit_is_scanned_again() {
    let sim = fleet(1).await;
    let rt = runtime(&sim, memory());
    rt.backfill(None).await.unwrap();
    rt.drain().await.unwrap();
    sim.move_branch(REPO, 1, &"c".repeat(40), "a new commit");
    let again = rt.backfill(None).await.unwrap();
    assert_eq!(again.queued, 1);
}

#[tokio::test]
async fn a_backfill_can_look_at_one_repository() {
    let sim = fleet(2).await;
    sim.add_repo(Repo::public("owner/other"));
    sim.add_pull("owner/other", Pull::renovate("renovate/x", "Update"));
    let rt = runtime(&sim, memory());
    let only: Slug = "owner/other".parse().unwrap();
    assert_eq!(rt.backfill(Some(&only)).await.unwrap().found, 1);
}

#[tokio::test]
async fn a_scan_and_a_webhook_key_identically() {
    let sim = fleet(1).await;
    let webhook = sim_webhook(&sim, 1).await;
    let scanned = PullRequestOpened::webhook_payload(&rest(&sim, 1).await);
    let event_of = |p: &Value| PullRequestOpened::from_json(p).unwrap();
    assert_eq!(
        Watcher::key(&event_of(&webhook)),
        Watcher::key(&event_of(&scanned))
    );
}

/// The webhook GitHub would send for a pull request, captured from the simulator.
async fn sim_webhook(sim: &SimForge, number: u64) -> Value {
    let received = Arc::new(std::sync::Mutex::new(vec![]));
    let log = received.clone();
    let app = axum::Router::new().route(
        "/",
        axum::routing::post(move |body: axum::body::Bytes| {
            let log = log.clone();
            async move { log.lock().unwrap().push(body) }
        }),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, app).await });
    sim.deliver_to(&url, "s");
    sim.pull_request(REPO, number, "opened").await;
    let body = received.lock().unwrap()[0].clone();
    serde_json::from_slice(&body).unwrap()
}

#[tokio::test]
async fn a_webhook_arriving_after_a_scan_coalesces_on_the_same_key() {
    let sim = fleet(1).await;
    let webhook = sim_webhook(&sim, 1).await;
    let rt = runtime(&sim, memory());
    rt.backfill(None).await.unwrap();
    let ingest = rt
        .ingest(event(Origin::Webhook, "w1", webhook))
        .await
        .unwrap();
    assert_eq!(ingest, Ingest::Coalesced);
    assert_eq!(rt.drain().await.unwrap().len(), 1);
}

async fn decision(sim: &SimForge, origin: Origin, payload: Value) -> Vec<String> {
    let rt = runtime(sim, memory());
    rt.ingest(event(origin, "d", payload)).await.unwrap();
    let outcomes = rt.drain().await.unwrap();
    let Outcome::Ran { run_id, .. } = &outcomes[0] else {
        panic!("{outcomes:?}");
    };
    let entries = rt.effects_of(run_id).await.unwrap();
    entries.iter().map(|e| e.effect.to_string()).collect()
}

#[tokio::test]
async fn all_three_origins_produce_identical_decisions_for_the_same_state() {
    let sim = fleet(1).await;
    let webhook = sim_webhook(&sim, 1).await;
    let scanned = PullRequestOpened::webhook_payload(&rest(&sim, 1).await);
    let from_webhook = decision(&sim, Origin::Webhook, webhook.clone()).await;
    let from_scan = decision(&sim, Origin::Scan, scanned).await;
    let from_simulation = decision(&sim, Origin::Simulated, webhook).await;
    assert_eq!(from_webhook, from_scan);
    assert_eq!(from_webhook, from_simulation);
    assert!(
        from_webhook[0].contains("\"until\":\"labeled\""),
        "{from_webhook:?}"
    );
}

#[tokio::test]
async fn the_manifest_says_a_workflow_scans() {
    let sim = fleet(0).await;
    assert_eq!(runtime(&sim, memory()).manifest()[0]["scans"], true);
}

#[test]
fn a_state_in_a_delivery_id_lets_a_scan_reconsider_an_unchanged_commit() {
    let repo: Slug = REPO.parse().unwrap();
    let head: Sha = "a".repeat(40).parse().unwrap();
    let rest = serde_json::json!({});
    let first: Scanned<PullRequestOpened> =
        Scanned::pull(&repo, 1, &head, &rest).at_state("waiting");
    let second: Scanned<PullRequestOpened> =
        Scanned::pull(&repo, 1, &head, &rest).at_state("ready");
    let id = |s: &Scanned<PullRequestOpened>| format!("{:?}", s.id());
    assert_ne!(id(&first), id(&second));
}

#[tokio::test]
async fn two_scans_cannot_overlap() {
    let sim = fleet(1).await;
    let rt = runtime(&sim, memory());
    let (first, second) = tokio::join!(rt.backfill(None), rt.backfill(None));
    let reports = [first.unwrap(), second.unwrap()];
    let skipped = reports.iter().filter(|r| r.skipped).count();
    assert_eq!(skipped, 1, "{reports:?}");
}

#[tokio::test]
async fn a_scheduled_scan_runs_when_due_and_only_then() {
    let sim = fleet(1).await;
    let clock = FakeClock::at(time::macros::datetime!(2026-11-06 12:00 UTC));
    let rt = runtime(&sim, memory()).with_clock(Arc::new(clock.clone()));
    assert!(rt.tick().await.unwrap().is_some(), "due at start");
    assert!(rt.tick().await.unwrap().is_none());
    clock.advance(rt.limits().scan_interval);
    assert!(rt.tick().await.unwrap().is_some());
}

#[tokio::test]
async fn config_can_lengthen_the_scan_interval_and_never_shorten_it() {
    let sim = fleet(0).await;
    let rt = runtime(&sim, memory());
    let longer = rt.limits().scan_interval * 2;
    let lengthened = runtime(&sim, memory()).tightened(almanac::runtime::Limits {
        scan_interval: longer,
        ..Default::default()
    });
    assert_eq!(lengthened.limits().scan_interval, longer);
    let shorter = lengthened.tightened(almanac::runtime::Limits {
        scan_interval: std::time::Duration::from_secs(1),
        ..Default::default()
    });
    assert_eq!(shorter.limits().scan_interval, longer);
}
