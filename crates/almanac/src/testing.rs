//! Fakes, so a workflow's logic is testable with no model, service, or network.
use crate::agent::{Classifier, Grant, Harness, Pass};
use crate::cx::StepError;
use crate::effect::{AttestState, Forge, Push, Tracker};
use crate::taint::{Sha, Slug, Untrusted};
use futures::future::BoxFuture;
use serde_json::Value;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use tokio::sync::Notify;

#[derive(Clone, Default)]
pub struct ScriptedHarness {
    pub reply: Arc<Mutex<Value>>,
    pub grants: Arc<Mutex<Vec<Grant>>>,
    /// Inputs as sent, to check taint markers survive to the provider boundary.
    pub inputs: Arc<Mutex<Vec<Value>>>,
    /// Instructions as rendered.
    pub prompts: Arc<Mutex<Vec<String>>>,
    /// When set, a run parks here until notified, so tests can act mid-run.
    pub gate: Option<Arc<Notify>>,
}

impl ScriptedHarness {
    /// `reply` is the agent's answer, as JSON text.
    pub fn replying(reply: impl ToString) -> Self {
        let reply = serde_json::from_str(&reply.to_string()).expect("valid JSON");
        Self {
            reply: Arc::new(Mutex::new(reply)),
            ..Self::default()
        }
    }
    pub fn gated(mut self, gate: Arc<Notify>) -> Self {
        self.gate = Some(gate);
        self
    }
    pub fn calls(&self) -> usize {
        self.inputs.lock().unwrap().len()
    }
}

impl Harness for ScriptedHarness {
    fn run<'a>(
        &'a self,
        _: &'a Pass,
        prompt: &'a str,
        grant: Grant,
        input: Value,
    ) -> BoxFuture<'a, Result<Value, StepError>> {
        Box::pin(async move {
            self.prompts.lock().unwrap().push(prompt.to_string());
            self.grants.lock().unwrap().push(grant);
            self.inputs.lock().unwrap().push(input);
            if let Some(gate) = &self.gate {
                gate.notified().await;
            }
            Ok(self.reply.lock().unwrap().clone())
        })
    }
}

#[derive(Clone, Default)]
pub struct FixedClassifier {
    pub probability: f64,
    pub calls: Arc<AtomicUsize>,
}

impl FixedClassifier {
    pub fn answering(probability: f64) -> Self {
        Self {
            probability,
            ..Self::default()
        }
    }
    pub fn calls(&self) -> usize {
        self.calls.load(Ordering::SeqCst)
    }
}

impl Classifier for FixedClassifier {
    fn yes_probability<'a>(
        &'a self,
        _: &'a Pass,
        _: &'static str,
        _: Value,
    ) -> BoxFuture<'a, Result<f64, StepError>> {
        Box::pin(async move {
            self.calls.fetch_add(1, Ordering::SeqCst);
            Ok(self.probability)
        })
    }
}

/// Records statuses, and applies a key only once, as a real forge's status API does.
#[derive(Clone, Default)]
pub struct FakeForge {
    pub calls: Arc<Mutex<Vec<String>>>,
    pub applied: Arc<Mutex<Vec<(String, AttestState)>>>,
    pub pushes: Arc<Mutex<Vec<(String, Push)>>>,
    /// Failures to return before succeeding.
    pub fail_next: Arc<AtomicUsize>,
}

impl Forge for FakeForge {
    fn attest<'a>(
        &'a self,
        key: &'a str,
        repo: &'a Slug,
        sha: &'a Sha,
        context: &'a str,
        state: AttestState,
    ) -> BoxFuture<'a, Result<(), StepError>> {
        Box::pin(async move {
            self.calls.lock().unwrap().push(key.to_string());
            if self.fail_next.load(Ordering::SeqCst) > 0 {
                self.fail_next.fetch_sub(1, Ordering::SeqCst);
                return Err(StepError("forge is down".to_string()));
            }
            let status = format!("{repo}@{sha}:{context}");
            let mut applied = self.applied.lock().unwrap();
            applied.retain(|(existing, _)| existing != &status);
            applied.push((status, state));
            Ok(())
        })
    }

    fn push<'a>(&'a self, key: &'a str, push: &'a Push) -> BoxFuture<'a, Result<(), StepError>> {
        Box::pin(async move {
            self.calls.lock().unwrap().push(key.to_string());
            let mut pushes = self.pushes.lock().unwrap();
            if !pushes.iter().any(|(existing, _)| existing == key) {
                pushes.push((key.to_string(), push.clone()));
            }
            Ok(())
        })
    }
}

/// Records filed work, and applies a key only once.
#[derive(Clone, Default)]
pub struct FakeTracker {
    pub calls: Arc<Mutex<Vec<String>>>,
    /// `(key, target, title or question)`, with bodies left out because they stay tainted.
    pub filed: Arc<Mutex<Vec<(String, String, String)>>>,
}

impl FakeTracker {
    fn record(&self, key: &str, target: &Slug, title: &str) {
        self.calls.lock().unwrap().push(key.to_string());
        let mut filed = self.filed.lock().unwrap();
        if !filed.iter().any(|(existing, ..)| existing == key) {
            filed.push((key.to_string(), target.to_string(), title.to_string()));
        }
    }
}

impl Tracker for FakeTracker {
    fn file<'a>(
        &'a self,
        key: &'a str,
        target: &'a Slug,
        title: &'a str,
        _body: &'a Untrusted<String>,
    ) -> BoxFuture<'a, Result<(), StepError>> {
        Box::pin(async move {
            self.record(key, target, title);
            Ok(())
        })
    }

    fn ask<'a>(
        &'a self,
        key: &'a str,
        target: &'a Slug,
        question: &'a str,
        _body: &'a Untrusted<String>,
    ) -> BoxFuture<'a, Result<(), StepError>> {
        Box::pin(async move {
            self.record(key, target, question);
            Ok(())
        })
    }
}

/// What every `Store` must do. Run with `conformance::all`, giving it a function that opens a
/// new handle on a scope's state: calling it twice models a restart, or a second process.
pub mod conformance {
    use crate::runtime::Ingest;
    use crate::store::{Admission, Budget, Claim, EntryStatus, Pending, Store};
    use crate::taint::Slug;
    use crate::workflow::Subject;
    use serde_json::{Value, json};
    use std::sync::Arc;

    const W: &str = "workflow";
    const OTHER: &str = "other";
    const BUDGETS: &[Budget] = &[
        Budget {
            workflow: W,
            limit: 100,
        },
        Budget {
            workflow: OTHER,
            limit: 100,
        },
    ];

    fn subject() -> Subject {
        let repo: Slug = "owner/repo".parse().unwrap();
        Subject::repo(&repo)
    }

    fn queue(workflow: &str, key: &str, payload: Value) -> Admission {
        Admission::Queue(Pending {
            workflow: workflow.to_string(),
            key: key.to_string(),
            payload,
            subjects: vec![subject()],
        })
    }

    async fn admit(s: &Arc<dyn Store>, delivery: &str, key: &str, payload: Value) -> Ingest {
        s.admit("t", delivery, queue(W, key, payload))
            .await
            .unwrap()
    }

    async fn started(s: &Arc<dyn Store>) -> crate::store::Run {
        match s.claim(BUDGETS).await.unwrap() {
            Claim::Run(run) => run,
            other => panic!("expected a run, got {other:?}"),
        }
    }

    pub async fn all(open: impl Fn(&str) -> Arc<dyn Store>) {
        duplicates_survive_a_restart(&open("duplicates"), &open("duplicates")).await;
        dropped_deliveries_are_remembered(&open("dropped")).await;
        a_burst_coalesces_to_the_latest_event(&open("burst")).await;
        an_event_during_a_run_causes_one_rerun(&open("dirty")).await;
        one_of_two_concurrent_claims_wins(&open("race"), &open("race")).await;
        a_budget_trips_once(&open("budget")).await;
        oldest_work_runs_first(&open("order")).await;
        workflows_not_in_this_binary_are_left_alone(&open("unknown")).await;
        queued_work_and_the_log_survive_a_restart(&open("restart"), &open("restart")).await;
        recovery_requeues_interrupted_runs(&open("recover")).await;
        recovery_keeps_a_newer_pending_event(&open("newer")).await;
        subjects_link_runs_in_order_without_repeats(&open("subjects")).await;
        almanacs_do_not_see_each_other(&open("one"), &open("two")).await;
        effects_wait_in_order_until_delivered(&open("outbox"), &open("outbox")).await;
        dry_run_effects_are_never_delivered(&open("dry")).await;
        repeated_failures_stop_being_retried(&open("failing")).await;
    }

    async fn duplicates_survive_a_restart(before: &Arc<dyn Store>, after: &Arc<dyn Store>) {
        assert_eq!(admit(before, "d1", "k", json!(1)).await, Ingest::Queued);
        assert_eq!(admit(after, "d1", "k", json!(1)).await, Ingest::Duplicate);
    }

    async fn dropped_deliveries_are_remembered(s: &Arc<dyn Store>) {
        let drop = Admission::Drop(Ingest::Rejected);
        let first = s.admit("t", "d1", drop.clone()).await.unwrap();
        assert_eq!(first, Ingest::Rejected);
        assert_eq!(s.admit("t", "d1", drop).await.unwrap(), Ingest::Duplicate);
        assert_eq!(s.claim(BUDGETS).await.unwrap(), Claim::Idle);
    }

    async fn a_burst_coalesces_to_the_latest_event(s: &Arc<dyn Store>) {
        assert_eq!(admit(s, "d1", "k", json!(1)).await, Ingest::Queued);
        assert_eq!(admit(s, "d2", "k", json!(2)).await, Ingest::Coalesced);
        assert_eq!(started(s).await.payload, json!(2));
        assert_eq!(s.claim(BUDGETS).await.unwrap(), Claim::Idle);
    }

    async fn an_event_during_a_run_causes_one_rerun(s: &Arc<dyn Store>) {
        admit(s, "d1", "k", json!(1)).await;
        let run = started(s).await;
        assert_eq!(admit(s, "d2", "k", json!(2)).await, Ingest::MarkedDirty);
        assert_eq!(admit(s, "d3", "k", json!(3)).await, Ingest::MarkedDirty);
        assert_eq!(s.claim(BUDGETS).await.unwrap(), Claim::Idle);
        s.release(&run.id, &[], vec![], EntryStatus::Pending)
            .await
            .unwrap();
        let rerun = started(s).await;
        assert_eq!(rerun.payload, json!(3));
        s.release(&rerun.id, &[], vec![], EntryStatus::Pending)
            .await
            .unwrap();
        assert_eq!(s.claim(BUDGETS).await.unwrap(), Claim::Idle);
    }

    async fn one_of_two_concurrent_claims_wins(a: &Arc<dyn Store>, b: &Arc<dyn Store>) {
        admit(a, "d1", "k", json!(1)).await;
        let (x, y) = futures::join!(a.claim(BUDGETS), b.claim(BUDGETS));
        let claims = [x.unwrap(), y.unwrap()];
        let won = claims.iter().filter(|c| matches!(c, Claim::Run(_))).count();
        assert_eq!(won, 1, "{claims:?}");
        assert!(claims.contains(&Claim::Idle), "{claims:?}");
    }

    async fn a_budget_trips_once(s: &Arc<dyn Store>) {
        let one = [Budget {
            workflow: W,
            limit: 1,
        }];
        for (delivery, key) in [("d1", "a"), ("d2", "b"), ("d3", "c")] {
            admit(s, delivery, key, json!(1)).await;
        }
        assert!(matches!(s.claim(&one).await.unwrap(), Claim::Run(_)));
        let tripped = Claim::Tripped {
            workflow: W.to_string(),
            held: 2,
        };
        assert_eq!(s.claim(&one).await.unwrap(), tripped);
        assert_eq!(s.claim(&one).await.unwrap(), Claim::Idle);
    }

    async fn oldest_work_runs_first(s: &Arc<dyn Store>) {
        admit(s, "d1", "z", json!(1)).await;
        admit(s, "d2", "a", json!(2)).await;
        assert_eq!(started(s).await.key, "z");
        assert_eq!(started(s).await.key, "a");
    }

    async fn workflows_not_in_this_binary_are_left_alone(s: &Arc<dyn Store>) {
        s.admit("t", "d1", queue("retired", "k", json!(1)))
            .await
            .unwrap();
        assert_eq!(s.claim(BUDGETS).await.unwrap(), Claim::Idle);
    }

    async fn queued_work_and_the_log_survive_a_restart(
        before: &Arc<dyn Store>,
        after: &Arc<dyn Store>,
    ) {
        admit(before, "d1", "k", json!({"n": 1})).await;
        let run = started(after).await;
        assert_eq!(run.payload, json!({"n": 1}));
        before
            .record(&run.id, "registry", 1, json!({"seen": true}))
            .await
            .unwrap();
        let recorded = after.recorded(&run.id, "registry", 1).await.unwrap();
        assert_eq!(recorded, Some(json!({"seen": true})));
        assert_eq!(after.recorded(&run.id, "registry", 2).await.unwrap(), None);
        assert_eq!(after.run(&run.id).await.unwrap(), Some(run));
        assert_eq!(after.run("missing").await.unwrap(), None);
    }

    async fn recovery_requeues_interrupted_runs(s: &Arc<dyn Store>) {
        admit(s, "d1", "k", json!(1)).await;
        let interrupted = started(s).await;
        assert_eq!(s.claim(BUDGETS).await.unwrap(), Claim::Idle);
        assert_eq!(s.recover().await.unwrap(), 1);
        assert_eq!(s.recover().await.unwrap(), 0);
        let again = started(s).await;
        assert_ne!(again.id, interrupted.id);
        assert_eq!(again.payload, json!(1));
        let runs = s.runs_for(&subject()).await.unwrap();
        assert_eq!(runs, vec![interrupted.id, again.id]);
    }

    async fn recovery_keeps_a_newer_pending_event(s: &Arc<dyn Store>) {
        admit(s, "d1", "k", json!(1)).await;
        started(s).await;
        admit(s, "d2", "k", json!(2)).await;
        s.recover().await.unwrap();
        assert_eq!(started(s).await.payload, json!(2));
    }

    async fn subjects_link_runs_in_order_without_repeats(s: &Arc<dyn Store>) {
        admit(s, "d1", "a", json!(1)).await;
        let first = started(s).await;
        s.release(&first.id, &[subject()], vec![], EntryStatus::Pending)
            .await
            .unwrap();
        admit(s, "d2", "b", json!(1)).await;
        let second = started(s).await;
        let runs = s.runs_for(&subject()).await.unwrap();
        assert_eq!(runs, vec![first.id, second.id]);
    }

    async fn effects_wait_in_order_until_delivered(
        before: &Arc<dyn Store>,
        after: &Arc<dyn Store>,
    ) {
        admit(before, "d1", "k", json!(1)).await;
        let run = started(before).await;
        let effects = vec![json!("first"), json!("second")];
        before
            .release(&run.id, &[], effects, EntryStatus::Pending)
            .await
            .unwrap();
        let waiting = after.undelivered().await.unwrap();
        let keys: Vec<_> = waiting.iter().map(|e| e.key.clone()).collect();
        assert_eq!(keys, [format!("{}:0", run.id), format!("{}:1", run.id)]);
        assert_eq!(waiting[1].effect, json!("second"));
        after.delivered(&keys[0]).await.unwrap();
        after.delivered(&keys[0]).await.unwrap();
        let left = after.undelivered().await.unwrap();
        assert_eq!(left.len(), 1);
        assert_eq!(left[0].key, keys[1]);
        let recorded = after.outbox(&run.id).await.unwrap();
        assert_eq!(recorded[0].status, EntryStatus::Applied);
        assert_eq!(recorded[1].status, EntryStatus::Pending);
    }

    async fn dry_run_effects_are_never_delivered(s: &Arc<dyn Store>) {
        admit(s, "d1", "k", json!(1)).await;
        let run = started(s).await;
        s.release(&run.id, &[], vec![json!("x")], EntryStatus::WouldApply)
            .await
            .unwrap();
        assert_eq!(s.undelivered().await.unwrap(), vec![]);
        let recorded = s.outbox(&run.id).await.unwrap();
        assert_eq!(recorded[0].status, EntryStatus::WouldApply);
    }

    async fn repeated_failures_stop_being_retried(s: &Arc<dyn Store>) {
        admit(s, "d1", "k", json!(1)).await;
        let run = started(s).await;
        s.release(&run.id, &[], vec![json!("x")], EntryStatus::Pending)
            .await
            .unwrap();
        let key = format!("{}:0", run.id);
        s.delivery_failed(&key, "down", 2).await.unwrap();
        assert_eq!(s.undelivered().await.unwrap()[0].attempts, 1);
        s.delivery_failed(&key, "down", 2).await.unwrap();
        assert_eq!(s.undelivered().await.unwrap(), vec![]);
        let recorded = s.outbox(&run.id).await.unwrap();
        assert_eq!(recorded[0].status, EntryStatus::Failed);
    }

    async fn almanacs_do_not_see_each_other(one: &Arc<dyn Store>, two: &Arc<dyn Store>) {
        assert_eq!(admit(one, "d1", "k", json!(1)).await, Ingest::Queued);
        assert_eq!(admit(two, "d1", "k", json!(2)).await, Ingest::Queued);
        assert_eq!(started(one).await.payload, json!(1));
        assert_eq!(started(two).await.payload, json!(2));
    }
}
