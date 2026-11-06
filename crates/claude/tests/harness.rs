//! Drives the harness with a shell script in place of the CLI, so these need nothing installed.
use almanac::agent::Grant;
use claude::{ClaudeConfig, ClaudeHarness, WORKSPACE_FIELD};
use sandbox::{ExecOutput, FakeSandbox, SandboxSpec};
use serde_json::{Value, json};
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::sync::Arc;
use std::time::{Duration, Instant};

const READ: Grant = Grant {
    write: false,
    egress: false,
    secrets: false,
};
const WRITE: Grant = Grant {
    write: true,
    egress: false,
    secrets: false,
};

fn cli(dir: &Path, body: &str) -> ClaudeConfig {
    let path = dir.join("claude");
    let log = dir.join("stdin");
    let script = format!("#!/bin/sh\ncat > {}\n{body}\n", log.display());
    std::fs::write(&path, script).unwrap();
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
    let mut config = ClaudeConfig::new("key");
    config.binary = path.to_string_lossy().into_owned();
    config
}

fn answers(text: &str) -> String {
    let event = json!({"type": "result", "is_error": false, "result": text});
    format!("echo '{event}'")
}

async fn read(config: ClaudeConfig) -> Result<Value, String> {
    let harness = ClaudeHarness::new(config);
    harness
        .session("Review this.", READ, json!({"diff": "x"}))
        .await
        .map_err(|e| e.0)
}

#[tokio::test]
async fn an_answer_is_typed_json() {
    let dir = tempfile::tempdir().unwrap();
    let config = cli(
        dir.path(),
        &answers(r#"{\"verdict\": \"ok\"}"#.replace('\\', "").as_str()),
    );
    assert_eq!(read(config).await.unwrap(), json!({"verdict": "ok"}));
}

#[tokio::test]
async fn output_that_does_not_parse_is_a_failure_not_a_crash() {
    let dir = tempfile::tempdir().unwrap();
    let config = cli(dir.path(), &answers("I think this looks fine."));
    let error = read(config).await.unwrap_err();
    assert!(error.contains("not JSON"), "{error}");
}

#[tokio::test]
async fn a_session_that_ends_without_a_result_is_a_failure() {
    let dir = tempfile::tempdir().unwrap();
    let config = cli(dir.path(), "echo not-json-at-all");
    let error = read(config).await.unwrap_err();
    assert!(error.contains("without a result"), "{error}");
}

#[tokio::test]
async fn a_session_over_its_token_budget_is_killed_and_reported() {
    let dir = tempfile::tempdir().unwrap();
    let event = json!({"type": "assistant", "message": {"usage": {"input_tokens": 60, "output_tokens": 50}}});
    let mut config = cli(dir.path(), &format!("echo '{event}'\nsleep 30"));
    config.token_budget = 100;
    let started = Instant::now();
    let error = read(config).await.unwrap_err();
    assert!(error.contains("110 tokens of a 100 budget"), "{error}");
    assert!(started.elapsed() < Duration::from_secs(10));
}

#[tokio::test]
async fn cached_tokens_count_against_the_budget() {
    let dir = tempfile::tempdir().unwrap();
    let usage = json!({"input_tokens": 1, "output_tokens": 1, "cache_read_input_tokens": 200});
    let event = json!({"type": "assistant", "message": {"usage": usage}});
    let mut config = cli(dir.path(), &format!("echo '{event}'\nsleep 30"));
    config.token_budget = 100;
    let error = read(config).await.unwrap_err();
    assert!(error.contains("202 tokens of a 100 budget"), "{error}");
}

#[tokio::test]
async fn a_message_split_across_events_is_counted_once() {
    let dir = tempfile::tempdir().unwrap();
    let event =
        json!({"type": "assistant", "message": {"id": "m1", "usage": {"input_tokens": 60}}});
    let done = json!({"type": "result", "is_error": false, "result": "{}"});
    let mut config = cli(
        dir.path(),
        &format!("echo '{event}'\necho '{event}'\necho '{done}'"),
    );
    config.token_budget = 100;
    assert_eq!(read(config).await.unwrap(), json!({}));
}

#[tokio::test]
async fn a_session_over_its_time_limit_is_killed_and_reported() {
    let dir = tempfile::tempdir().unwrap();
    let mut config = cli(dir.path(), "sleep 30");
    config.timeout = Duration::from_millis(200);
    let started = Instant::now();
    let error = read(config).await.unwrap_err();
    assert!(error.contains("time limit"), "{error}");
    assert!(started.elapsed() < Duration::from_secs(10));
}

#[tokio::test]
async fn a_failed_session_is_reported_with_its_reason() {
    let dir = tempfile::tempdir().unwrap();
    let event = json!({"type": "result", "is_error": true, "subtype": "error_max_turns"});
    let config = cli(dir.path(), &format!("echo '{event}'"));
    let error = read(config).await.unwrap_err();
    assert!(error.contains("error_max_turns"), "{error}");
}

#[tokio::test]
async fn a_session_that_may_write_is_refused_without_a_sandbox() {
    let harness = ClaudeHarness::new(ClaudeConfig::new("key"));
    let error = harness.session("Fix.", WRITE, json!({})).await.unwrap_err();
    assert!(error.0.contains("needs a sandbox"), "{}", error.0);
}

#[tokio::test]
async fn a_session_with_egress_or_secrets_is_refused() {
    let harness = ClaudeHarness::new(ClaudeConfig::new("key"));
    let wide = Grant {
        write: true,
        egress: true,
        secrets: true,
    };
    let error = harness.session("Act.", wide, json!({})).await.unwrap_err();
    assert!(error.0.contains("no egress and no secrets"), "{}", error.0);
}

fn fake_with_patch() -> Arc<FakeSandbox> {
    Arc::new(FakeSandbox::answering(|command| ExecOutput {
        exit_code: 0,
        stdout: if command.contains("--binary") {
            "diff --git a/src b/src\n".to_string()
        } else {
            String::new()
        },
        stderr: String::new(),
        timed_out: false,
    }))
}

#[tokio::test]
async fn a_sandboxed_session_returns_the_patch_and_destroys_its_sandbox() {
    let dir = tempfile::tempdir().unwrap();
    let config = cli(dir.path(), &answers("{}"));
    let fake = fake_with_patch();
    let harness =
        ClaudeHarness::new(config).with_sandbox(fake.clone(), SandboxSpec::agent("image"));
    let out = harness.session("Fix.", WRITE, json!({})).await.unwrap();
    assert_eq!(out["diff"], "diff --git a/src b/src\n");
    assert_eq!(fake.alive(), 0);
}

#[tokio::test]
async fn a_sandbox_is_destroyed_when_its_session_fails() {
    let dir = tempfile::tempdir().unwrap();
    let config = cli(dir.path(), "exit 1");
    let fake = fake_with_patch();
    let harness =
        ClaudeHarness::new(config).with_sandbox(fake.clone(), SandboxSpec::agent("image"));
    harness.session("Fix.", WRITE, json!({})).await.unwrap_err();
    assert_eq!(fake.alive(), 0);
}

#[tokio::test]
async fn the_checkout_is_uploaded_and_never_sent_to_the_model() {
    let dir = tempfile::tempdir().unwrap();
    let config = cli(dir.path(), &answers("{}"));
    let fake = fake_with_patch();
    let harness =
        ClaudeHarness::new(config).with_sandbox(fake.clone(), SandboxSpec::agent("image"));
    let input = json!({"logs": "boom", WORKSPACE_FIELD: "dGFy"});
    harness.session("Fix.", WRITE, input).await.unwrap();
    let sent = std::fs::read_to_string(dir.path().join("stdin")).unwrap();
    assert!(sent.contains("boom"), "{sent}");
    assert!(!sent.contains("dGFy"), "{sent}");
}
