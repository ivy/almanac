//! The binary as an operator or agent meets it.
use serde_json::Value;
use std::process::{Command, Output};

fn run(config: Option<&str>, args: &[&str]) -> Output {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("almanac.toml");
    if let Some(text) = config {
        std::fs::write(&path, text).unwrap();
    }
    Command::new(env!("CARGO_BIN_EXE_almanac-renovate"))
        .args(args)
        .env_clear()
        .env("ALMANAC_CONFIG", &path)
        .env("GITHUB_TOKEN", "t")
        .env("ANTHROPIC_API_KEY", "k")
        .output()
        .unwrap()
}

fn stderr(out: &Output) -> String {
    String::from_utf8_lossy(&out.stderr).into_owned()
}

#[test]
fn a_bad_config_key_stops_the_almanac_and_points_at_the_line() {
    let out = run(Some("github_token = \"abc\"\n"), &["check"]);
    assert!(!out.status.success());
    let text = stderr(&out);
    assert!(text.contains("almanac.toml:1:1"), "{text}");
    assert!(text.contains("secrets come from the environment"), "{text}");
    assert!(!text.contains("abc"), "{text}");
}

#[test]
fn errors_are_json_when_asked() {
    let out = run(
        Some("github_token = \"abc\"\n"),
        &["--format", "json", "check"],
    );
    assert!(!out.status.success());
    let json: Value = serde_json::from_str(&stderr(&out)).unwrap();
    assert_eq!(json["code"], "almanac::config::secret");
}

#[test]
fn every_verb_answers_in_json_when_asked() {
    for args in [
        &["--format", "json", "check"][..],
        &["--format", "json", "synth"][..],
        &["--format", "json", "explain", "almanac::usage"][..],
    ] {
        let out = run(None, args);
        assert!(out.status.success(), "{args:?}: {}", stderr(&out));
        let text = String::from_utf8_lossy(&out.stdout);
        let _: Value = serde_json::from_str(&text).unwrap();
    }
}

#[test]
fn an_unknown_verb_says_what_exists() {
    let out = run(None, &["frobnicate"]);
    assert!(!out.status.success());
    assert!(stderr(&out).contains("synth | check"), "{}", stderr(&out));
}

#[test]
fn explain_prints_the_long_form() {
    let out = run(None, &["explain", "almanac::config::secret"]);
    let text = String::from_utf8_lossy(&out.stdout);
    assert!(text.contains("## What to do next"), "{text}");
}

const FIXTURE: &str = concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/fixtures/check-suite-green.json"
);

#[test]
fn simulate_goes_through_the_production_path() {
    let out = run(
        None,
        &[
            "simulate",
            "github.check_suite.completed",
            FIXTURE,
            "--dry-run",
        ],
    );
    assert!(out.status.success(), "{}", stderr(&out));
    let text = String::from_utf8_lossy(&out.stdout);
    assert!(text.contains("simulate: Ignored"), "{text}");
}

#[test]
fn backfill_refuses_a_repository_it_cannot_read() {
    let out = run(
        None,
        &["--format", "json", "backfill", "--repo", "not a slug"],
    );
    assert!(!out.status.success());
    let json: Value = serde_json::from_str(&stderr(&out)).unwrap();
    assert_eq!(json["code"], "almanac::usage");
}
