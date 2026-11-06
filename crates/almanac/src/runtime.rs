//! In-memory stand-in for the stateful service. `seen` is a unique index in a real build,
//! and `slots` a table whose partial unique index enforces one active run per key.
use crate::cx::{Cx, Halt, RunLog, StepError};
use crate::effect::{ActionSummary, Effect};
use crate::providers::Providers;
use crate::workflow::{Decided, Registered, Subject};
use futures::future::{BoxFuture, join_all};
use serde_json::Value;
use std::collections::{BTreeMap, HashMap, HashSet};
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

#[derive(Debug, PartialEq)]
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

type Slot = (usize, String);

struct Pending {
    payload: Value,
    subjects: Vec<Subject>,
}

/// The per-key state machine. Every transition is one match on this.
enum SlotState {
    Queued(Pending),
    Running { next: Option<Pending> },
}

#[derive(Clone)]
struct Run {
    id: String,
    workflow: usize,
    payload: Value,
}

#[derive(Default)]
struct State {
    seen: HashSet<(String, String)>,
    slots: BTreeMap<Slot, SlotState>,
    started: HashMap<usize, u32>,
    tripped: HashSet<usize>,
    runs: HashMap<String, Run>,
    subjects: HashMap<Subject, Vec<String>>,
    next_run: u64,
}

pub struct Runtime {
    workflows: Vec<Box<dyn Registered>>,
    providers: Arc<Providers>,
    guard: Box<dyn Guard>,
    limits: Limits,
    state: Mutex<State>,
    log: RunLog,
}

impl Runtime {
    pub fn new(workflows: Vec<Box<dyn Registered>>, providers: Providers) -> Self {
        Self {
            workflows,
            providers: Arc::new(providers),
            guard: Box::new(AllowDeclared),
            limits: Limits::default(),
            state: Mutex::default(),
            log: Arc::default(),
        }
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

    /// Routes to the first matching workflow; a real build fans out to all of them.
    pub fn ingest(&self, e: Envelope) -> Ingest {
        let mut s = self.state.lock().unwrap();
        if !s.seen.insert((e.source, e.delivery_id)) {
            return Ingest::Duplicate;
        }
        if e.depth > self.limits.max_depth {
            return Ingest::ChainStopped;
        }
        let Some(workflow) = self.workflows.iter().position(|w| w.kind() == e.kind) else {
            return Ingest::Unrouted;
        };
        let Ok(id) = self.workflows[workflow].identify(&e.payload) else {
            return Ingest::Rejected;
        };
        let pending = Pending {
            payload: e.payload,
            subjects: id.subjects,
        };
        match s.slots.get_mut(&(workflow, id.key.clone())) {
            None => {
                s.slots
                    .insert((workflow, id.key), SlotState::Queued(pending));
                Ingest::Queued
            }
            Some(SlotState::Queued(queued)) => {
                *queued = pending;
                Ingest::Coalesced
            }
            Some(SlotState::Running { next }) => {
                *next = Some(pending);
                Ingest::MarkedDirty
            }
        }
    }

    fn claim(&self, out: &Mutex<Vec<Outcome>>) -> Option<(Slot, Run)> {
        let mut s = self.state.lock().unwrap();
        let queued: Vec<Slot> = s
            .slots
            .iter()
            .filter(|(_, state)| matches!(state, SlotState::Queued(_)))
            .map(|(slot, _)| slot.clone())
            .collect();
        for slot in queued {
            let workflow = slot.0;
            if s.tripped.contains(&workflow) {
                continue;
            }
            let declared = self.workflows[workflow].runs_per_hour();
            let limit = self
                .limits
                .runs_cap
                .map_or(declared, |cap| cap.min(declared));
            if *s.started.get(&workflow).unwrap_or(&0) >= limit {
                s.tripped.insert(workflow);
                let held = s
                    .slots
                    .iter()
                    .filter(|(k, v)| k.0 == workflow && matches!(v, SlotState::Queued(_)))
                    .count();
                out.lock().unwrap().push(Outcome::BudgetTripped {
                    workflow: self.workflows[workflow].name(),
                    held,
                });
                continue;
            }
            let Some(SlotState::Queued(pending)) = s
                .slots
                .insert(slot.clone(), SlotState::Running { next: None })
            else {
                unreachable!("listed as queued under the same lock");
            };
            *s.started.entry(workflow).or_default() += 1;
            s.next_run += 1;
            let run = Run {
                id: format!("run-{}", s.next_run),
                workflow,
                payload: pending.payload,
            };
            s.runs.insert(run.id.clone(), run.clone());
            for subject in pending.subjects {
                s.subjects.entry(subject).or_default().push(run.id.clone());
            }
            return Some((slot, run));
        }
        None
    }

    fn release(&self, slot: Slot, run: &Run, links: Vec<Subject>) {
        let mut s = self.state.lock().unwrap();
        for subject in links {
            s.subjects.entry(subject).or_default().push(run.id.clone());
        }
        if let Some(SlotState::Running {
            next: Some(pending),
        }) = s.slots.remove(&slot)
        {
            s.slots.insert(slot, SlotState::Queued(pending));
        }
    }

    async fn execute(&self, run: &Run) -> (Outcome, Vec<Subject>) {
        let w = &self.workflows[run.workflow];
        let cx = Cx::new(
            run.id.clone(),
            self.log.clone(),
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

    async fn work(&self, out: &Mutex<Vec<Outcome>>) {
        while let Some((slot, run)) = self.claim(out) {
            let (outcome, links) = self.execute(&run).await;
            self.release(slot, &run, links);
            out.lock().unwrap().push(outcome);
        }
    }

    /// Runs independent keys concurrently, up to `limits.concurrency` at a time.
    pub async fn drain(&self) -> Vec<Outcome> {
        let out = Mutex::default();
        join_all((0..self.limits.concurrency).map(|_| self.work(&out))).await;
        out.into_inner().unwrap()
    }

    /// Re-run from the log. Recorded reads, model calls, and guard verdicts are reused;
    /// policy runs again.
    pub async fn replay(&self, run_id: &str) -> Outcome {
        let run = self.state.lock().unwrap().runs[run_id].clone();
        self.execute(&run).await.0
    }

    pub fn runs_for(&self, subject: &Subject) -> Vec<String> {
        self.state
            .lock()
            .unwrap()
            .subjects
            .get(subject)
            .cloned()
            .unwrap_or_default()
    }
}
