//! Agent handles. The capability profile is part of the type.
use crate::cx::{Cx, Halt, Record, StepError};
use crate::taint::{Clean, Untrusted};
use futures::future::BoxFuture;
use serde::Serialize;
use serde_json::Value;
use std::marker::PhantomData;

/// What a session is given. The sandbox beneath the harness must enforce this.
#[derive(Clone, Copy, Debug, PartialEq, Serialize)]
pub struct Grant {
    pub write: bool,
    pub egress: bool,
    pub secrets: bool,
}

/// Proof that a call came through `Cx`. Only this crate can make one, so a workflow
/// cannot reach a model without the call being recorded.
pub struct Pass(());

impl Pass {
    pub(crate) fn new() -> Self {
        Self(())
    }
}

pub trait Harness: Send + Sync + 'static {
    fn run<'a>(&'a self, pass: &'a Pass, prompt: &'a str, grant: Grant, input: Value)
        -> BoxFuture<'a, Result<Value, StepError>>;
}

pub trait Classifier: Send + Sync + 'static {
    fn yes_probability<'a>(&'a self, pass: &'a Pass, question: &'static str, state: Value)
        -> BoxFuture<'a, Result<f64, StepError>>;
}

pub trait Profile {
    const NAME: &'static str;
    const GRANT: Grant;
}

/// Reads hostile content. No writes, no egress, no secrets; output stays tainted.
pub struct Quarantined;
impl Profile for Quarantined {
    const NAME: &'static str = "quarantined";
    const GRANT: Grant = Grant { write: false, egress: false, secrets: false };
}

/// Can change things. Accepts only `Clean` input and returns only `Clean` output.
pub struct Worker;
impl Profile for Worker {
    const NAME: &'static str = "worker";
    const GRANT: Grant = Grant { write: true, egress: true, secrets: true };
}

/// A prompt with its variables as typed fields. Use `#[prompt("file.md")]` on a struct: the
/// file is embedded in the binary, and a mismatch with the struct is a compile error.
pub trait Template: Send + Sync + 'static {
    const SOURCE: &'static str;
    const VARS: &'static [&'static str];
    fn render(&self) -> String;
}

/// How an agent appears in the manifest, so a capability change shows up in review.
#[derive(Clone, Debug, Serialize)]
pub struct AgentInfo {
    pub name: &'static str,
    pub profile: &'static str,
    pub grant: Grant,
    pub prompt: &'static str,
    pub prompt_vars: &'static [&'static str],
}

/// An agent's declaration: a `const` a workflow names. Its type fixes both what the agent
/// may do and which prompt it takes.
pub struct AgentSpec<P, T> {
    name: &'static str,
    _types: PhantomData<fn() -> (P, T)>,
}

impl<P, T> AgentSpec<P, T> {
    pub const fn name(&self) -> &'static str {
        self.name
    }
}

impl<P: Profile, T: Template> AgentSpec<P, T> {
    pub fn info(&self) -> AgentInfo {
        AgentInfo { name: self.name, profile: P::NAME, grant: P::GRANT, prompt: T::SOURCE, prompt_vars: T::VARS }
    }
}

impl<T> AgentSpec<Quarantined, T> {
    pub const fn quarantined(name: &'static str) -> Self {
        Self { name, _types: PhantomData }
    }
}

impl<T> AgentSpec<Worker, T> {
    pub const fn worker(name: &'static str) -> Self {
        Self { name, _types: PhantomData }
    }
}

/// An agent bound to a run, from `cx.agent(SPEC)`.
pub struct Bound<'a, P, T> {
    pub(crate) cx: &'a Cx,
    pub(crate) spec: AgentSpec<P, T>,
}

impl<P: Profile, T: Template> Bound<'_, P, T> {
    async fn invoke<I: Record, O: Record>(&self, prompt: &T, input: &I) -> Result<O, Halt> {
        let name = self.spec.name;
        self.cx.ensure_declared(name)?;
        let harness = self.cx.harness_of(name)?;
        let prompt = prompt.render();
        let input = serde_json::to_value(input).map_err(StepError::from)?;
        let call = async move {
            let value = harness.run(&Pass::new(), &prompt, P::GRANT, input).await?;
            Ok(serde_json::from_value(value)?)
        };
        Ok(self.cx.record(name, call).await?)
    }
}

impl<T: Template> Bound<'_, Quarantined, T> {
    pub async fn read<I: Record, O: Record>(&self, prompt: T, input: &I) -> Result<Untrusted<O>, Halt> {
        self.invoke(&prompt, input).await.map(Untrusted::new)
    }
}

impl<T: Template> Bound<'_, Worker, T> {
    pub async fn act<I: Record + Clean, O: Record + Clean>(&self, prompt: T, input: &I) -> Result<O, Halt> {
        self.invoke(&prompt, input).await
    }
}
