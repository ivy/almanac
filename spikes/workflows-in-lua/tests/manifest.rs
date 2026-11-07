//! How a manifest is derived, and where it can drift from what a workflow does.
mod common;

use almanac::prelude::*;
use common::script::*;
use serde_json::Value;
use std::collections::BTreeSet;
use std::path::Path;
use std::process::Command;
use workflows_in_lua::renovate::build_failures;

#[test]
fn loading_runs_neither_gather_nor_decide() {
    let workflow = load(
        "",
        &[
            ("gather", r#"function() error("gather ran") end"#),
            ("decide", r#"{ Done = function() error("decide ran") end }"#),
        ],
    )
    .unwrap();
    assert_eq!(workflow.manifest()["workflow"], "probe");
}

/// Nothing about a load depends on the world, because the file sees no clock, input, or `cx`.
#[test]
fn the_manifest_is_the_same_every_time() {
    let first = build_failures().unwrap().manifest();
    for _ in 0..20 {
        assert_eq!(build_failures().unwrap().manifest(), first);
    }
}

/// The file's top level is code, so reading its table means evaluating it, and a static reader
/// cannot see this one's `may`. The closed set still applies to the result.
#[test]
fn a_computed_declaration_needs_evaluating() {
    let may = r#"(function()
        local kinds = {}
        for _, kind in ipairs({ "Attest", "Await" }) do table.insert(kinds, kind) end
        return kinds
    end)()"#;
    let workflow = load("", &[("may", may)]).unwrap();
    assert_eq!(workflow.may(), [EffectKind::Attest, EffectKind::Await]);

    let may = r#"(function() return { "Mer" .. "ge" } end)()"#;
    let error = load("", &[("may", may)]).err().unwrap().to_string();
    assert!(error.contains("not an effect"), "{error}");
}

#[test]
fn a_top_level_that_never_finishes_fails_to_load() {
    let error = load("while true do end", &[]).err().unwrap().to_string();
    assert!(error.contains("instruction budget"), "{error}");
}

/// Drift one way fails safe: an undeclared read is not on `cx`, so the call stops the run. But it
/// stops it when the line runs, where `#[workflow]` would have added it to the manifest.
#[tokio::test]
async fn a_read_the_manifest_omits_fails_when_it_is_reached() {
    let halves = gather_then("cx.hostile()", "return {}");
    let mut changes: Vec<(&str, &str)> = halves.iter().map(|(k, v)| (*k, v.as_str())).collect();
    changes.push(("reads", "{}"));
    let ran = run(load("", &changes).unwrap(), serde_json::json!({})).await;
    assert!(
        ran.error().contains("no field `hostile`"),
        "{}",
        ran.error()
    );
}

/// Drift the other way is silent: a read nobody calls still shows in the manifest.
#[test]
fn a_read_the_workflow_never_makes_stays_in_the_manifest() {
    let source = include_str!("../workflows/build_failures.luau");
    let padded = source.replace(
        r#"reads = { "pull", "logs", "cooled" }"#,
        r#"reads = { "pull", "logs", "cooled", "pull" }"#,
    );
    let padded: &'static str = Box::leak(padded.into_boxed_str());
    let catalog = workflows_in_lua::renovate::catalog();
    let workflow =
        workflows_in_lua::workflow::LuaWorkflow::load(padded, "padded.luau", &catalog).unwrap();
    assert_eq!(workflow.declared().reads.len(), 4, "nothing objects");
}

fn collect(node: &Value, found: &mut BTreeSet<String>) {
    match node {
        Value::Object(fields) => {
            let on_cx = fields.get("type").and_then(Value::as_str) == Some("AstExprIndexName")
                && node["expr"]["type"] == "AstExprLocal"
                && node["expr"]["local"]["name"] == "cx";
            if on_cx {
                found.insert(node["index"].as_str().unwrap_or_default().to_string());
            }
            fields.values().for_each(|value| collect(value, found));
        }
        Value::Array(items) => items.iter().for_each(|item| collect(item, found)),
        _ => {}
    }
}

/// `#[workflow]` finds what a body uses by walking its syntax. `luau-ast` allows the same check,
/// from outside the host, with the same limit: a `cx` passed to a helper under another name is not
/// seen. Run it with `mise exec luau@0.745 -- cargo test`.
#[test]
fn the_declared_reads_are_the_ones_the_source_uses() {
    let file = Path::new(env!("CARGO_MANIFEST_DIR")).join("workflows/build_failures.luau");
    let output = Command::new("luau-ast").arg(&file).output();
    let Some(output) = output.ok().filter(|output| output.status.success()) else {
        println!("luau-ast is not on PATH, so nothing was checked");
        return;
    };
    let tree: Value = serde_json::from_slice(&output.stdout).unwrap();
    let mut used = BTreeSet::new();
    collect(&tree, &mut used);
    for universal in ["attempts", "hold", "repair", "read", "act"] {
        used.remove(universal);
    }
    let workflow = build_failures().unwrap();
    let declared: BTreeSet<String> = workflow
        .declared()
        .reads
        .iter()
        .map(|(name, _)| name.to_string())
        .collect();
    assert_eq!(used, declared);
}
