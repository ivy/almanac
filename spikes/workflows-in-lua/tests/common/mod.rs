//! The world a Renovate pull request lives in, shared by every test that runs one for real. It is
//! the setup in `almanacs/renovate/tests/build_failures.rs`, with the workflow as a parameter.
#![allow(dead_code)]
pub mod script;

use almanac::prelude::*;
use almanac::runtime::{Envelope, Ingest, Origin, Outcome, Runtime};
use almanac::store::{MemoryStore, Store};
use almanac::testing::{FakeTracker, ScriptedHarness};
use almanac_renovate::update::{Ages, Update};
use almanac_renovate::workflows::build_failures::{BuildFailures, ScanTarget};
use github::client::{Auth, GithubClient};
use github::forge::{CommitAuthor, GithubForge};
use serde_json::{Value, json};
use simforge::{Pull, RENOVATE, Repo, SimForge, Suite};
use std::sync::{Arc, Mutex};
use workflows_in_lua::renovate::build_failures;

pub const REPO: &str = "owner/repo";
pub const BRANCH: &str = "renovate/tar-7.x";
pub const BODY: &str = "| Package | Change |\n|---|---|\n| [tar](https://x) | [`1` → `7`](https://renovatebot.com/diffs/npm/tar/1.0.0/7.0.0) |\n";

pub struct Aged(pub Published);

impl Ages for Aged {
    fn newest<'a>(
        &'a self,
        _: &'a Untrusted<Update>,
    ) -> BoxFuture<'a, Result<Published, StepError>> {
        Box::pin(async move { Ok(self.0) })
    }
}

pub fn hours_old(hours: i64) -> Published {
    Published::At(time::OffsetDateTime::now_utc() - time::Duration::hours(hours))
}

pub fn patch_reply(path: &str) -> Value {
    json!({"answer": {"summary": "fixed"}, "diff": "the diff", "changes": [
        {"Write": {"path": path, "content": "eA==", "executable": false}},
    ]})
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Language {
    Rust,
    Lua,
}

impl Language {
    pub fn workflow(self) -> Box<dyn Registered> {
        match self {
            Self::Rust => Box::new(BuildFailures),
            Self::Lua => Box::new(build_failures().expect("the workflow loads")),
        }
    }
}

pub struct World {
    pub sim: SimForge,
    pub harness: ScriptedHarness,
    pub tracker: FakeTracker,
    pub received: Arc<Mutex<Vec<axum::body::Bytes>>>,
    pub next: u32,
    pub rt: Runtime,
    pub number: u64,
    pub store: Arc<dyn Store>,
    published: Published,
}

async fn capture(sim: &SimForge) -> Arc<Mutex<Vec<axum::body::Bytes>>> {
    let received: Arc<Mutex<Vec<axum::body::Bytes>>> = Arc::default();
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
    received
}

fn providers(
    sim: &SimForge,
    harness: &ScriptedHarness,
    tracker: &FakeTracker,
    published: Published,
) -> Providers {
    let client = || GithubClient::new(sim.base_url(), Auth::Token("t".to_string()));
    let forge: Arc<dyn Forge> = Arc::new(GithubForge::new(client(), CommitAuthor::default()));
    let tracked: Arc<dyn Tracker> = Arc::new(tracker.clone());
    let ages: Arc<dyn Ages> = Arc::new(Aged(published));
    let target = ScanTarget {
        owner: "owner".to_string(),
        author: RENOVATE.to_string(),
    };
    Providers::new()
        .provide::<GithubClient>(Arc::new(client()))
        .provide::<dyn Forge>(forge)
        .provide::<dyn Tracker>(tracked)
        .provide::<dyn Ages>(ages)
        .provide(Arc::new(target))
        .harness(harness.clone())
        .acting_as(&["almanac[bot]"])
}

pub async fn world(language: Language, published: Published, reply: Value) -> World {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public(REPO));
    let mut pull = Pull::renovate(BRANCH, "Update tar");
    pull.body = Some(BODY.to_string());
    let number = sim.add_pull(REPO, pull);
    let received = capture(&sim).await;
    let harness = ScriptedHarness::replying(reply);
    let tracker = FakeTracker::default();
    let store: Arc<dyn Store> = Arc::new(MemoryStore::default());
    let rt = Runtime::new(
        vec![language.workflow()],
        providers(&sim, &harness, &tracker, published),
    )
    .with_store(store.clone())
    .in_mode(Mode::Live);
    World {
        sim,
        harness,
        tracker,
        received,
        next: 0,
        rt,
        number,
        store,
        published,
    }
}

impl World {
    pub fn head(&self) -> String {
        self.sim.pull(REPO, self.number).head_sha
    }

    pub async fn suite_finishes(&mut self, conclusion: &str) -> Ingest {
        let sha = self.head();
        let suite = Suite::finished(&sha, BRANCH, conclusion, "error[E0308]: mismatched types");
        let id = self.sim.add_suite(REPO, suite);
        self.sim.suite_completed(REPO, id).await;
        self.deliver("github.check_suite.completed").await
    }

    async fn deliver(&mut self, kind: &str) -> Ingest {
        let body = self.received.lock().unwrap().last().unwrap().clone();
        self.next += 1;
        self.rt
            .ingest(Envelope {
                source: "github".to_string(),
                delivery_id: format!("d{}", self.next),
                kind: kind.to_string(),
                depth: 0,
                origin: Origin::Webhook,
                payload: serde_json::from_slice(&body).unwrap(),
            })
            .await
            .unwrap()
    }

    pub async fn settle(&self) -> Vec<Outcome> {
        let outcomes = self.rt.drain().await.unwrap();
        self.rt.deliver().await.unwrap();
        outcomes
    }

    pub fn statuses(&self) -> Vec<(String, String)> {
        let writes = self.sim.writes();
        let posted = writes.iter().filter(|w| w.path.contains("/statuses/"));
        posted
            .map(|w| {
                let sha = w.path.rsplit('/').next().unwrap().to_string();
                (sha, w.body["state"].as_str().unwrap().to_string())
            })
            .collect()
    }

    pub fn pushes(&self) -> usize {
        let writes = self.sim.writes();
        writes.iter().filter(|w| w.method == "PATCH").count()
    }

    /// Another runtime on the same store and forge, running `language`'s version of the workflow.
    pub fn runtime_for(&self, language: Language) -> Runtime {
        let providers = providers(&self.sim, &self.harness, &self.tracker, self.published);
        Runtime::new(vec![language.workflow()], providers)
            .with_store(self.store.clone())
            .in_mode(Mode::Live)
    }
}

pub fn run_id(outcome: &Outcome) -> String {
    match outcome {
        Outcome::Ran { run_id, .. } => run_id.clone(),
        other => panic!("expected a finished run, got {other:?}"),
    }
}

pub fn kinds(outcome: &Outcome) -> Vec<EffectKind> {
    match outcome {
        Outcome::Ran { effects, .. } => effects.iter().map(Effect::kind).collect(),
        other => panic!("expected a finished run, got {other:?}"),
    }
}
