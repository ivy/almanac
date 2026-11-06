//! Routes events, runs workflows, and applies limits. What it must remember is in `store`.
use crate::cx::{Cx, Halt, StepError};
use crate::effect::{ActionSummary, Effect};
use crate::providers::Providers;
use crate::store::{
    Admission, Budget, Claim, MemoryStore, Pending, Run, Store, StoreError, Stored,
};
use crate::workflow::{Decided, Registered, Subject};
use futures::future::{BoxFuture, join_all};
use serde_json::Value;
use std::sync::{Arc, Mutex};

pub const MAX_CHAIN_DEPTH: u32 = 3;
const MAX_CONCURRENT_RUNS: usize = 4;

/// Runtime limits. `tighten` can only lower them, however often it is called.
#[derive(Clone, Copy, Debug)]
pub struct Limits {
    pub max_depth: u32,
    pub runs_cap: Option<u32>,
    pub concurrency: usize,
}

impl Default for Limits {
    fn default() -> Self {
        Self {
            max_depth: MAX_CHAIN_DEPTH,
            runs_cap: None,
            concurrency: MAX_CONCURRENT_RUNS,
        }
    }
}

impl Limits {
    pub fn tighten(self, other: Limits) -> Self {
        Self {
            max_depth: self.max_depth.min(other.max_depth),
            runs_cap: match (self.runs_cap, other.runs_cap) {
                (Some(a), Some(b)) => Some(a.min(b)),
                (a, b) => a.or(b),
            },
            concurrency: self.concurrency.min(other.concurrency),
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
    BudgetTripped {
        workflow: &'static str,
        held: usize,
    },
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
    store: Arc<dyn Store>,
}

impl Runtime {
    pub fn new(workflows: Vec<Box<dyn Registered>>, providers: Providers) -> Self {
        Self {
            workflows,
            providers: Arc::new(providers),
            guard: Box::new(AllowDeclared),
            limits: Limits::default(),
            store: Arc::new(MemoryStore::default()),
        }
    }

    pub fn with_store(mut self, store: Arc<dyn Store>) -> Self {
        self.store = store;
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
    pub fn validate(&self) -> Result<(), String> {
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
        }
        if missing.is_empty() {
            Ok(())
        } else {
            Err(missing.join("; "))
        }
    }

    pub fn manifest(&self) -> Vec<Value> {
        self.workflows.iter().map(|w| w.manifest()).collect()
    }

    /// Routes to the first matching workflow.
    /// A delivery is seen only once this returns `Ok`, so an error means it can be redelivered.
    pub async fn ingest(&self, e: Envelope) -> Stored<Ingest> {
        let admission = self.admission(&e.kind, e.depth, e.payload);
        self.store.admit(&e.source, &e.delivery_id, admission).await
    }

    fn admission(&self, kind: &str, depth: u32, payload: Value) -> Admission {
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
            match self.store.claim(budgets).await? {
                Claim::Idle => return Ok(()),
                Claim::Tripped { workflow, held } => {
                    let workflow = self.workflow(&workflow)?.name();
                    let tripped = Outcome::BudgetTripped { workflow, held };
                    out.lock().unwrap().push(tripped);
                }
                Claim::Run(run) => {
                    let w = self.workflow(&run.workflow)?;
                    let (outcome, links) = self.execute(w, &run).await;
                    self.store.release(&run.id, &links).await?;
                    out.lock().unwrap().push(outcome);
                }
            }
        }
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
