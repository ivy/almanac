//! The manifest comes from reading the source. Nothing in a workflow runs to produce it.
mod common;
use almanac::prelude::*;
use almanac_renovate::workflows::build_failures::BuildFailures;
use common::*;
use serde_json::{Value, json};
use workflows_in_fennel::build_failures;

#[test]
fn the_ported_manifest_matches_the_rust_one_except_for_scan() {
    let mut rust = BuildFailures.manifest();
    let mut fennel = build_failures().unwrap().manifest();
    assert_eq!(rust["scans"], true);
    assert_eq!(fennel["scans"], false, "this spike does not port scan");
    rust["scans"] = Value::Null;
    fennel["scans"] = Value::Null;
    assert_eq!(rust, fennel);
}

#[test]
fn a_manifest_is_derived_without_running_the_workflow() {
    // Every piece of code here would fail if it ran, including the one at the top level.
    let workflow = Probe {
        top: r#"(local boom (error "ran at load"))"#,
        gather: r#"(error "ran gather") (cx.get :github)"#,
        policy: r#"(error "ran policy")"#,
        ..Probe::default()
    }
    .load()
    .unwrap();
    let manifest = workflow.manifest();
    assert_eq!(manifest["workflow"], "probe");
    assert_eq!(manifest["requires"].as_array().unwrap().len(), 1);
}

#[test]
fn a_step_in_a_helper_reaches_the_manifest() {
    // `#[workflow]` reads one function body, so the same helper in Rust goes unseen. See
    // `Uses` in crates/macros.
    let workflow = Probe {
        top: "(fn deep [cx] (cx.get :ages))
              (fn shallow [cx] (deep cx))",
        gather: "(shallow cx) {}",
        ..Probe::default()
    }
    .load()
    .unwrap();
    let requires = workflow.manifest()["requires"].clone();
    assert_eq!(
        requires,
        json!([{"name": "dyn almanac_renovate::update::Ages"}])
    );
}

#[test]
fn a_computed_declaration_is_refused() {
    let error = Probe {
        options: ":runs-per-hour (* 4 5)",
        ..Probe::default()
    }
    .load()
    .err()
    .unwrap();
    assert_eq!(error.code, "load/not-literal");
}

#[test]
fn a_name_computed_at_run_time_is_refused_where_it_is_written() {
    let error = Probe {
        gather: r#"(cx.get (.. "git" "hub"))"#,
        ..Probe::default()
    }
    .load()
    .err()
    .unwrap();
    assert_eq!(error.code, "load/not-literal");
}

#[test]
fn macros_and_raw_lua_are_refused_because_the_reader_could_not_see_through_them() {
    for gather in [
        r#"(lua "return 1")"#,
        "(macro hidden [] `(cx.get :github)) (hidden)",
        "(eval-compiler nil)",
        r#"(require :other)"#,
        r#"(include :other)"#,
    ] {
        let error = Probe {
            gather,
            ..Probe::default()
        }
        .load()
        .err()
        .unwrap();
        assert_eq!(error.code, "load/refused-form", "{gather}: {error}");
    }
}

#[test]
fn nothing_may_run_at_the_top_level() {
    let error = Probe {
        top: r#"(print "hello")"#,
        ..Probe::default()
    }
    .load()
    .err()
    .unwrap();
    assert_eq!(error.code, "load/not-a-declaration");
}

#[test]
fn a_load_error_says_what_why_and_what_next_and_where() {
    let error = Probe {
        options: ":may [:Merge]",
        ..Probe::default()
    }
    .load()
    .err()
    .unwrap();
    let text = error.to_string();
    assert!(text.contains("`Merge` is not an effect"), "{text}");
    assert!(text.contains("closed set"), "{text}");
    assert!(text.contains("Merging is the repository's job"), "{text}");

    let error = Probe {
        gather: "(cx.summon :fixer)",
        ..Probe::default()
    }
    .load()
    .err()
    .unwrap();
    assert_eq!(error.code, "load/unknown-step");
    assert_eq!(error.line, 6, "{error}");
}

/// What a reader cannot see still cannot be used, so a manifest never understates a workflow.
#[tokio::test]
async fn a_provider_reached_past_the_reader_fails_at_run_time() {
    let ran = run(Probe {
        gather: "(let [get cx.get] (get :github))",
        ..Probe::default()
    })
    .await;
    assert!(
        ran.error().contains("not in this workflow's manifest"),
        "{}",
        ran.error()
    );
}

#[tokio::test]
async fn an_agent_reached_past_the_reader_fails_at_run_time() {
    let ran = run(Probe {
        gather: "(let [ask cx.read] (ask :reader {:prompt {} :input {}}))",
        ..Probe::default()
    })
    .await;
    assert!(
        ran.error().contains("not declared by this workflow"),
        "{}",
        ran.error()
    );
    assert_eq!(ran.harness.calls(), 0);
}

#[tokio::test]
async fn an_observation_named_past_the_reader_fails_at_run_time() {
    let ran = run(Probe {
        gather: r#"(let [read cx.observe] (read (.. "se" "en") #{}))"#,
        ..Probe::default()
    })
    .await;
    assert!(
        ran.error().contains("not an observation"),
        "{}",
        ran.error()
    );
}
