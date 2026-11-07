//! The Lua workflow end to end against the simulated forge, with a scripted agent. Most of these
//! are the compiled workflow's own tests, run on the port.
mod common;

use almanac::prelude::*;
use almanac::runtime::{Ingest, Outcome};
use almanac_renovate::fleet::ATTEMPT_CAP;
use common::Language::{Lua, Rust};
use common::*;
use serde_json::{Value, json};
use simforge::{Failure, Suite};
use workflows_in_lua::renovate::build_failures;
use workflows_in_lua::taint::Tainted;
use workflows_in_lua::value::Node;

#[tokio::test]
async fn a_red_pull_request_past_its_cooldown_is_patched_and_goes_pending_then_success() {
    let mut w = world(Lua, hours_old(100), patch_reply("src/main.rs")).await;
    let broken = w.head();
    assert_eq!(w.suite_finishes("failure").await, Ingest::Queued);
    w.settle().await;
    assert_eq!(w.harness.calls(), 1);
    assert_eq!(w.pushes(), 1);
    let patched = w.head();
    assert_ne!(patched, broken);
    let statuses = w.statuses();
    assert_eq!(
        statuses[0],
        (broken, "pending".to_string()),
        "held before the agent ran"
    );
    assert_eq!(
        statuses[1],
        (patched.clone(), "pending".to_string()),
        "held on the new commit"
    );

    assert_eq!(w.suite_finishes("success").await, Ingest::Queued);
    w.settle().await;
    let statuses = w.statuses();
    assert_eq!(statuses.last().unwrap(), &(patched, "success".to_string()));
    assert_eq!(w.harness.calls(), 1, "a green build runs no agent");
}

#[tokio::test]
async fn a_broken_update_inside_its_cooldown_reaches_no_agent_and_awaits() {
    let mut w = world(Lua, hours_old(10), patch_reply("src/main.rs")).await;
    w.suite_finishes("failure").await;
    let outcomes = w.settle().await;
    assert_eq!(kinds(&outcomes[0]), [EffectKind::Await]);
    assert_eq!(w.harness.calls(), 0, "no agent reads it");
    assert_eq!(
        w.statuses(),
        vec![],
        "nothing is held for a run that does nothing"
    );
}

#[tokio::test]
async fn a_rejected_patch_asks_a_person_and_pushes_nothing() {
    let mut w = world(Lua, hours_old(100), patch_reply(".github/workflows/ci.yml")).await;
    let broken = w.head();
    w.suite_finishes("failure").await;
    w.settle().await;
    assert_eq!(w.pushes(), 0);
    assert_eq!(w.tracker.filed.lock().unwrap().len(), 1);
    assert_eq!(
        w.statuses().last().unwrap(),
        &(broken.clone(), "failure".to_string())
    );
    assert_eq!(w.head(), broken, "the branch did not move");
}

#[tokio::test]
async fn an_update_with_no_publish_time_awaits() {
    let mut w = world(Lua, Published::Unknown, patch_reply("src/main.rs")).await;
    w.suite_finishes("failure").await;
    w.settle().await;
    assert_eq!(w.harness.calls(), 0);
}

#[tokio::test]
async fn green_pull_requests_produce_no_run() {
    let mut w = world(Lua, hours_old(100), patch_reply("src/main.rs")).await;
    assert_eq!(w.suite_finishes("success").await, Ingest::Ignored);
    assert!(w.settle().await.is_empty());
}

#[tokio::test]
async fn several_check_suites_on_one_commit_start_one_run() {
    let mut w = world(Lua, hours_old(100), patch_reply("src/main.rs")).await;
    assert_eq!(w.suite_finishes("failure").await, Ingest::Queued);
    assert_eq!(w.suite_finishes("failure").await, Ingest::Coalesced);
    w.settle().await;
    assert_eq!(w.harness.calls(), 1);
}

#[tokio::test]
async fn a_repair_that_keeps_failing_stops_at_the_cap_and_asks_once() {
    let mut w = world(Lua, hours_old(100), patch_reply("src/main.rs")).await;
    for _ in 0..8 {
        w.suite_finishes("failure").await;
        w.settle().await;
    }
    assert_eq!(w.pushes() as u32, ATTEMPT_CAP);
    assert_eq!(w.tracker.filed.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn a_backfill_finds_a_red_pull_request_and_repairs_it() {
    let w = world(Lua, hours_old(100), patch_reply("src/main.rs")).await;
    let sha = w.head();
    w.sim
        .add_suite(REPO, Suite::finished(&sha, BRANCH, "failure", "boom"));
    let report = w.rt.backfill(None).await.unwrap();
    assert_eq!((report.found, report.queued), (1, 1));
    w.settle().await;
    assert_eq!(w.pushes(), 1);
}

#[tokio::test]
async fn the_logs_reach_the_agent_as_tainted_input_and_the_checkout_by_reference() {
    let mut w = world(Lua, hours_old(100), patch_reply("src/main.rs")).await;
    w.suite_finishes("failure").await;
    w.settle().await;
    let inputs = w.harness.inputs.lock().unwrap();
    let logs = &inputs[0]["logs"]["$untrusted"];
    assert!(
        logs.as_str().unwrap().contains("mismatched types"),
        "{logs}"
    );
    assert_eq!(inputs[0]["$checkout"]["repo"], REPO);
    let prompts = w.harness.prompts.lock().unwrap();
    assert!(
        !prompts[0].contains("mismatched"),
        "logs are never prompt text"
    );
    assert!(prompts[0].contains(REPO), "the prompt names the repository");
}

/// Every scenario, through both versions, must record the same effects and the same agent traffic.
#[tokio::test]
async fn the_lua_and_rust_versions_record_the_same_effects() {
    let scenarios = [
        ("patched", hours_old(100), "src/main.rs"),
        ("cooling", hours_old(10), "src/main.rs"),
        ("unknown", Published::Unknown, "src/main.rs"),
        ("refused", hours_old(100), ".github/workflows/ci.yml"),
    ];
    for (name, published, path) in scenarios {
        let mut seen = vec![];
        for language in [Rust, Lua] {
            let mut w = world(language, published, patch_reply(path)).await;
            w.suite_finishes("failure").await;
            let outcomes = w.settle().await;
            let id = run_id(&outcomes[0]);
            let effects = w.rt.effects_of(&id).await.unwrap();
            let effects: Vec<Value> = effects.into_iter().map(|e| e.effect).collect();
            let inputs = w.harness.inputs.lock().unwrap().clone();
            let prompts = w.harness.prompts.lock().unwrap().clone();
            let grants = w.harness.grants.lock().unwrap().clone();
            seen.push((effects, inputs, prompts, grants));
        }
        assert_eq!(seen[0], seen[1], "{name}");
    }
}

#[tokio::test]
async fn a_replay_reuses_what_was_recorded_and_reaches_the_same_effects() {
    let mut w = world(Lua, hours_old(100), patch_reply("src/main.rs")).await;
    w.suite_finishes("failure").await;
    let outcomes = w.settle().await;
    let id = run_id(&outcomes[0]);
    let writes = w.sim.writes().len();

    // Nothing recorded is read again, so an outage cannot matter.
    let down: Vec<Failure> = vec![Failure::ServerError(500); 20];
    w.sim.fail_requests("/repos", &down);
    let replayed = w.rt.replay(&id).await.unwrap();
    assert_eq!(kinds(&replayed), [EffectKind::Push]);
    assert_eq!(w.harness.calls(), 1, "the model is not called again");
    assert_eq!(w.sim.writes().len(), writes, "nothing is written again");
}

/// The two versions record under the same names and types, so either can explain the other's run.
#[tokio::test]
async fn each_version_can_replay_a_run_the_other_recorded() {
    for (ran, replays) in [(Lua, Rust), (Rust, Lua)] {
        let mut w = world(ran, hours_old(100), patch_reply("src/main.rs")).await;
        w.suite_finishes("failure").await;
        let outcomes = w.settle().await;
        let replayed = w.runtime_for(replays).replay(&run_id(&outcomes[0])).await;
        assert_eq!(kinds(&replayed.unwrap()), [EffectKind::Push], "{ran:?}");
        assert_eq!(w.harness.calls(), 1);
    }
}

#[test]
fn the_manifest_matches_the_compiled_workflows() {
    let sorted = |mut manifest: Value| {
        let requires = manifest["requires"].as_array_mut().unwrap();
        requires.sort_by_key(|need| need["name"].as_str().unwrap().to_string());
        manifest
    };
    let lua = sorted(Lua.workflow().manifest());
    let rust = sorted(Rust.workflow().manifest());
    assert_eq!(lua, rust);
}

fn repo() -> Node {
    Node::text(REPO)
}

fn head() -> Node {
    Node::text("a".repeat(40))
}

fn repaired(path: &str) -> Node {
    let changes = vec![Change::Write {
        path: path.to_string(),
        content: "eA==".to_string(),
        executable: false,
    }];
    let patch = Untrusted::new(Patch {
        changes,
        diff: String::new(),
    });
    Node::map([
        ("kind", Node::text("Repaired")),
        ("repo", repo()),
        ("number", Node::Num(1.0)),
        ("branch", Node::text(BRANCH)),
        ("head", head()),
        ("patch", Node::Tainted(Tainted::Patch(patch))),
    ])
}

/// Policy is a function of facts, so it is table-testable with no runtime, as in Rust.
#[test]
fn the_policy_table() {
    use EffectKind::*;
    let kind = |name: &str| Node::map([("kind", Node::text(name))]);
    let exhausted = |asked| {
        Node::map([
            ("kind", Node::text("Exhausted")),
            ("repo", repo()),
            ("head", head()),
            ("asked", Node::Bool(asked)),
        ])
    };
    let passed = Node::map([
        ("kind", Node::text("Passed")),
        ("repo", repo()),
        ("head", head()),
    ]);
    let cases: [(Node, &[EffectKind]); 7] = [
        (kind("Nothing"), &[]),
        (kind("Cooling"), &[Await]),
        (passed, &[Attest]),
        (exhausted(false), &[Attest, Ask]),
        (exhausted(true), &[]),
        (repaired("src/main.rs"), &[Push]),
        (repaired(".github/workflows/ci.yml"), &[Attest, Ask]),
    ];
    let workflow = build_failures().unwrap();
    for (facts, expected) in cases {
        let effects = workflow.decide(&facts).unwrap();
        let found: Vec<_> = effects.iter().map(Effect::kind).collect();
        assert_eq!(found, expected);
    }
}

#[test]
fn a_status_and_a_check_suite_identify_the_same_key() {
    let sha = "a".repeat(40);
    let suite = json!({
        "repository": {"full_name": REPO},
        "check_suite": {"head_sha": sha, "head_branch": BRANCH, "conclusion": "failure",
                        "head_commit": {"message": "Update"}},
    });
    let status = json!({
        "repository": {"full_name": REPO}, "sha": sha, "state": "failure", "context": "ci",
        "branches": [{"name": BRANCH}], "commit": {"commit": {"message": "Update"}},
    });
    let workflow = build_failures().unwrap();
    let a = workflow.identify(&suite).unwrap();
    let b = workflow.identify(&status).unwrap();
    assert_eq!(a.key, b.key);
    assert_eq!(a.key, format!("{REPO}:{BRANCH}"));
    assert!(a.wanted && b.wanted);
    assert_eq!(a.subjects[0].as_str(), format!("repo:{REPO}"));
}

#[tokio::test]
async fn a_failed_read_fails_the_run_and_changes_nothing() {
    let mut w = world(Lua, hours_old(100), patch_reply("src/main.rs")).await;
    w.suite_finishes("failure").await;
    w.sim
        .fail_requests("/pulls", &vec![Failure::ServerError(500); 20]);
    let outcomes = w.settle().await;
    assert!(
        matches!(outcomes[0], Outcome::Failed { .. }),
        "{outcomes:?}"
    );
    assert_eq!(w.harness.calls(), 0);
    assert_eq!(w.pushes(), 0);
}
