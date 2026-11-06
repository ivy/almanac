//! What an author implements, and the erased form the runtime holds.
use crate::agent::AgentInfo;
use crate::cx::{Cx, Halt, StepError};
use crate::effect::{Effect, EffectKind};
use crate::providers::{Providers, Requirement};
use crate::taint::{Sha, Slug};
use crate::wire::{Event, FromRest, Malformed, Payload};
use futures::future::BoxFuture;
use serde_json::{Value, json};
use std::future::Future;
use std::marker::PhantomData;
use std::sync::Arc;

/// A durable link between runs, so a workflow can find what another one learned.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Subject(String);

impl Subject {
    pub fn as_str(&self) -> &str {
        &self.0
    }
    pub fn repo(repo: &Slug) -> Self {
        Self(format!("repo:{repo}"))
    }
    pub fn package(repo: &Slug, package: &Slug) -> Self {
        Self(format!("pkg:{repo}:{package}"))
    }
}

/// What a scan can reach. It lists subjects: deciding about them is a run's job.
pub struct Scanner {
    providers: Arc<Providers>,
    repo: Option<Slug>,
}

impl Scanner {
    pub(crate) fn new(providers: Arc<Providers>, repo: Option<Slug>) -> Self {
        Self { providers, repo }
    }

    pub fn get<T: ?Sized + Send + Sync + 'static>(&self) -> Result<Arc<T>, StepError> {
        let missing = || StepError(format!("no provider for {}", std::any::type_name::<T>()));
        self.providers.get::<T>().ok_or_else(missing)
    }

    /// The one repository to look at, if a backfill named one.
    pub fn repo(&self) -> Option<&Slug> {
        self.repo.as_ref()
    }
}

/// An event a scan found. It reaches a workflow by the same path as a webhook.
pub struct Scanned<E> {
    delivery_id: String,
    payload: Value,
    _event: PhantomData<fn() -> E>,
}

impl<E: Event> Scanned<E> {
    pub fn id(&self) -> &str {
        &self.delivery_id
    }

    /// `delivery_id` must be stable, because that is what makes scanning twice harmless.
    pub fn new(delivery_id: impl Into<String>, payload: Value) -> Self {
        Self {
            delivery_id: delivery_id.into(),
            payload,
            _event: PhantomData,
        }
    }

    /// Adds something that changes when the subject should be looked at again, such as the state
    /// of its checks. Without it, a scan of an unchanged commit is dropped as a duplicate, and a
    /// run that decided to wait would never be reconsidered.
    pub fn at_state(mut self, state: impl std::fmt::Display) -> Self {
        self.delivery_id = format!("{}~{state}", self.delivery_id);
        self
    }
}

impl<E: FromRest> Scanned<E> {
    /// A pull request read from the REST API, identified by repository, number, and head.
    pub fn pull(repo: &Slug, number: u64, head: &Sha, rest: &Value) -> Self {
        Self::new(
            format!("scan:{repo}#{number}@{head}"),
            E::webhook_payload(rest),
        )
    }
}

/// A scanned event with its type erased, ready for the runtime.
#[derive(Clone, Debug)]
pub struct Found {
    pub delivery_id: String,
    pub kind: &'static str,
    pub payload: Value,
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
    /// Whether this workflow can list its current subjects.
    const SCANS: bool = false;

    /// Lists current subjects, for backfill and catch-up. Must produce the same key as the
    /// webhook for the same subject, or the two will not coalesce.
    fn scan(
        &self,
        _scanner: &Scanner,
    ) -> impl Future<Output = Result<Vec<Scanned<Self::Event>>, StepError>> + Send {
        async { Ok(vec![]) }
    }

    /// Whether this event deserves a run at all. A plain check of the event, so most deliveries
    /// cost nothing.
    fn wanted(_event: &Self::Event) -> bool {
        true
    }

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

    fn gather(
        &self,
        cx: &Cx,
        event: &Self::Event,
    ) -> impl Future<Output = Result<Self::Facts, Halt>> + Send;

    fn decide(facts: &Self::Facts) -> Vec<Effect>;
}

pub struct Identity {
    pub key: String,
    pub subjects: Vec<Subject>,
    pub wanted: bool,
}

pub struct Decided {
    pub effects: Vec<Effect>,
    pub links: Vec<Subject>,
}

pub trait Registered: Send + Sync {
    fn name(&self) -> &'static str;
    fn intent(&self) -> &'static str;
    fn kind(&self) -> &'static str;
    fn handles(&self, kind: &str) -> bool;
    fn runs_per_hour(&self) -> u32;
    fn may(&self) -> &'static [EffectKind];
    fn agent_names(&self) -> Vec<&'static str>;
    fn requirements(&self) -> Vec<Requirement>;
    fn identify(&self, payload: &Value) -> Result<Identity, Malformed>;
    fn execute<'a>(
        &'a self,
        cx: &'a Cx,
        payload: &'a Value,
    ) -> BoxFuture<'a, Result<Decided, Halt>>;
    fn scan<'a>(&'a self, scanner: &'a Scanner) -> BoxFuture<'a, Result<Vec<Found>, StepError>>;
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
    fn handles(&self, kind: &str) -> bool {
        kind == W::Event::KIND || W::Event::ALSO.contains(&kind)
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
    fn identify(&self, payload: &Value) -> Result<Identity, Malformed> {
        let event = W::Event::parse(&Payload::new(payload))?;
        Ok(Identity {
            key: W::key(&event),
            subjects: W::subjects(&event),
            wanted: W::wanted(&event),
        })
    }
    fn execute<'a>(
        &'a self,
        cx: &'a Cx,
        payload: &'a Value,
    ) -> BoxFuture<'a, Result<Decided, Halt>> {
        Box::pin(async move {
            let event = W::Event::parse(&Payload::new(payload)).map_err(StepError::from)?;
            let facts = self.gather(cx, &event).await?;
            Ok(Decided {
                effects: W::decide(&facts),
                links: W::links(&facts),
            })
        })
    }
    fn scan<'a>(&'a self, scanner: &'a Scanner) -> BoxFuture<'a, Result<Vec<Found>, StepError>> {
        Box::pin(async move {
            let scanned = Workflow::scan(self, scanner).await?;
            let erased = scanned.into_iter().map(|s| Found {
                delivery_id: s.delivery_id,
                kind: W::Event::KIND,
                payload: s.payload,
            });
            Ok(erased.collect())
        })
    }
    fn manifest(&self) -> Value {
        json!({
            "workflow": W::NAME,
            "intent": W::INTENT,
            "on": W::Event::KIND,
            "scans": W::SCANS,
            "runs_per_hour": W::RUNS_PER_HOUR,
            "may": W::MAY,
            "agents": self.agents(),
            "requires": self.requires(),
        })
    }
}
