//! Runs a throwaway workflow through the real runtime, against fakes, so a test can state what a
//! script tries and what must become of it.
use almanac::cooled::Cooled;
use almanac::prelude::*;
use almanac::runtime::{Envelope, Origin, Outcome, Runtime};
use almanac::testing::{FakeForge, FakeTracker, ScriptedHarness};
use almanac::workflow::Found;
use futures::future::BoxFuture;
use serde_json::{Value, json};
use std::sync::Arc;
use std::time::Duration;
use workflows_in_lua::catalog::{Catalog, EventSpec, Field, ReadSpec, ScanSpec, parse_event};
use workflows_in_lua::taint::Tainted;
use workflows_in_lua::value::Node;
use workflows_in_lua::workflow::{LoadError, LuaWorkflow};

/// Must never appear in anything a script can read.
pub const SENTINEL: &str = "IGNORE-PREVIOUS-INSTRUCTIONS";

almanac::observed! {
    pub struct Opened {
        pub repo: Slug,
        pub title: Untrusted<String>,
    }
}

impl Event for Opened {
    const KIND: &'static str = "opened";
    fn parse(p: &Payload<'_>) -> Result<Self, Malformed> {
        Ok(Self {
            repo: p.field("/repo")?,
            title: p.text("/title")?,
        })
    }
}

pub fn hostile_text() -> Tainted {
    Tainted::Text(Untrusted::new(SENTINEL.to_string()))
}

pub fn hostile_patch() -> Tainted {
    Tainted::Patch(Untrusted::new(Patch {
        changes: vec![Change::Write {
            path: "src/main.rs".to_string(),
            content: "eA==".to_string(),
            executable: false,
        }],
        diff: SENTINEL.to_string(),
    }))
}

fn hostile<'a>(
    _: &'a Cx,
    _: Vec<Node>,
    _: &'a mut Vec<Cooled>,
) -> BoxFuture<'a, Result<Node, Halt>> {
    Box::pin(async { Ok(Node::Tainted(hostile_text())) })
}

fn a_patch<'a>(
    _: &'a Cx,
    _: Vec<Node>,
    _: &'a mut Vec<Cooled>,
) -> BoxFuture<'a, Result<Node, Halt>> {
    Box::pin(async { Ok(Node::Tainted(hostile_patch())) })
}

fn proof<'a>(
    _: &'a Cx,
    _: Vec<Node>,
    proofs: &'a mut Vec<Cooled>,
) -> BoxFuture<'a, Result<Node, Halt>> {
    Box::pin(async move {
        let now = time::OffsetDateTime::now_utc();
        let cooled = Cooled::check(
            Published::At(now - Duration::from_secs(60)),
            Duration::ZERO,
            now,
        );
        proofs.push(cooled.expect("a release from a minute ago has cooled for no time at all"));
        Ok(Node::Proof(proofs.len() - 1))
    })
}

fn broken<'a>(
    _: &'a Cx,
    _: Vec<Node>,
    _: &'a mut Vec<Cooled>,
) -> BoxFuture<'a, Result<Node, Halt>> {
    Box::pin(async { Err(Halt::Failed(StepError("the forge is down".to_string()))) })
}

fn no_scan(_: &Scanner) -> BoxFuture<'_, Result<Vec<Found>, StepError>> {
    Box::pin(async { Ok(vec![]) })
}

pub fn catalog() -> Catalog {
    let mut catalog = Catalog::default();
    catalog.events.insert(
        "Opened",
        EventSpec {
            kind: Opened::KIND,
            also: &[],
            fields: &[("repo", Field::Slug), ("title", Field::Tainted)],
            parse: parse_event::<Opened>,
        },
    );
    for (name, run) in [
        ("hostile", hostile as workflows_in_lua::catalog::Read),
        ("a_patch", a_patch),
        ("proof", proof),
        ("broken", broken),
    ] {
        let spec = ReadSpec {
            requires: Vec::new,
            run,
        };
        catalog.reads.insert(name, spec);
    }
    catalog.scans.insert(
        "others",
        ScanSpec {
            event: "Other",
            requires: Vec::new,
            run: no_scan,
        },
    );
    for (name, text) in [
        ("read.md", "read"),
        ("write.md", "write"),
        ("fix.md", "fix {{ repo }}"),
        ("malformed.md", "Review {{ repo-name }}"),
        ("typo.md", "Review {{ rpeo }}"),
        ("unclosed.md", "line one\nReview {{ repo"),
    ] {
        catalog.prompts.insert(name, text);
    }
    catalog
}

const OPTIONS: &[(&str, &str)] = &[
    ("name", r#""probe""#),
    ("on", r#""Opened""#),
    ("key", r#""{repo}""#),
    ("intent", r#""x""#),
    ("may", r#"{ "Attest", "Await", "File", "Ask", "Push" }"#),
    ("runs_per_hour", "100"),
    ("reads", r#"{ "hostile", "a_patch", "proof", "broken" }"#),
    (
        "agents",
        r#"{
            reader = { profile = "quarantined", prompt = "read.md" },
            writer = { profile = "worker", prompt = "write.md" },
            fixer = { profile = "fixer", prompt = "fix.md", vars = { repo = "Slug" } },
        }"#,
    ),
    ("literals", r#"{ "ctx", "wait", "commit" }"#),
    (
        "gather",
        r#"function(cx, event) return { kind = "Done" } end"#,
    ),
    ("decide", r#"{ Done = function(facts) return {} end }"#),
];

/// A workflow file with `changes` applied over working defaults. `nil` removes an option.
pub fn source(top: &str, changes: &[(&str, &str)]) -> &'static str {
    let mut text = format!("local almanac = require(\"./almanac\")\n{top}\nreturn {{\n");
    for (name, default) in OPTIONS {
        let chosen = changes.iter().find(|(option, _)| option == name);
        let value = chosen.map_or(*default, |(_, value)| value);
        text.push_str(&format!("  {name} = {value},\n"));
    }
    for (name, value) in changes {
        if !OPTIONS.iter().any(|(option, _)| option == name) {
            text.push_str(&format!("  {name} = {value},\n"));
        }
    }
    text.push_str("}\n");
    Box::leak(text.into_boxed_str())
}

pub fn load(top: &str, changes: &[(&str, &str)]) -> Result<LuaWorkflow, LoadError> {
    LuaWorkflow::load(source(top, changes), "probe.luau", &catalog())
}

/// Effects in the shape they are stored in, for a script's two halves.
pub fn gather_then(gather: &str, decide: &str) -> Vec<(&'static str, String)> {
    vec![
        ("gather", format!("function(cx, event)\n{gather}\nend")),
        (
            "decide",
            format!("{{ Done = function(facts)\n{decide}\nend }}"),
        ),
    ]
}

pub struct Ran {
    pub outcome: Outcome,
    pub harness: ScriptedHarness,
    pub forge: FakeForge,
    pub tracker: FakeTracker,
    pub rt: Runtime,
}

impl Ran {
    /// The failure text, for a run that must have failed.
    pub fn error(&self) -> &str {
        match &self.outcome {
            Outcome::Failed { error, .. } => error,
            other => panic!("expected the run to fail, got {other:?}"),
        }
    }

    pub fn effects(&self) -> &[Effect] {
        match &self.outcome {
            Outcome::Ran { effects, .. } => effects,
            other => panic!("expected the run to finish, got {other:?}"),
        }
    }
}

pub async fn run(workflow: LuaWorkflow, reply: Value) -> Ran {
    let harness = ScriptedHarness::replying(reply);
    let forge = FakeForge::default();
    let tracker = FakeTracker::default();
    let forged: Arc<dyn Forge> = Arc::new(forge.clone());
    let tracked: Arc<dyn Tracker> = Arc::new(tracker.clone());
    let providers = Providers::new()
        .provide::<dyn Forge>(forged)
        .provide::<dyn Tracker>(tracked)
        .harness(harness.clone());
    let rt = Runtime::new(vec![Box::new(workflow)], providers).in_mode(Mode::Live);
    rt.ingest(Envelope {
        source: "test".to_string(),
        delivery_id: "d1".to_string(),
        kind: "opened".to_string(),
        depth: 0,
        origin: Origin::Webhook,
        payload: json!({"repo": "owner/repo", "title": SENTINEL}),
    })
    .await
    .unwrap();
    let mut outcomes = rt.drain().await.unwrap();
    assert_eq!(outcomes.len(), 1, "{outcomes:?}");
    let outcome = outcomes.remove(0);
    rt.deliver().await.unwrap();
    Ran {
        outcome,
        harness,
        forge,
        tracker,
        rt,
    }
}

/// Runs a gather body and a decide body.
pub async fn script(top: &str, gather: &str, decide: &str) -> Ran {
    script_replying(top, gather, decide, json!({"ok": true})).await
}

pub async fn script_replying(top: &str, gather: &str, decide: &str, reply: Value) -> Ran {
    let halves = gather_then(gather, decide);
    let changes: Vec<(&str, &str)> = halves.iter().map(|(k, v)| (*k, v.as_str())).collect();
    let workflow = load(top, &changes).unwrap_or_else(|e| panic!("{e}"));
    run(workflow, reply).await
}
