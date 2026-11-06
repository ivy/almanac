//! The store for tests and one-shot commands. It forgets everything when dropped.
use super::{Admission, Budget, Claim, Pending, Run, Store, Stored};
use crate::runtime::Ingest;
use crate::workflow::Subject;
use futures::future::BoxFuture;
use serde_json::Value;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Mutex;

type Slot = (String, String);

struct Waiting {
    seq: u64,
    pending: Pending,
}

#[derive(Default)]
struct State {
    seen: HashSet<(String, String)>,
    pending: BTreeMap<Slot, Waiting>,
    running: HashMap<Slot, String>,
    started: HashMap<String, u32>,
    tripped: HashSet<String>,
    runs: HashMap<String, Run>,
    subjects: HashMap<Subject, Vec<String>>,
    log: HashMap<(String, String, usize), Value>,
    next_seq: u64,
    next_run: u64,
}

impl State {
    fn link(&mut self, subject: Subject, run_id: &str) {
        let runs = self.subjects.entry(subject).or_default();
        if !runs.iter().any(|id| id == run_id) {
            runs.push(run_id.to_string());
        }
    }

    fn enqueue(&mut self, slot: Slot, pending: Pending) {
        self.next_seq += 1;
        let seq = self.next_seq;
        self.pending.insert(slot, Waiting { seq, pending });
    }

    fn claimable(&self, workflow: &str) -> usize {
        self.pending
            .iter()
            .filter(|(slot, _)| slot.0 == workflow && !self.running.contains_key(*slot))
            .count()
    }
}

#[derive(Default)]
pub struct MemoryStore {
    state: Mutex<State>,
}

impl MemoryStore {
    fn admit_now(&self, source: &str, delivery_id: &str, admission: Admission) -> Ingest {
        let mut s = self.state.lock().unwrap();
        if !s.seen.insert((source.to_string(), delivery_id.to_string())) {
            return Ingest::Duplicate;
        }
        let pending = match admission {
            Admission::Drop(ingest) => return ingest,
            Admission::Queue(pending) => pending,
        };
        let slot = (pending.workflow.clone(), pending.key.clone());
        let running = s.running.contains_key(&slot);
        if let Some(waiting) = s.pending.get_mut(&slot) {
            waiting.pending = pending;
            return if running {
                Ingest::MarkedDirty
            } else {
                Ingest::Coalesced
            };
        }
        s.enqueue(slot, pending);
        if running {
            Ingest::MarkedDirty
        } else {
            Ingest::Queued
        }
    }

    fn claim_now(&self, budgets: &[Budget]) -> Claim {
        let mut s = self.state.lock().unwrap();
        let mut candidates: Vec<(u64, Slot)> = s
            .pending
            .iter()
            .filter(|(slot, _)| !s.running.contains_key(*slot))
            .map(|(slot, waiting)| (waiting.seq, slot.clone()))
            .collect();
        candidates.sort();
        for (_, slot) in candidates {
            let Some(budget) = budgets.iter().find(|b| b.workflow == slot.0) else {
                continue;
            };
            if s.tripped.contains(&slot.0) {
                continue;
            }
            if *s.started.get(&slot.0).unwrap_or(&0) >= budget.limit {
                s.tripped.insert(slot.0.clone());
                let held = s.claimable(&slot.0);
                return Claim::Tripped {
                    workflow: slot.0,
                    held,
                };
            }
            let waiting = s.pending.remove(&slot).expect("listed under the same lock");
            s.next_run += 1;
            let run = Run {
                id: format!("run-{}", s.next_run),
                workflow: slot.0.clone(),
                key: slot.1.clone(),
                payload: waiting.pending.payload,
            };
            *s.started.entry(slot.0.clone()).or_default() += 1;
            s.running.insert(slot, run.id.clone());
            s.runs.insert(run.id.clone(), run.clone());
            for subject in waiting.pending.subjects {
                s.link(subject, &run.id);
            }
            return Claim::Run(run);
        }
        Claim::Idle
    }

    fn recover_now(&self) -> usize {
        let mut s = self.state.lock().unwrap();
        let interrupted: Vec<(Slot, String)> = s.running.drain().collect();
        for (slot, run_id) in &interrupted {
            if s.pending.contains_key(slot) {
                continue;
            }
            let payload = s.runs[run_id].payload.clone();
            let subjects = s
                .subjects
                .iter()
                .filter(|(_, runs)| runs.contains(run_id))
                .map(|(subject, _)| subject.clone())
                .collect();
            let pending = Pending {
                workflow: slot.0.clone(),
                key: slot.1.clone(),
                payload,
                subjects,
            };
            s.enqueue(slot.clone(), pending);
        }
        interrupted.len()
    }
}

impl Store for MemoryStore {
    fn admit<'a>(
        &'a self,
        source: &'a str,
        delivery_id: &'a str,
        admission: Admission,
    ) -> BoxFuture<'a, Stored<Ingest>> {
        Box::pin(async move { Ok(self.admit_now(source, delivery_id, admission)) })
    }

    fn claim<'a>(&'a self, budgets: &'a [Budget]) -> BoxFuture<'a, Stored<Claim>> {
        Box::pin(async move { Ok(self.claim_now(budgets)) })
    }

    fn release<'a>(&'a self, run_id: &'a str, links: &'a [Subject]) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            let mut s = self.state.lock().unwrap();
            s.running.retain(|_, id| id != run_id);
            for subject in links {
                s.link(subject.clone(), run_id);
            }
            Ok(())
        })
    }

    fn recover(&self) -> BoxFuture<'_, Stored<usize>> {
        Box::pin(async move { Ok(self.recover_now()) })
    }

    fn run<'a>(&'a self, run_id: &'a str) -> BoxFuture<'a, Stored<Option<Run>>> {
        Box::pin(async move { Ok(self.state.lock().unwrap().runs.get(run_id).cloned()) })
    }

    fn recorded<'a>(
        &'a self,
        run_id: &'a str,
        name: &'a str,
        occurrence: usize,
    ) -> BoxFuture<'a, Stored<Option<Value>>> {
        Box::pin(async move {
            let key = (run_id.to_string(), name.to_string(), occurrence);
            Ok(self.state.lock().unwrap().log.get(&key).cloned())
        })
    }

    fn record<'a>(
        &'a self,
        run_id: &'a str,
        name: &'a str,
        occurrence: usize,
        value: Value,
    ) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            let key = (run_id.to_string(), name.to_string(), occurrence);
            self.state.lock().unwrap().log.insert(key, value);
            Ok(())
        })
    }

    fn runs_for<'a>(&'a self, subject: &'a Subject) -> BoxFuture<'a, Stored<Vec<String>>> {
        Box::pin(async move {
            let s = self.state.lock().unwrap();
            Ok(s.subjects.get(subject).cloned().unwrap_or_default())
        })
    }
}
