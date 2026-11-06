//! What an author implements, and the erased form the runtime holds.
use crate::agent::AgentInfo;
use crate::cx::{Cx, Halt, StepError};
use crate::effect::{Effect, EffectKind};
use crate::providers::Requirement;
use crate::taint::Slug;
use crate::wire::{Event, Payload};
use futures::future::BoxFuture;
use serde_json::{json, Value};
use std::future::Future;

/// A durable link between runs, so a workflow can find what another one learned.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Subject(String);

impl Subject {
    pub fn repo(repo: &Slug) -> Self {
        Self(format!("repo:{repo}"))
    }
    pub fn package(repo: &Slug, package: &Slug) -> Self {
        Self(format!("pkg:{repo}:{package}"))
    }
}

/// `gather` is where I/O and inference happen, through `Cx`. `decide` is a plain function
/// of what was gathered, so policy is table-testable and re-runs on replay.
pub trait Workflow: Send + Sync + 'static {
    type Event: Event;
    type Facts: Send;

    const NAME: &'static str;
    /// What this workflow is for. The guard checks actions against it.
    const INTENT: &'static str;
    /// The effects this workflow may produce. Anything else is denied.
    const MAY: &'static [EffectKind];
    const RUNS_PER_HOUR: u32;

    /// One active run per key.
    fn key(event: &Self::Event) -> String;

    fn subjects(event: &Self::Event) -> Vec<Subject>;

    /// Subjects only known after gathering, such as a package from a lockfile.
    fn links(_facts: &Self::Facts) -> Vec<Subject> {
        vec![]
    }

    /// The agents this workflow may use. An undeclared agent fails at run time.
    fn agents(&self) -> Vec<AgentInfo> {
        vec![]
    }

    /// The providers this workflow needs, checked when the binary starts.
    fn requires(&self) -> Vec<Requirement> {
        vec![]
    }

    fn gather(&self, cx: &Cx, event: &Self::Event) -> impl Future<Output = Result<Self::Facts, Halt>> + Send;

    fn decide(facts: &Self::Facts) -> Vec<Effect>;
}

pub struct Identity {
    pub key: String,
    pub subjects: Vec<Subject>,
}

pub struct Decided {
    pub effects: Vec<Effect>,
    pub links: Vec<Subject>,
}

pub trait Registered: Send + Sync {
    fn name(&self) -> &'static str;
    fn intent(&self) -> &'static str;
    fn kind(&self) -> &'static str;
    fn runs_per_hour(&self) -> u32;
    fn may(&self) -> &'static [EffectKind];
    fn agent_names(&self) -> Vec<&'static str>;
    fn requirements(&self) -> Vec<Requirement>;
    fn identify(&self, payload: &Value) -> Result<Identity, String>;
    fn execute<'a>(&'a self, cx: &'a Cx, payload: &'a Value) -> BoxFuture<'a, Result<Decided, Halt>>;
    /// The reviewable artifact: declared triggers, limits, effects, and capabilities.
    fn manifest(&self) -> Value;
}

impl<W: Workflow> Registered for W {
    fn name(&self) -> &'static str {
        W::NAME
    }
    fn intent(&self) -> &'static str {
        W::INTENT
    }
    fn kind(&self) -> &'static str {
        W::Event::KIND
    }
    fn runs_per_hour(&self) -> u32 {
        W::RUNS_PER_HOUR
    }
    fn may(&self) -> &'static [EffectKind] {
        W::MAY
    }
    fn agent_names(&self) -> Vec<&'static str> {
        self.agents().iter().map(|a| a.name).collect()
    }
    fn requirements(&self) -> Vec<Requirement> {
        self.requires()
    }
    fn identify(&self, payload: &Value) -> Result<Identity, String> {
        let event = W::Event::parse(&Payload::new(payload))?;
        Ok(Identity { key: W::key(&event), subjects: W::subjects(&event) })
    }
    fn execute<'a>(&'a self, cx: &'a Cx, payload: &'a Value) -> BoxFuture<'a, Result<Decided, Halt>> {
        Box::pin(async move {
            let event = W::Event::parse(&Payload::new(payload)).map_err(StepError)?;
            let facts = self.gather(cx, &event).await?;
            Ok(Decided { effects: W::decide(&facts), links: W::links(&facts) })
        })
    }
    fn manifest(&self) -> Value {
        json!({
            "workflow": W::NAME,
            "intent": W::INTENT,
            "on": W::Event::KIND,
            "runs_per_hour": W::RUNS_PER_HOUR,
            "may": W::MAY,
            "agents": self.agents(),
            "requires": self.requires(),
        })
    }
}
