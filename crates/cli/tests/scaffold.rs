//! Scaffolds an almanac, generates one of each kind, and builds and tests the result. Slow, because
//! it compiles a new workspace. `make test-scaffold` runs this.
use std::path::Path;
use std::process::{Command, Output};

fn almanac(dir: &Path, args: &[&str]) -> Output {
    let out = Command::new(env!("CARGO_BIN_EXE_almanac"))
        .args(args)
        .current_dir(dir)
        .output()
        .unwrap();
    let text = String::from_utf8_lossy(&out.stderr);
    assert!(out.status.success(), "almanac {args:?}: {text}");
    out
}

fn cargo(dir: &Path, args: &[&str]) -> Output {
    let target = concat!(env!("CARGO_MANIFEST_DIR"), "/../../target/scaffold");
    let out = Command::new(env!("CARGO"))
        .args(args)
        .current_dir(dir)
        .env("CARGO_TARGET_DIR", target)
        .env("SQLX_OFFLINE", "true")
        .output()
        .unwrap();
    let text = String::from_utf8_lossy(&out.stderr);
    assert!(out.status.success(), "cargo {args:?}: {text}");
    out
}

#[test]
fn a_new_almanac_builds_tests_and_describes_itself() {
    let dir = tempfile::tempdir().unwrap();
    almanac(dir.path(), &["new", "sample"]);
    let root = dir.path().join("sample");
    almanac(&root, &["generate", "workflow", "watch-things"]);
    almanac(&root, &["generate", "prompt", "reviewer"]);
    almanac(&root, &["generate", "source", "tickets"]);
    assert!(root.join("prompts/reviewer.md").exists());
    assert!(root.join("projections/tickets.toml").exists());

    cargo(&root, &["test", "--quiet"]);
    let out = cargo(
        &root,
        &["run", "--quiet", "--", "--format", "json", "synth"],
    );
    let manifest: serde_json::Value = serde_json::from_slice(&out.stdout).unwrap();
    assert_eq!(manifest[0]["workflow"], "watch-things");
    let out = cargo(&root, &["run", "--quiet", "--", "check"]);
    assert!(String::from_utf8_lossy(&out.stdout).contains("1 workflow(s)"));
}

#[test]
fn generating_outside_an_almanac_says_so() {
    let dir = tempfile::tempdir().unwrap();
    let out = Command::new(env!("CARGO_BIN_EXE_almanac"))
        .args(["generate", "workflow", "x"])
        .current_dir(dir.path())
        .output()
        .unwrap();
    assert!(!out.status.success());
    assert!(String::from_utf8_lossy(&out.stderr).contains("not inside an almanac"));
}

#[test]
fn an_error_code_can_be_explained() {
    let dir = tempfile::tempdir().unwrap();
    let out = almanac(dir.path(), &["explain", "almanac::config::secret"]);
    assert!(String::from_utf8_lossy(&out.stdout).contains("What to do next"));
}
