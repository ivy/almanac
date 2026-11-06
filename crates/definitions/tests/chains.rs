//! Chain containment against simforge's real delivery shapes.
use almanac::clock::FakeClock;
use almanac::prelude::*;
use almanac::runtime::{Envelope, Ingest, LEDGER_WINDOW, Origin, Outcome, Runtime};
use almanac::store::{MemoryStore, Store};
use almanac::testing::{FakeTracker, ScriptedHarness};
use axum::body::Bytes;
use axum::routing::post;
use github::client::{Auth, GithubClient};
use github::forge::{CommitAuthor, GithubForge};
use github::{CheckSuiteCompleted, Status};
use serde_json::{Value, json};
use simforge::{Pull, Repo, SimForge, Suite};
use std::sync::{Arc, Mutex};
use std::time::Duration;

const REPO: &str = "owner/repo";
const BRANCH: &str = "renovate/tar-7.x";
const ME: &str = "almanac[bot]";
const ATTEMPT_CAP: u32 = 3;

#[prompt(text = "Repair this build.")]
pub struct RepairPrompt;

const REPAIRER: AgentSpec<Fixer, RepairPrompt> = AgentSpec::fixer("repairer");

pub struct Repair {
    repo: Slug,
    pull: u64,
    branch: Slug,
    head: Sha,
    attempts: Attempts,
    patch: Option<Untrusted<Patch>>,
}

fn repair_or_ask(r: &Repair) -> Vec<Effect> {
    match &r.patch {
        Some(patch) => match DEFAULT_POLICY.check(patch, &r.head) {
            Ok(patch) => vec![Effect::Push {
                repo: r.repo.clone(),
                pull: r.pull,
                branch: r.branch.clone(),
                message: "fix: repair the build",
                hold: None,
                patch,
            }],
            Err(_) => vec![],
        },
        None if r.attempts.asked => vec![],
        None => vec![Effect::Ask {
            question: "Repairs did not fix this build",
            target: r.repo.clone(),
            body: Untrusted::new(String::new()),
        }],
    }
}

/// Keyed per repository, so one key spans a whole repair loop.
#[workflow(
    on = CheckSuiteCompleted,
    key = "{repo}",
    intent = "Repair a red build.",
    may = [Push, Ask],
    runs_per_hour = 1000,
    decide = repair_or_ask,
)]
async fn repair_per_repo(cx: &Cx, event: &CheckSuiteCompleted) -> Result<Repair, Halt> {
    let attempts = cx.attempts().await?;
    let branch: Slug = BRANCH.parse().map_err(StepError::from)?;
    let mut patch = None;
    if attempts.used < ATTEMPT_CAP {
        let old = Published::At(time::macros::datetime!(2020-01-01 0:00 UTC));
        let read = async move { Ok(old) };
        let cooled = cx.cooled(Duration::ZERO, read).await?;
        let Some(cooled) = cooled else {
            return Err(Halt::Stop("not cooled".to_string()));
        };
        let input = event.suite_id;
        let repaired = cx
            .agent(REPAIRER)
            .repair(RepairPrompt, &input, &cooled)
            .await?;
        patch = Some(repaired);
    }
    Ok(Repair {
        repo: event.repo.clone(),
        pull: 1,
        branch,
        head: event.head_sha.clone(),
        attempts,
        patch,
    })
}

#[workflow(on = Status, key = "{repo}", intent = "Note a status.", may = [Await], runs_per_hour = 1000, decide = wait)]
async fn notes_status(_cx: &Cx, _event: &Status) -> Result<(), Halt> {
    Ok(())
}

fn wait(_: &()) -> Vec<Effect> {
    vec![Effect::Await { until: "nothing" }]
}

/// Webhooks as simforge delivers them.
async fn capture(sim: &SimForge) -> Arc<Mutex<Vec<Bytes>>> {
    let received: Arc<Mutex<Vec<Bytes>>> = Arc::default();
    let log = received.clone();
    let app = axum::Router::new().route(
        "/",
        post(move |body: Bytes| {
            let log = log.clone();
            async move { log.lock().unwrap().push(body) }
        }),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, app).await });
    sim.deliver_to(&url, "s");
    received
}

struct Loop {
    sim: SimForge,
    received: Arc<Mutex<Vec<Bytes>>>,
    tracker: FakeTracker,
    harness: ScriptedHarness,
    clock: FakeClock,
    store: Arc<dyn Store>,
    next: u32,
}

impl Loop {
    async fn new() -> Self {
        let sim = SimForge::start().await;
        sim.add_repo(Repo::public(REPO));
        sim.add_pull(REPO, Pull::renovate(BRANCH, "Update tar"));
        let received = capture(&sim).await;
        let reply = json!({"changes": [{"Write": {
            "path": "src/main.rs", "content": "eA==", "executable": false,
        }}], "diff": ""});
        Self {
            sim,
            received,
            tracker: FakeTracker::default(),
            harness: ScriptedHarness::replying(reply),
            clock: FakeClock::at(time::macros::datetime!(2026-11-06 12:00 UTC)),
            store: Arc::new(MemoryStore::default()),
            next: 0,
        }
    }

    fn runtime(&self, workflows: Vec<Box<dyn Registered>>) -> Runtime {
        let client = GithubClient::new(self.sim.base_url(), Auth::Token("t".to_string()));
        let forge = GithubForge::new(client, CommitAuthor::default());
        let forge: Arc<dyn Forge> = Arc::new(forge);
        let tracker: Arc<dyn Tracker> = Arc::new(self.tracker.clone());
        let providers = Providers::new()
            .provide::<dyn Forge>(forge)
            .provide::<dyn Tracker>(tracker)
            .harness(self.harness.clone());
        Runtime::new(workflows, providers)
            .with_store(self.store.clone())
            .with_clock(Arc::new(self.clock.clone()))
            .in_mode(Mode::Live)
            .acting_as(&[ME])
    }

    /// CI finishes red on the branch's current head, sent by another actor.
    async fn ci_goes_red(&mut self, rt: &Runtime) -> Ingest {
        let sha = self.sim.pull(REPO, 1).head_sha;
        let suite = Suite::finished(&sha, BRANCH, "failure", "error: boom");
        let id = self.sim.add_suite(REPO, suite);
        self.sim.suite_completed(REPO, id).await;
        let body = self.received.lock().unwrap().last().unwrap().clone();
        self.next += 1;
        rt.ingest(Envelope {
            source: "github".to_string(),
            delivery_id: format!("d{}", self.next),
            kind: "github.check_suite.completed".to_string(),
            depth: 0,
            origin: Origin::Webhook,
            payload: serde_json::from_slice(&body).unwrap(),
        })
        .await
        .unwrap()
    }

    fn pushes(&self) -> usize {
        let is_ref = |w: &simforge::Write| w.method == "PATCH";
        self.sim.writes().iter().filter(|w| is_ref(w)).count()
    }

    fn asks(&self) -> usize {
        self.tracker.filed.lock().unwrap().len()
    }
}

fn status_payload(sender: &str) -> Value {
    json!({
        "sha": "a".repeat(40), "state": "success", "context": "almanac/trust",
        "repository": {"full_name": REPO}, "sender": {"login": sender},
    })
}

fn status_event(delivery: &str, sender: &str) -> Envelope {
    Envelope {
        source: "github".to_string(),
        delivery_id: delivery.to_string(),
        kind: "github.status".to_string(),
        depth: 0,
        origin: Origin::Webhook,
        payload: status_payload(sender),
    }
}

#[tokio::test]
async fn a_direct_echo_does_not_start_a_run() {
    let l = Loop::new().await;
    let rt = l.runtime(vec![Box::new(NotesStatus)]);
    assert_eq!(
        rt.ingest(status_event("d1", ME)).await.unwrap(),
        Ingest::Echo
    );
    assert_eq!(rt.drain().await.unwrap().len(), 0);
    let other = rt.ingest(status_event("d2", "github-actions[bot]")).await;
    assert_eq!(other.unwrap(), Ingest::Queued);
}

#[tokio::test]
async fn a_repair_that_keeps_failing_makes_exactly_three_attempts_and_asks_once() {
    let mut l = Loop::new().await;
    let rt = l.runtime(vec![Box::new(RepairPerRepo)]);
    for _ in 0..8 {
        let _ = l.ci_goes_red(&rt).await;
        rt.drain().await.unwrap();
        rt.deliver().await.unwrap();
    }
    assert_eq!(l.pushes(), 3, "exactly the cap");
    assert_eq!(l.asks(), 1, "a person is asked once");
}

#[tokio::test]
async fn ci_after_a_push_inherits_depth_though_another_actor_sent_it() {
    let mut l = Loop::new().await;
    let rt = l.runtime(vec![Box::new(RepairPerRepo)]);
    let mut stopped = 0;
    for _ in 0..12 {
        if l.ci_goes_red(&rt).await == Ingest::ChainStopped {
            stopped += 1;
        }
        rt.drain().await.unwrap();
        rt.deliver().await.unwrap();
    }
    assert!(
        stopped > 0,
        "the chain must stop on depth alone, whoever sent the events"
    );
}

#[tokio::test]
async fn an_unrelated_event_after_the_ledger_window_starts_fresh() {
    let mut l = Loop::new().await;
    let rt = l.runtime(vec![Box::new(RepairPerRepo)]);
    for _ in 0..6 {
        let _ = l.ci_goes_red(&rt).await;
        rt.drain().await.unwrap();
        rt.deliver().await.unwrap();
    }
    assert_eq!(l.ci_goes_red(&rt).await, Ingest::ChainStopped);
    l.clock.advance(LEDGER_WINDOW + Duration::from_secs(1));
    assert_eq!(l.ci_goes_red(&rt).await, Ingest::Queued);
}

#[tokio::test]
async fn a_dry_run_leaves_no_trace_and_so_causes_no_depth() {
    let mut l = Loop::new().await;
    let rt = l
        .runtime(vec![Box::new(RepairPerRepo)])
        .in_mode(Mode::DryRun);
    for _ in 0..8 {
        assert_eq!(l.ci_goes_red(&rt).await, Ingest::Queued);
        let outcomes = rt.drain().await.unwrap();
        assert!(matches!(outcomes[0], Outcome::Ran { .. }), "{outcomes:?}");
    }
    assert_eq!(l.pushes(), 0);
}
