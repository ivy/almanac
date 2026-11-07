//! The binary as an operator or agent meets it.
use almanac::testing::generators::{arguments, config_texts, env_settings};
use hegel::TestCase;
use hegel::generators::{self as gs, Generator};
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
fn an_oauth_token_is_enough_to_start() {
    let out = Command::new(env!("CARGO_BIN_EXE_almanac-renovate"))
        .args(["--format", "json", "check"])
        .env_clear()
        .env("ALMANAC_CONFIG", "/nonexistent/almanac.toml")
        .env("GITHUB_TOKEN", "t")
        .env("CLAUDE_CODE_OAUTH_TOKEN", "o")
        .output()
        .unwrap();
    assert!(out.status.success(), "{}", stderr(&out));
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

/// A simulated fleet of one repository, with a red pull request that would otherwise reach an
/// agent and post statuses.
async fn red_fleet() -> simforge::SimForge {
    let sim = simforge::SimForge::start().await;
    sim.add_repo(simforge::Repo::public("owner/repo"));
    let mut pull = simforge::Pull::renovate("renovate/tar-7.x", "Update tar");
    pull.body = Some("| Package | Change |\n|---|---|\n".to_string());
    let number = sim.add_pull("owner/repo", pull);
    let sha = sim.pull("owner/repo", number).head_sha;
    let suite = simforge::Suite::finished(&sha, "renovate/tar-7.x", "failure", "boom");
    sim.add_suite("owner/repo", suite);
    sim
}

async fn read_only(sim: &simforge::SimForge, args: &[&str]) -> Output {
    tokio::process::Command::new(env!("CARGO_BIN_EXE_almanac-renovate"))
        .args(["--format", "json"])
        .args(args)
        .env_clear()
        .env("ALMANAC_READ_ONLY", "1")
        .env("GITHUB_TOKEN", "t")
        .env("GITHUB_API_URL", sim.base_url())
        .env("ALMANAC_OWNER", "owner")
        .env("ALMANAC_RENOVATE_AUTHOR", simforge::RENOVATE)
        .env("ALMANAC_REGISTRY_URL", sim.base_url())
        .env("ALMANAC_CONFIG", "/nonexistent/almanac.toml")
        .output()
        .await
        .unwrap()
}

#[tokio::test]
async fn a_read_only_pass_writes_nothing_and_calls_no_model() {
    let sim = red_fleet().await;
    let out = read_only(&sim, &["backfill", "--dry-run"]).await;
    assert!(out.status.success(), "{}", stderr(&out));
    let json: Value = serde_json::from_slice(&out.stdout).unwrap();
    assert_eq!(json["backfill"]["found"], 1);
    assert_eq!(sim.writes(), vec![], "nothing reached the forge");
}

#[tokio::test]
async fn a_backfill_of_a_repository_it_can_see_finds_its_pull_requests() {
    let sim = red_fleet().await;
    let out = read_only(&sim, &["backfill", "--repo", "owner/repo", "--dry-run"]).await;
    assert!(out.status.success(), "{}", stderr(&out));
    let json: Value = serde_json::from_slice(&out.stdout).unwrap();
    assert_eq!(json["backfill"]["found"], 1);
}

/// Finding nothing would read as all clear, for a repository nobody looked at.
#[tokio::test]
async fn a_backfill_of_a_repository_it_cannot_see_fails() {
    let sim = red_fleet().await;
    let out = read_only(&sim, &["backfill", "--repo", "owner/unseen", "--dry-run"]).await;
    assert!(!out.status.success());
    let json: Value = serde_json::from_str(&stderr(&out)).unwrap();
    assert_eq!(json["code"], "almanac::scan::failed");
    assert!(stderr(&out).contains("owner/unseen"), "{}", stderr(&out));
}

#[test]
fn help_is_not_an_error() {
    let out = run(None, &["--help"]);
    assert!(out.status.success(), "{}", stderr(&out));
    assert!(String::from_utf8_lossy(&out.stdout).contains("usage:"));
}

#[test]
fn simulating_a_kind_nothing_handles_says_so() {
    let out = run(None, &["simulate", "no.such.kind", FIXTURE, "--dry-run"]);
    let text = String::from_utf8_lossy(&out.stdout);
    assert!(text.contains("no workflow is triggered by"), "{text}");
}

/// What one invocation is given: its arguments, its config file, its environment, and, for
/// `simulate`, an input file that is never valid JSON.
#[derive(Debug)]
struct Invocation {
    args: Vec<String>,
    config: Option<String>,
    env: Vec<(String, String)>,
    token: bool,
    input: Option<String>,
}

#[hegel::composite]
fn invocations(tc: &TestCase) -> Invocation {
    Invocation {
        args: tc.draw(arguments()),
        config: tc.draw(gs::optional(config_texts())),
        env: tc.draw(env_settings()),
        token: tc.draw(gs::weighted_booleans(0.8)),
        input: tc.draw(gs::optional(
            gs::text().max_size(20).map(|t| format!("{{{t}")),
        )),
    }
}

impl Invocation {
    /// Runs the binary in `dir`, so both forms of one invocation name the same paths.
    fn run(&self, dir: &std::path::Path, json: bool) -> (bool, String) {
        let config = dir.join("almanac.toml");
        if let Some(text) = &self.config {
            std::fs::write(&config, text).unwrap();
        }
        let mut args = self.args.clone();
        if json {
            args.splice(0..0, ["--format".to_string(), "json".to_string()]);
        }
        if self.args.first().is_some_and(|a| a == "simulate") && self.args.len() == 2 {
            let input = dir.join("event.json");
            if let Some(text) = &self.input {
                std::fs::write(&input, text).unwrap();
            }
            args.push(input.to_string_lossy().into_owned());
        }
        let mut command = Command::new(env!("CARGO_BIN_EXE_almanac-renovate"));
        command
            .args(&args)
            .env_clear()
            .env("ALMANAC_CONFIG", &config)
            .envs(self.env.iter().map(|(k, v)| (k, v)));
        if self.token {
            command
                .env("GITHUB_TOKEN", "t")
                .env("ANTHROPIC_API_KEY", "k");
        }
        let out = command.output().unwrap();
        (out.status.success(), stderr(&out))
    }
}

/// Letters and digits only, so wrapping, box drawing, and indentation do not count as substance.
fn substance(text: &str) -> String {
    text.chars().filter(|c| c.is_alphanumeric()).collect()
}

/// Principle 9, R-42: whatever an operator passes, a failure names a code with a page, says what
/// to do next, and says the same to an agent asking for JSON as to a person.
#[hegel::test]
fn principle_9_every_failure_says_the_same_to_people_and_agents(tc: TestCase) {
    let invocation = tc.draw(invocations());
    let dir = tempfile::tempdir().unwrap();
    let (ok, machine) = invocation.run(dir.path(), true);
    let (human_ok, human) = invocation.run(dir.path(), false);
    assert_eq!(ok, human_ok, "the format changed the outcome");
    if ok {
        return;
    }
    let json: Value = serde_json::from_str(&machine).expect("an error an agent can parse");
    let code = json["code"].as_str().expect("a code");
    assert!(almanac::diag::explain(code).is_ok(), "{code} has no page");
    let human = substance(&human);
    assert!(
        human.contains(&substance(code)),
        "{code} is missing for people"
    );
    for field in ["message", "help"] {
        let text = json[field].as_str().unwrap_or_default();
        assert!(!text.trim().is_empty(), "{code} has no {field}");
        assert!(
            human.contains(&substance(text)),
            "{code}'s {field} differs for people"
        );
    }
}
