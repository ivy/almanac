//! The store for tests and one-shot commands. It forgets everything when dropped.
use super::{
    Admission, Claim, ClaimRequest, Entry, EntryStatus, Pending, Run, Store, Stored, Trace,
};
use crate::runtime::Ingest;
use crate::workflow::Subject;
use futures::future::BoxFuture;
use serde_json::Value;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Mutex;
use time::OffsetDateTime;

type Slot = (String, String);

struct Waiting {
    seq: u64,
    lane: u8,
    pending: Pending,
}

#[derive(Default)]
struct State {
    seen: HashSet<(String, String)>,
    pending: BTreeMap<Slot, Waiting>,
    running: HashMap<Slot, String>,
    started: Vec<(String, OffsetDateTime)>,
    tripped: HashMap<Option<String>, OffsetDateTime>,
    runs: HashMap<String, Run>,
    subjects: HashMap<Subject, Vec<String>>,
    log: HashMap<(String, String, usize), Value>,
    outbox: Vec<Entry>,
    ledger: HashMap<Slot, Vec<Trace>>,
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
        let lane = pending.origin.lane();
        self.pending.insert(slot, Waiting { seq, lane, pending });
    }

    /// A key waits for its last run's effects, because a run must see what they changed.
    fn undelivered_on(&self, slot: &Slot) -> bool {
        let of_slot = |run_id: &String| {
            let run = self.runs.get(run_id);
            run.is_some_and(|r| r.workflow == slot.0 && r.key == slot.1)
        };
        self.outbox
            .iter()
            .any(|e| e.status == EntryStatus::Pending && e.run_id.as_ref().is_some_and(of_slot))
    }

    fn is_free(&self, slot: &Slot) -> bool {
        !self.running.contains_key(slot) && !self.undelivered_on(slot)
    }

    fn claimable(&self, workflow: &str) -> usize {
        self.pending
            .keys()
            .filter(|slot| slot.0 == workflow && self.is_free(slot))
            .count()
    }

    fn started_since(&self, workflow: Option<&str>, since: OffsetDateTime) -> u32 {
        let matches = |w: &str| workflow.is_none_or(|wanted| wanted == w);
        let started = self.started.iter();
        started.filter(|(w, at)| matches(w) && *at > since).count() as u32
    }

    /// Records a trip unless one was reported in this window.
    fn report(
        &mut self,
        scope: Option<String>,
        now: OffsetDateTime,
        since: OffsetDateTime,
    ) -> bool {
        if self.tripped.get(&scope).is_some_and(|at| *at > since) {
            return false;
        }
        self.tripped.insert(scope, now);
        true
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
            waiting.lane = waiting.lane.min(pending.origin.lane());
            // As in PostgreSQL: a fresh event must not reset the chain it joins.
            let depth = waiting.pending.depth.max(pending.depth);
            waiting.pending = Pending { depth, ..pending };
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

    fn claim_now(&self, request: ClaimRequest<'_>) -> Claim {
        let mut s = self.state.lock().unwrap();
        let now = request.now;
        let since = now - request.window;
        let held: usize = request
            .budgets
            .iter()
            .map(|b| s.claimable(b.workflow))
            .sum();
        if let Some(limit) = request.overall
            && s.started_since(None, since) >= limit
        {
            if held > 0 && s.report(None, now, since) {
                return Claim::Tripped {
                    workflow: None,
                    held,
                };
            }
            return Claim::Idle;
        }
        let mut open = vec![];
        for budget in request.budgets {
            if s.started_since(Some(budget.workflow), since) < budget.limit {
                open.push(budget.workflow);
                continue;
            }
            let held = s.claimable(budget.workflow);
            let scope = Some(budget.workflow.to_string());
            if held > 0 && s.report(scope.clone(), now, since) {
                return Claim::Tripped {
                    workflow: scope,
                    held,
                };
            }
        }
        let next = s
            .pending
            .iter()
            .filter(|(slot, _)| s.is_free(slot) && open.contains(&slot.0.as_str()))
            .min_by_key(|(_, waiting)| (waiting.lane, waiting.seq))
            .map(|(slot, _)| slot.clone());
        let Some(slot) = next else {
            return Claim::Idle;
        };
        let waiting = s.pending.remove(&slot).expect("listed under the same lock");
        s.next_run += 1;
        let run = Run {
            id: format!("run-{}", s.next_run),
            workflow: slot.0.clone(),
            key: slot.1.clone(),
            payload: waiting.pending.payload,
            origin: waiting.pending.origin,
            depth: waiting.pending.depth,
        };
        s.started.push((slot.0.clone(), now));
        s.running.insert(slot, run.id.clone());
        s.runs.insert(run.id.clone(), run.clone());
        for subject in waiting.pending.subjects {
            s.link(subject, &run.id);
        }
        Claim::Run(run)
    }

    fn recover_now(&self) -> usize {
        let mut s = self.state.lock().unwrap();
        let interrupted: Vec<(Slot, String)> = s.running.drain().collect();
        for (slot, run_id) in &interrupted {
            if s.pending.contains_key(slot) {
                continue;
            }
            let payload = s.runs[run_id].payload.clone();
            let origin = s.runs[run_id].origin;
            let depth = s.runs[run_id].depth;
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
                origin,
                depth,
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

    fn claim<'a>(&'a self, request: ClaimRequest<'a>) -> BoxFuture<'a, Stored<Claim>> {
        Box::pin(async move { Ok(self.claim_now(request)) })
    }

    fn remember<'a>(
        &'a self,
        workflow: &'a str,
        key: &'a str,
        trace: Trace,
    ) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            let slot = (workflow.to_string(), key.to_string());
            let mut s = self.state.lock().unwrap();
            s.ledger.entry(slot).or_default().push(trace);
            Ok(())
        })
    }

    fn traces<'a>(
        &'a self,
        workflow: &'a str,
        key: &'a str,
        since: OffsetDateTime,
    ) -> BoxFuture<'a, Stored<Vec<Trace>>> {
        Box::pin(async move {
            let slot = (workflow.to_string(), key.to_string());
            let s = self.state.lock().unwrap();
            let all = s.ledger.get(&slot).into_iter().flatten();
            Ok(all.filter(|t| t.at > since).cloned().collect())
        })
    }

    fn note<'a>(
        &'a self,
        key: &'a str,
        effect: Value,
        status: EntryStatus,
    ) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            let mut s = self.state.lock().unwrap();
            if !s.outbox.iter().any(|e| e.key == key) {
                s.outbox.push(Entry {
                    key: key.to_string(),
                    run_id: None,
                    effect,
                    status,
                    attempts: 0,
                });
            }
            Ok(())
        })
    }

    fn release<'a>(
        &'a self,
        run_id: &'a str,
        links: &'a [Subject],
        effects: Vec<Value>,
        status: EntryStatus,
    ) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            let mut s = self.state.lock().unwrap();
            s.running.retain(|_, id| id != run_id);
            for subject in links {
                s.link(subject.clone(), run_id);
            }
            for (i, effect) in effects.into_iter().enumerate() {
                s.outbox.push(Entry {
                    key: format!("{run_id}:{i}"),
                    run_id: Some(run_id.to_string()),
                    effect,
                    status,
                    attempts: 0,
                });
            }
            Ok(())
        })
    }

    fn undelivered(&self) -> BoxFuture<'_, Stored<Vec<Entry>>> {
        Box::pin(async move {
            let s = self.state.lock().unwrap();
            let waiting = s.outbox.iter().filter(|e| e.status == EntryStatus::Pending);
            Ok(waiting.cloned().collect())
        })
    }

    fn outbox<'a>(&'a self, run_id: &'a str) -> BoxFuture<'a, Stored<Vec<Entry>>> {
        Box::pin(async move {
            let s = self.state.lock().unwrap();
            let entries = s
                .outbox
                .iter()
                .filter(|e| e.run_id.as_deref() == Some(run_id));
            Ok(entries.cloned().collect())
        })
    }

    fn delivered<'a>(&'a self, key: &'a str) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            let mut s = self.state.lock().unwrap();
            if let Some(entry) = s.outbox.iter_mut().find(|e| e.key == key) {
                entry.status = EntryStatus::Applied;
            }
            Ok(())
        })
    }

    fn delivery_failed<'a>(
        &'a self,
        key: &'a str,
        _error: &'a str,
        give_up: u32,
    ) -> BoxFuture<'a, Stored<()>> {
        Box::pin(async move {
            let mut s = self.state.lock().unwrap();
            if let Some(entry) = s.outbox.iter_mut().find(|e| e.key == key) {
                entry.attempts += 1;
                if entry.attempts >= give_up {
                    entry.status = EntryStatus::Failed;
                }
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
