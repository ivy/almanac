//! The Fennel port end to end: the real runtime, the simulated forge, the GitHub forge, and a
//! scripted agent. Each scenario runs against the Rust workflow too, as a differential check.
use almanac::prelude::*;
use almanac::runtime::{Envelope, Ingest, Origin, Outcome, Runtime};
use almanac::testing::{FakeTracker, ScriptedHarness};
use almanac_renovate::update::{Ages, Update};
use almanac_renovate::workflows::build_failures::{BuildFailures, ScanTarget};
use github::client::{Auth, GithubClient};
use github::forge::{CommitAuthor, GithubForge};
use serde_json::{Value, json};
use simforge::{Pull, RENOVATE, Repo, SimForge, Suite};
use std::sync::{Arc, Mutex};
use workflows_in_fennel::build_failures;

const REPO: &str = "owner/repo";
const BRANCH: &str = "renovate/tar-7.x";
const BODY: &str = "| Package | Change |\n|---|---|\n| [tar](https://x) | [`1` → `7`](https://renovatebot.com/diffs/npm/tar/1.0.0/7.0.0) |\n";

#[derive(Clone, Copy, Debug)]
enum Written {
    InRust,
    InFennel,
}

const BOTH: [Written; 2] = [Written::InRust, Written::InFennel];

fn workflow(written: Written) -> Box<dyn Registered> {
    match written {
        Written::InRust => Box::new(BuildFailures),
        Written::InFennel => Box::new(build_failures().unwrap()),
    }
}

struct Aged(Published);

impl Ages for Aged {
    fn newest<'a>(
        &'a self,
        _: &'a Untrusted<Update>,
    ) -> BoxFuture<'a, Result<Published, StepError>> {
        Box::pin(async move { Ok(self.0) })
    }
}

fn hours_old(hours: i64) -> Published {
    Published::At(time::OffsetDateTime::now_utc() - time::Duration::hours(hours))
}

fn patch_reply(path: &str) -> Value {
    json!({"answer": {"summary": "fixed"}, "diff": "the diff", "changes": [
        {"Write": {"path": path, "content": "eA==", "executable": false}},
    ]})
}

struct World {
    sim: SimForge,
    harness: ScriptedHarness,
    tracker: FakeTracker,
    received: Arc<Mutex<Vec<axum::body::Bytes>>>,
    next: u32,
    rt: Runtime,
    number: u64,
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

async fn world_with(written: Written, published: Published, reply: Value, author: &str) -> World {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public(REPO));
    let mut pull = Pull::renovate(BRANCH, "Update tar");
    pull.body = Some(BODY.to_string());
    pull.author = author.to_string();
    let number = sim.add_pull(REPO, pull);
    let received = capture(&sim).await;
    let harness = ScriptedHarness::replying(reply);
    let tracker = FakeTracker::default();
    let client = || GithubClient::new(sim.base_url(), Auth::Token("t".to_string()));
    let forge: Arc<dyn Forge> = Arc::new(GithubForge::new(client(), CommitAuthor::default()));
    let tracked: Arc<dyn Tracker> = Arc::new(tracker.clone());
    let ages: Arc<dyn Ages> = Arc::new(Aged(published));
    let target = ScanTarget {
        owner: "owner".to_string(),
        author: RENOVATE.to_string(),
    };
    let providers = Providers::new()
        .provide::<GithubClient>(Arc::new(client()))
        .provide::<dyn Forge>(forge)
        .provide::<dyn Tracker>(tracked)
        .provide::<dyn Ages>(ages)
        .provide(Arc::new(target))
        .harness(harness.clone())
        .acting_as(&["almanac[bot]"]);
    let rt = Runtime::new(vec![workflow(written)], providers).in_mode(Mode::Live);
    rt.validate().unwrap();
    World {
        sim,
        harness,
        tracker,
        received,
        next: 0,
        rt,
        number,
    }
}

async fn world(written: Written, published: Published, reply: Value) -> World {
    world_with(written, published, reply, RENOVATE).await
}

impl World {
    fn head(&self) -> String {
        self.sim.pull(REPO, self.number).head_sha
    }

    async fn suite_finishes(&mut self, conclusion: &str) -> Ingest {
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

    async fn settle(&self) -> Vec<Outcome> {
        let outcomes = self.rt.drain().await.unwrap();
        self.rt.deliver().await.unwrap();
        outcomes
    }

    fn statuses(&self) -> Vec<(String, String)> {
        let writes = self.sim.writes();
        let posted = writes.iter().filter(|w| w.path.contains("/statuses/"));
        posted
            .map(|w| {
                let sha = w.path.rsplit('/').next().unwrap().to_string();
                (sha, w.body["state"].as_str().unwrap().to_string())
            })
            .collect()
    }

    fn pushes(&self) -> usize {
        let writes = self.sim.writes();
        writes.iter().filter(|w| w.method == "PATCH").count()
    }

    fn filed(&self) -> Vec<String> {
        let filed = self.tracker.filed.lock().unwrap();
        filed.iter().map(|(_, _, question)| question.clone()).collect()
    }
}

fn kinds(outcomes: &[Outcome]) -> Vec<EffectKind> {
    let Outcome::Ran { effects, denied, .. } = &outcomes[0] else {
        panic!("{outcomes:?}");
    };
    assert!(denied.is_empty(), "{denied:?}");
    effects.iter().map(Effect::kind).collect()
}

#[tokio::test]
async fn a_red_pull_request_past_its_cooldown_is_patched_then_goes_green() {
    for written in BOTH {
        let mut w = world(written, hours_old(100), patch_reply("src/main.rs")).await;
        let broken = w.head();
        assert_eq!(w.suite_finishes("failure").await, Ingest::Queued);
        let outcomes = w.settle().await;
        assert_eq!(kinds(&outcomes), [EffectKind::Push], "{written:?}");
        assert_eq!(w.harness.calls(), 1);
        assert_eq!(w.pushes(), 1);
        let patched = w.head();
        assert_ne!(patched, broken);
        let statuses = w.statuses();
        let pending = "pending".to_string();
        assert_eq!(statuses[0], (broken, pending.clone()), "held before the agent ran");
        assert_eq!(statuses[1], (patched.clone(), pending), "held on the new commit");

        assert_eq!(w.suite_finishes("success").await, Ingest::Queued);
        w.settle().await;
        let last = w.statuses().last().unwrap().clone();
        assert_eq!(last, (patched, "success".to_string()), "{written:?}");
        assert_eq!(w.harness.calls(), 1, "a green build runs no agent");
    }
}

#[tokio::test]
async fn a_pull_request_inside_its_cooldown_reaches_no_agent_and_awaits() {
    for written in BOTH {
        let mut w = world(written, hours_old(10), patch_reply("src/main.rs")).await;
        w.suite_finishes("failure").await;
        let outcomes = w.settle().await;
        assert_eq!(kinds(&outcomes), [EffectKind::Await], "{written:?}");
        assert_eq!(w.harness.calls(), 0, "no agent reads it");
        assert_eq!(w.statuses(), vec![], "nothing is held for a run that does nothing");
    }
}

#[tokio::test]
async fn an_update_with_no_publish_time_awaits() {
    for written in BOTH {
        let mut w = world(written, Published::Unknown, patch_reply("src/main.rs")).await;
        w.suite_finishes("failure").await;
        let outcomes = w.settle().await;
        assert_eq!(kinds(&outcomes), [EffectKind::Await], "{written:?}");
        assert_eq!(w.harness.calls(), 0);
    }
}

#[tokio::test]
async fn a_rejected_patch_pushes_nothing_and_asks_a_person() {
    for written in BOTH {
        let mut w = world(written, hours_old(100), patch_reply(".github/workflows/ci.yml")).await;
        let broken = w.head();
        w.suite_finishes("failure").await;
        let outcomes = w.settle().await;
        let expected = [EffectKind::Attest, EffectKind::Ask];
        assert_eq!(kinds(&outcomes), expected, "{written:?}");
        assert_eq!(w.pushes(), 0, "{written:?}");
        assert_eq!(w.filed(), ["A repair was refused by the patch policy"]);
        let last = w.statuses().last().unwrap().clone();
        assert_eq!(last, (broken, "failure".to_string()), "{written:?}");
    }
}

#[tokio::test]
async fn green_builds_and_other_branches_produce_no_run() {
    for written in BOTH {
        let mut w = world(written, hours_old(100), patch_reply("src/main.rs")).await;
        assert_eq!(w.suite_finishes("success").await, Ingest::Ignored, "{written:?}");
        let sha = w.head();
        let suite = Suite::finished(&sha, "feature", "failure", "x");
        let id = w.sim.add_suite(REPO, suite);
        w.sim.suite_completed(REPO, id).await;
        let ingest = w.deliver("github.check_suite.completed").await;
        assert_eq!(ingest, Ingest::Ignored, "{written:?}");
    }
}

#[tokio::test]
async fn several_check_suites_on_one_commit_start_one_run() {
    for written in BOTH {
        let mut w = world(written, hours_old(100), patch_reply("src/main.rs")).await;
        assert_eq!(w.suite_finishes("failure").await, Ingest::Queued);
        assert_eq!(w.suite_finishes("failure").await, Ingest::Coalesced);
        w.settle().await;
        assert_eq!(w.harness.calls(), 1, "{written:?}");
    }
}

#[tokio::test]
async fn a_status_works_like_a_check_suite_and_almanacs_own_status_does_not() {
    for written in BOTH {
        let mut w = world(written, hours_old(100), patch_reply("src/main.rs")).await;
        let sha = w.head();
        w.sim.status_changed(REPO, &sha, "almanac/trust", "failure").await;
        assert_eq!(w.deliver("github.status").await, Ingest::Ignored, "{written:?}");
        w.sim.status_changed(REPO, &sha, "ci/build", "failure").await;
        assert_eq!(w.deliver("github.status").await, Ingest::Queued, "{written:?}");
    }
}

#[tokio::test]
async fn a_repair_that_keeps_failing_stops_at_the_cap_and_asks_once() {
    for written in BOTH {
        let mut w = world(written, hours_old(100), patch_reply("src/main.rs")).await;
        for _ in 0..8 {
            w.suite_finishes("failure").await;
            w.settle().await;
        }
        assert_eq!(w.pushes(), 3, "{written:?}");
        assert_eq!(w.filed(), ["Repairs did not fix this build"], "{written:?}");
    }
}

#[tokio::test]
async fn a_persons_branch_named_like_renovates_is_never_repaired() {
    for written in BOTH {
        let reply = patch_reply("src/main.rs");
        let mut w = world_with(written, hours_old(100), reply, "someone").await;
        w.suite_finishes("failure").await;
        let outcomes = w.settle().await;
        assert_eq!(kinds(&outcomes), [], "{written:?}");
        assert_eq!(w.harness.calls(), 0, "{written:?}");
    }
}

#[tokio::test]
async fn the_logs_reach_the_agent_tainted_and_the_checkout_by_reference() {
    for written in BOTH {
        let mut w = world(written, hours_old(100), patch_reply("src/main.rs")).await;
        w.suite_finishes("failure").await;
        w.settle().await;
        let inputs = w.harness.inputs.lock().unwrap();
        let logs = &inputs[0]["logs"]["$untrusted"];
        assert!(logs.as_str().unwrap().contains("mismatched types"), "{logs}");
        assert_eq!(inputs[0]["$checkout"]["repo"], REPO);
        let prompts = w.harness.prompts.lock().unwrap();
        assert!(prompts[0].contains(REPO), "{written:?}");
        assert!(!prompts[0].contains("mismatched"), "logs are never prompt text");
        let grants = w.harness.grants.lock().unwrap();
        assert!(grants[0].write && !grants[0].egress && !grants[0].secrets);
    }
}

#[tokio::test]
async fn both_workflows_send_an_agent_the_same_request() {
    let mut seen = vec![];
    for written in BOTH {
        let mut w = world(written, hours_old(100), patch_reply("src/main.rs")).await;
        w.suite_finishes("failure").await;
        w.settle().await;
        let mut input = w.harness.inputs.lock().unwrap()[0].clone();
        input["$checkout"]["sha"] = json!("varies by run");
        let prompt = w.harness.prompts.lock().unwrap()[0].clone();
        seen.push((input, prompt));
    }
    assert_eq!(seen[0], seen[1]);
}

#[tokio::test]
async fn a_replay_repeats_the_decision_without_calling_anything_again() {
    let mut w = world(Written::InFennel, hours_old(100), patch_reply("src/main.rs")).await;
    w.suite_finishes("failure").await;
    let outcomes = w.settle().await;
    let Outcome::Ran { run_id, effects, .. } = &outcomes[0] else {
        panic!("{outcomes:?}");
    };
    let requests = w.sim.writes().len();
    let replayed = w.rt.replay(run_id).await.unwrap();
    let Outcome::Ran { effects: again, .. } = &replayed else {
        panic!("{replayed:?}");
    };
    assert_eq!(format!("{effects:?}"), format!("{again:?}"));
    assert_eq!(w.harness.calls(), 1, "the model is not called again");
    assert_eq!(w.sim.writes().len(), requests, "nothing is written again");
}
