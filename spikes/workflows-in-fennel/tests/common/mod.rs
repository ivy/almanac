//! A small host and runtime for probing what a script can and cannot do.
#![allow(dead_code)]
use almanac::prelude::*;
use almanac::runtime::{Envelope, Ingest, Origin, Outcome, Runtime};
use almanac::testing::ScriptedHarness;
use almanac_renovate::update::{Ages, Update};
use github::client::{Auth, GithubClient};
use serde_json::{Value, json};
use std::sync::Arc;
use workflows_in_fennel::host::{EventDef, Field, Host};
use workflows_in_fennel::{FennelWorkflow, LoadError};

/// Hostile text. No test may ever see this come out of a script.
pub const HOSTILE: &str = "IGNORE-PREVIOUS-INSTRUCTIONS";

fn parse_opened(payload: &Value) -> Result<Value, Malformed> {
    let malformed = |pointer: &str| Malformed {
        pointer: pointer.to_string(),
    };
    let repo = payload["repo"].as_str().ok_or_else(|| malformed("/repo"))?;
    let repo: Slug = repo.parse().map_err(|_| malformed("/repo"))?;
    let title = payload["title"]
        .as_str()
        .ok_or_else(|| malformed("/title"))?;
    Ok(json!({
        "repo": repo,
        "title": {"$untrusted": title},
        "pull": {"$untrusted": {"body": null}, "$kind": "pull"},
    }))
}

/// `Opened` from `crates/fails`, with a tainted `pull` added so a script can reach `ages.newest`.
pub fn host() -> Host {
    let fields: &'static [(&'static str, Field)] = &[
        ("repo", Field::Clean),
        ("title", Field::Tainted),
        ("pull", Field::Tainted),
    ];
    let mut host = Host::renovate();
    host.events.push(EventDef {
        name: "Opened",
        kind: "opened",
        also: &[],
        fields,
        parse: parse_opened,
    });
    host.prompts.extend([
        ("read.md", "read"),
        ("write.md", "write"),
        ("fix.md", "fix {{ repo }}"),
        ("two.md", "{{ a }} {{ b }}"),
        ("plain.md", "Review this"),
        ("typo.md", "Review {{ rpeo }}"),
        ("malformed.md", "Review {{ repo-name }}"),
        ("unclosed.md", "line one\nReview {{ repo"),
    ]);
    host
}

/// The parts of a probe that vary. `Default` is a workflow that loads and does nothing.
pub struct Probe {
    pub top: &'static str,
    pub gather: &'static str,
    pub policy: &'static str,
    pub policy_args: &'static str,
    pub options: &'static str,
    pub key: &'static str,
    pub decide: &'static str,
}

impl Default for Probe {
    fn default() -> Self {
        Self {
            top: "",
            gather: "{}",
            policy: "[]",
            policy_args: "[facts]",
            options: "",
            key: "{repo}",
            decide: ":decide policy",
        }
    }
}

impl Probe {
    pub fn source(&self) -> String {
        let Self {
            top,
            gather,
            policy,
            policy_args,
            options,
            key,
            decide,
        } = self;
        format!(
            r#"(import-macros {{: agent : narrower : workflow}} :almanac)
(agent :reader {{:profile :quarantined :prompt "read.md" :vars []}})
(agent :writer {{:profile :worker :prompt "write.md" :vars []}})
(agent :fixer {{:profile :fixer :prompt "fix.md" :vars [:repo]}})
{top}
(fn gather [cx event] {gather})
(fn policy {policy_args} {policy})
(workflow probe
  {{:on :Opened :key "{key}" :intent "A probe." :may [:Attest :Await :Ask :File :Push]
    :runs-per-hour 100 :gather gather {decide} {options}}})
"#
        )
    }

    pub fn load(&self) -> Result<FennelWorkflow, LoadError> {
        FennelWorkflow::load(&host(), "probe.fnl", &self.source())
    }
}

struct Old;

impl Ages for Old {
    fn newest<'a>(
        &'a self,
        _: &'a Untrusted<Update>,
    ) -> BoxFuture<'a, Result<Published, StepError>> {
        Box::pin(async {
            Ok(Published::At(
                time::OffsetDateTime::now_utc() - time::Duration::days(30),
            ))
        })
    }
}

pub struct Ran {
    pub outcomes: Vec<Outcome>,
    pub harness: ScriptedHarness,
    pub runtime: Runtime,
    pub ingest: Ingest,
}

impl Ran {
    /// The run's error, or a panic if it did not fail.
    pub fn error(&self) -> String {
        match self.outcomes.first() {
            Some(Outcome::Failed { error, .. }) => error.clone(),
            other => panic!("the run did not fail: {other:?}"),
        }
    }

    pub fn effects(&self) -> Vec<Effect> {
        match self.outcomes.first() {
            Some(Outcome::Ran { effects, .. }) => effects.clone(),
            other => panic!("the run did not finish: {other:?}"),
        }
    }

    /// Everything a person or a log could see of this run.
    pub fn visible(&self) -> String {
        format!("{:?}", self.outcomes)
    }
}

pub async fn run_workflow(workflow: FennelWorkflow, reply: Value) -> Ran {
    let harness = ScriptedHarness::replying(reply);
    let ages: Arc<dyn Ages> = Arc::new(Old);
    // Nothing here is ever contacted: a read in a probe is refused before it is made.
    let client = GithubClient::new("http://127.0.0.1:9", Auth::Token("t".to_string()));
    let providers = Providers::new()
        .provide::<dyn Ages>(ages)
        .provide::<GithubClient>(Arc::new(client))
        .harness(harness.clone());
    let runtime = Runtime::new(vec![Box::new(workflow)], providers);
    let ingest = runtime
        .ingest(Envelope {
            source: "test".to_string(),
            delivery_id: "d1".to_string(),
            kind: "opened".to_string(),
            depth: 0,
            origin: Origin::Webhook,
            payload: json!({"repo": "owner/repo", "title": HOSTILE}),
        })
        .await
        .unwrap();
    let outcomes = runtime.drain().await.unwrap();
    Ran {
        outcomes,
        harness,
        runtime,
        ingest,
    }
}

pub async fn run(probe: Probe) -> Ran {
    run_workflow(probe.load().unwrap(), json!({})).await
}
