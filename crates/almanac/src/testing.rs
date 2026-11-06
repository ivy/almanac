//! Fakes, so a workflow's logic is testable with no model, service, or network.
use crate::agent::{Classifier, Grant, Harness, Pass};
use crate::cx::StepError;
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
        Self { reply: Arc::new(Mutex::new(reply)), ..Self::default() }
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
    fn run<'a>(&'a self, _: &'a Pass, prompt: &'a str, grant: Grant, input: Value)
        -> BoxFuture<'a, Result<Value, StepError>> {
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
        Self { probability, ..Self::default() }
    }
    pub fn calls(&self) -> usize {
        self.calls.load(Ordering::SeqCst)
    }
}

impl Classifier for FixedClassifier {
    fn yes_probability<'a>(&'a self, _: &'a Pass, _: &'static str, _: Value) -> BoxFuture<'a, Result<f64, StepError>> {
        Box::pin(async move {
            self.calls.fetch_add(1, Ordering::SeqCst);
            Ok(self.probability)
        })
    }
}
