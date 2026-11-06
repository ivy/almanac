//! Routes events, runs workflows, and applies limits. What it must remember is in `store`.
use crate::clock::{Clock, SystemClock};
use crate::cx::{Cx, Halt, StepError};
use crate::effect::{ActionSummary, Effect, EffectKind, Forge, Mode, Planned, Push, Tracker};
use crate::providers::Providers;
use crate::store::{
    Admission, Budget, Claim, ClaimRequest, Entry, EntryStatus, MemoryStore, Pending, Run, Store,
    StoreError, Stored,
};
use crate::taint::{Slug, Untrusted};
use crate::workflow::{Decided, Registered, Scanner, Subject};
use futures::future::{BoxFuture, join_all};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

pub const MAX_CHAIN_DEPTH: u32 = 3;
/// `runs_per_hour` and every budget are measured over this.
pub const BUDGET_WINDOW: Duration = Duration::from_secs(3600);
const MAX_CONCURRENT_RUNS: usize = 4;
const MAX_DELIVERY_ATTEMPTS: u32 = 5;

/// Runtime limits. `tighten` can only lower them, however often it is called.
#[derive(Clone, Copy, Debug)]
pub struct Limits {
    pub max_depth: u32,
    pub runs_cap: Option<u32>,
    /// Runs all workflows together may start per hour.
    pub overall_cap: Option<u32>,
    pub concurrency: usize,
}

impl Default for Limits {
    fn default() -> Self {
        Self {
            max_depth: MAX_CHAIN_DEPTH,
            runs_cap: None,
            overall_cap: None,
            concurrency: MAX_CONCURRENT_RUNS,
        }
    }
}

impl Limits {
    pub fn tighten(self, other: Limits) -> Self {
        let lower = |a: Option<u32>, b: Option<u32>| match (a, b) {
            (Some(a), Some(b)) => Some(a.min(b)),
            (a, b) => a.or(b),
        };
        Self {
            max_depth: self.max_depth.min(other.max_depth),
            runs_cap: lower(self.runs_cap, other.runs_cap),
            overall_cap: lower(self.overall_cap, other.overall_cap),
            concurrency: self.concurrency.min(other.concurrency),
        }
    }
}

/// Where an event came from. Independent of whether effects are live.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub enum Origin {
    #[default]
    Webhook,
    Scan,
    Simulated,
}

impl Origin {
    /// Lower claims first, so a backfill can never delay a live event.
    pub fn lane(self) -> u8 {
        match self {
            Self::Webhook | Self::Simulated => 0,
            Self::Scan => 1,
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Self::Webhook => "webhook",
            Self::Scan => "scan",
            Self::Simulated => "simulated",
        }
    }

    pub(crate) fn parse(text: &str) -> Stored<Self> {
        match text {
            "webhook" => Ok(Self::Webhook),
            "scan" => Ok(Self::Scan),
            "simulated" => Ok(Self::Simulated),
            other => Err(StoreError(format!("unknown origin `{other}`"))),
        }
    }
}

#[derive(Clone, Debug)]
pub struct Envelope {
    pub source: String,
    pub delivery_id: String,
    pub kind: String,
    /// How many of Almanac's own effects led here.
    pub depth: u32,
    pub origin: Origin,
    pub payload: Value,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Ingest {
    Duplicate,
    ChainStopped,
    Unrouted,
    Rejected,
    Queued,
    Coalesced,
    MarkedDirty,
}

#[derive(Debug)]
pub enum Outcome {
    Ran {
        run_id: String,
        workflow: &'static str,
        effects: Vec<Effect>,
        denied: Vec<ActionSummary>,
    },
    Stopped {
        run_id: String,
        workflow: &'static str,
        reason: String,
    },
    Failed {
        run_id: String,
        workflow: &'static str,
        error: String,
    },
    /// `workflow` is `None` for the overall budget.
    BudgetTripped {
        workflow: Option<&'static str>,
        held: usize,
    },
}

/// What a backfill found.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize)]
pub struct Backfill {
    pub found: usize,
    pub queued: usize,
    /// Already seen, so nothing has changed since the last scan.
    pub unchanged: usize,
    /// Did not parse. These should be looked at: a subject here is being missed.
    pub rejected: usize,
}

/// What one attempt to apply an effect did.
#[derive(Debug, PartialEq)]
pub struct Delivery {
    pub key: String,
    /// Why this attempt failed. `None` means the effect was applied.
    pub error: Option<String>,
}

fn planned(outcome: &Outcome) -> Stored<Vec<Value>> {
    let Outcome::Ran { effects, .. } = outcome else {
        return Ok(vec![]);
    };
    effects
        .iter()
        .map(|effect| serde_json::to_value(Planned::from(effect)))
        .collect::<Result<_, _>>()
        .map_err(|e| StoreError(e.to_string()))
}

/// Reviews a run's actions knowing only the workflow's intent and text-free summaries.
/// Its signature is the defense: hostile content cannot reach it.
pub trait Guard: Send + Sync {
    fn review<'a>(
        &'a self,
        intent: &'static str,
        actions: &'a [ActionSummary],
    ) -> BoxFuture<'a, Vec<bool>>;
}

struct AllowDeclared;
impl Guard for AllowDeclared {
    fn review<'a>(
        &'a self,
        _: &'static str,
        actions: &'a [ActionSummary],
    ) -> BoxFuture<'a, Vec<bool>> {
        Box::pin(async move { vec![true; actions.len()] })
    }
}

pub struct Runtime {
    workflows: Vec<Box<dyn Registered>>,
    providers: Arc<Providers>,
    guard: Box<dyn Guard>,
    limits: Limits,
    mode: Mode,
    store: Arc<dyn Store>,
    clock: Arc<dyn Clock>,
    /// Where a tripped budget asks a person.
    alerts: Option<Slug>,
    /// Set on shutdown. An in-flight run finishes, and no new one starts.
    stopped: AtomicBool,
}

impl Runtime {
    pub fn new(workflows: Vec<Box<dyn Registered>>, providers: Providers) -> Self {
        Self {
            workflows,
            providers: Arc::new(providers),
            guard: Box::new(AllowDeclared),
            limits: Limits::default(),
            mode: Mode::default(),
            store: Arc::new(MemoryStore::default()),
            clock: Arc::new(SystemClock),
            alerts: None,
            stopped: AtomicBool::new(false),
        }
    }

    pub fn with_store(mut self, store: Arc<dyn Store>) -> Self {
        self.store = store;
        self
    }

    pub fn with_clock(mut self, clock: Arc<dyn Clock>) -> Self {
        self.clock = clock;
        self
    }

    /// Files a question in `target` when a budget trips.
    pub fn alerting(mut self, target: Slug) -> Self {
        self.alerts = Some(target);
        self
    }

    /// Effects are dry-run unless this says otherwise.
    pub fn in_mode(mut self, mode: Mode) -> Self {
        self.mode = mode;
        self
    }

    pub fn guarded(mut self, guard: impl Guard + 'static) -> Self {
        self.guard = Box::new(guard);
        self
    }

    pub fn tightened(mut self, limits: Limits) -> Self {
        self.limits = self.limits.tighten(limits);
        self
    }

    pub fn limits(&self) -> Limits {
        self.limits
    }

    /// Everything a workflow declared that the binary did not set up.
    pub fn validate(&self) -> Result<(), crate::diag::Error> {
        let mut missing = vec![];
        for w in &self.workflows {
            for need in w.requirements() {
                if !self.providers.has(&need) {
                    missing.push(format!("{} needs a provider for {}", w.name(), need.name()));
                }
            }
            for agent in w.agent_names() {
                if self.providers.harness_of(agent).is_none() {
                    missing.push(format!("{} needs a harness for agent `{agent}`", w.name()));
                }
            }
            if self.mode == Mode::Live {
                self.missing_for_effects(w.as_ref(), &mut missing);
            }
        }
        if missing.is_empty() {
            Ok(())
        } else {
            Err(crate::diag::Error::MissingProviders { missing })
        }
    }

    fn missing_for_effects(&self, w: &dyn Registered, missing: &mut Vec<String>) {
        let may = w.may();
        let forged = may.contains(&EffectKind::Attest) || may.contains(&EffectKind::Push);
        if forged && self.providers.get::<dyn Forge>().is_none() {
            missing.push(format!(
                "{} may Attest or Push but no Forge is provided",
                w.name()
            ));
        }
        let tracked = may.contains(&EffectKind::File) || may.contains(&EffectKind::Ask);
        if tracked && self.providers.get::<dyn Tracker>().is_none() {
            missing.push(format!(
                "{} may File or Ask but no Tracker is provided",
                w.name()
            ));
        }
    }

    pub fn manifest(&self) -> Vec<Value> {
        self.workflows.iter().map(|w| w.manifest()).collect()
    }

    /// Routes to the first matching workflow.
    /// A delivery is seen only once this returns `Ok`, so an error means it can be redelivered.
    pub async fn ingest(&self, e: Envelope) -> Stored<Ingest> {
        let admission = self.admission(&e.kind, e.depth, e.origin, e.payload);
        self.store.admit(&e.source, &e.delivery_id, admission).await
    }

    fn admission(&self, kind: &str, depth: u32, origin: Origin, payload: Value) -> Admission {
        if depth > self.limits.max_depth {
            return Admission::Drop(Ingest::ChainStopped);
        }
        let Some(workflow) = self.workflows.iter().find(|w| w.kind() == kind) else {
            return Admission::Drop(Ingest::Unrouted);
        };
        let Ok(id) = workflow.identify(&payload) else {
            return Admission::Drop(Ingest::Rejected);
        };
        Admission::Queue(Pending {
            workflow: workflow.name().to_string(),
            key: id.key,
            payload,
            subjects: id.subjects,
            origin,
        })
    }

    fn budgets(&self) -> Vec<Budget> {
        self.workflows
            .iter()
            .map(|w| {
                let declared = w.runs_per_hour();
                Budget {
                    workflow: w.name(),
                    limit: self
                        .limits
                        .runs_cap
                        .map_or(declared, |cap| cap.min(declared)),
                }
            })
            .collect()
    }

    fn workflow(&self, name: &str) -> Stored<&dyn Registered> {
        self.workflows
            .iter()
            .find(|w| w.name() == name)
            .map(|w| w.as_ref())
            .ok_or_else(|| StoreError(format!("no workflow named `{name}` in this binary")))
    }

    async fn execute(&self, w: &dyn Registered, run: &Run) -> (Outcome, Vec<Subject>) {
        let cx = Cx::new(
            run.id.clone(),
            self.store.clone(),
            self.clock.clone(),
            self.mode,
            w.may(),
            w.agent_names(),
            self.providers.clone(),
        );
        let (id, workflow) = (run.id.clone(), w.name());
        match w.execute(&cx, &run.payload).await {
            Err(Halt::Stop(reason)) => (
                Outcome::Stopped {
                    run_id: id,
                    workflow,
                    reason,
                },
                vec![],
            ),
            Err(Halt::Failed(StepError(error))) => (
                Outcome::Failed {
                    run_id: id,
                    workflow,
                    error,
                },
                vec![],
            ),
            Ok(Decided { effects, links }) => {
                let (declared, undeclared): (Vec<_>, Vec<_>) = effects
                    .into_iter()
                    .partition(|e| w.may().contains(&e.kind()));
                let summaries: Vec<_> = declared.iter().map(Effect::summary).collect();
                let review = async { Ok(self.guard.review(w.intent(), &summaries).await) };
                let verdicts: Vec<bool> = match cx.record("guard", review).await {
                    Ok(verdicts) => verdicts,
                    Err(StepError(error)) => {
                        return (
                            Outcome::Failed {
                                run_id: id,
                                workflow,
                                error,
                            },
                            vec![],
                        );
                    }
                };
                let mut denied: Vec<_> = undeclared.iter().map(Effect::summary).collect();
                let mut allowed = vec![];
                for (i, effect) in declared.into_iter().enumerate() {
                    if verdicts.get(i).copied().unwrap_or(false) {
                        allowed.push(effect);
                    } else {
                        denied.push(effect.summary());
                    }
                }
                (
                    Outcome::Ran {
                        run_id: id,
                        workflow,
                        effects: allowed,
                        denied,
                    },
                    links,
                )
            }
        }
    }

    async fn work(&self, budgets: &[Budget], out: &Mutex<Vec<Outcome>>) -> Stored<()> {
        loop {
            if self.stopped.load(Ordering::SeqCst) {
                return Ok(());
            }
            let request = ClaimRequest {
                budgets,
                overall: self.limits.overall_cap,
                window: BUDGET_WINDOW,
                now: self.clock.now(),
            };
            match self.store.claim(request).await? {
                Claim::Idle => return Ok(()),
                Claim::Tripped { workflow, held } => {
                    let workflow = match workflow {
                        Some(name) => Some(self.workflow(&name)?.name()),
                        None => None,
                    };
                    self.ask_about_budget(workflow, held).await?;
                    let tripped = Outcome::BudgetTripped { workflow, held };
                    out.lock().unwrap().push(tripped);
                }
                Claim::Run(run) => {
                    let w = self.workflow(&run.workflow)?;
                    let (outcome, links) = self.execute(w, &run).await;
                    let status = match self.mode {
                        Mode::Live => EntryStatus::Pending,
                        Mode::DryRun => EntryStatus::WouldApply,
                    };
                    let planned = planned(&outcome)?;
                    self.store.release(&run.id, &links, planned, status).await?;
                    out.lock().unwrap().push(outcome);
                }
            }
        }
    }

    fn entry_status(&self) -> EntryStatus {
        match self.mode {
            Mode::Live => EntryStatus::Pending,
            Mode::DryRun => EntryStatus::WouldApply,
        }
    }

    /// Goes through the outbox like any effect, so it is delivered and retried the same way.
    async fn ask_about_budget(&self, workflow: Option<&str>, held: usize) -> Stored<()> {
        let Some(target) = self.alerts.clone() else {
            return Ok(());
        };
        let scope = workflow.unwrap_or("all workflows");
        let ask = Planned::Ask {
            question: format!("A run budget was reached for {scope}"),
            target,
            body: Untrusted::new(format!(
                "{held} event(s) are held and will resume as the hourly window opens."
            )),
        };
        let effect = serde_json::to_value(ask).map_err(|e| StoreError(e.to_string()))?;
        let key = format!("budget:{scope}:{}", self.clock.now().unix_timestamp());
        self.store.note(&key, effect, self.entry_status()).await
    }

    /// Runs independent keys concurrently, up to `limits.concurrency` at a time.
    pub async fn drain(&self) -> Stored<Vec<Outcome>> {
        let out = Mutex::default();
        let budgets = self.budgets();
        let workers = (0..self.limits.concurrency).map(|_| self.work(&budgets, &out));
        for finished in join_all(workers).await {
            finished?;
        }
        Ok(out.into_inner().unwrap())
    }

    /// Applies recorded effects, oldest first. A failure leaves an effect to be retried by a
    /// later call, up to a limit. Providers receive a stable key, so a crash between applying
    /// and marking it delivered changes nothing when it is applied again.
    pub async fn deliver(&self) -> Stored<Vec<Delivery>> {
        let mut report = vec![];
        for entry in self.store.undelivered().await? {
            let error = self.apply(&entry).await.err().map(|StepError(e)| e);
            match &error {
                None => self.store.delivered(&entry.key).await?,
                Some(error) => {
                    self.store
                        .delivery_failed(&entry.key, error, MAX_DELIVERY_ATTEMPTS)
                        .await?
                }
            }
            report.push(Delivery {
                key: entry.key,
                error,
            });
        }
        Ok(report)
    }

    async fn apply(&self, entry: &Entry) -> Result<(), StepError> {
        let planned: Planned = serde_json::from_value(entry.effect.clone())
            .map_err(|e| StepError(format!("{}: unreadable effect: {e}", entry.key)))?;
        let key = entry.key.as_str();
        match &planned {
            Planned::Await { .. } => Ok(()),
            Planned::Attest {
                context,
                state,
                repo,
                sha,
            } => {
                self.provider::<dyn Forge>("Forge")?
                    .attest(key, repo, sha, context, *state)
                    .await
            }
            Planned::Push {
                repo,
                pull,
                branch,
                message,
                base,
                patch,
            } => {
                let push = Push {
                    repo: repo.clone(),
                    pull: *pull,
                    branch: branch.clone(),
                    message: message.clone(),
                    base: base.clone(),
                    patch: patch.clone(),
                };
                self.provider::<dyn Forge>("Forge")?.push(key, &push).await
            }
            Planned::File {
                title,
                target,
                body,
            } => {
                self.provider::<dyn Tracker>("Tracker")?
                    .file(key, target, title, body)
                    .await
            }
            Planned::Ask {
                question,
                target,
                body,
            } => {
                self.provider::<dyn Tracker>("Tracker")?
                    .ask(key, target, question, body)
                    .await
            }
        }
    }

    fn provider<T: ?Sized + Send + Sync + 'static>(&self, name: &str) -> Result<Arc<T>, StepError> {
        self.providers
            .get::<T>()
            .ok_or_else(|| StepError(format!("no {name} is provided")))
    }

    /// What a run recorded, including what a dry run would have done.
    pub async fn effects_of(&self, run_id: &str) -> Stored<Vec<Entry>> {
        self.store.outbox(run_id).await
    }

    /// Lists every workflow's current subjects and ingests them as scanned events. Scanning
    /// twice is harmless, because scanned events have stable delivery IDs.
    pub async fn backfill(&self, repo: Option<&Slug>) -> Result<Backfill, crate::diag::Error> {
        let scanner = Scanner::new(self.providers.clone(), repo.cloned());
        let mut report = Backfill::default();
        for w in &self.workflows {
            let found = w.scan(&scanner).await.map_err(|StepError(reason)| {
                crate::diag::Error::ScanFailed {
                    workflow: w.name().to_string(),
                    reason,
                }
            })?;
            for event in found {
                report.found += 1;
                let ingest = self
                    .ingest(Envelope {
                        source: "scan".to_string(),
                        delivery_id: event.delivery_id,
                        kind: event.kind.to_string(),
                        depth: 0,
                        origin: Origin::Scan,
                        payload: event.payload,
                    })
                    .await?;
                match ingest {
                    Ingest::Duplicate => report.unchanged += 1,
                    Ingest::Rejected => report.rejected += 1,
                    _ => report.queued += 1,
                }
            }
        }
        Ok(report)
    }

    /// Starts no more runs. Queued work stays queued for the next start.
    pub fn stop_claiming(&self) {
        self.stopped.store(true, Ordering::SeqCst);
    }

    /// Whether the store answers.
    pub async fn ping(&self) -> Stored<()> {
        self.store.undelivered().await.map(|_| ())
    }

    /// Requeues what a crash left running. Call this once before draining after a start.
    pub async fn recover(&self) -> Stored<usize> {
        self.store.recover().await
    }

    /// Re-run from the log. Recorded reads, model calls, and guard verdicts are reused;
    /// policy runs again.
    pub async fn replay(&self, run_id: &str) -> Stored<Outcome> {
        let run = self
            .store
            .run(run_id)
            .await?
            .ok_or_else(|| StoreError(format!("no run named `{run_id}`")))?;
        let w = self.workflow(&run.workflow)?;
        Ok(self.execute(w, &run).await.0)
    }

    pub async fn runs_for(&self, subject: &Subject) -> Stored<Vec<String>> {
        self.store.runs_for(subject).await
    }
}
