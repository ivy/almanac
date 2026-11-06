//! Almanac: event-driven workflows, deterministic up to the point of inference.
extern crate self as almanac;

pub mod agent;
pub mod app;
pub mod config;
pub mod cx;
pub mod effect;
pub mod providers;
pub mod runtime;
pub mod taint;
pub mod testing;
pub mod wire;
pub mod workflow;

pub use app::main;
pub use taint::__private;

/// Everything a workflow author needs.
pub mod prelude {
    pub use crate::agent::{AgentSpec, Classifier, Harness, Pass, Quarantined, Template, Worker};
    pub use crate::config::Config;
    pub use crate::cx::{Cx, Halt, StepError};
    pub use crate::effect::{Effect, EffectKind};
    pub use crate::providers::Providers;
    pub use crate::taint::{Clean, Finite, Observed, Sha, Slug, Untrusted};
    pub use crate::wire::{Event, Payload};
    pub use crate::workflow::{Registered, Subject, Workflow};
    pub use crate::{clean, observed, output};
    pub use almanac_macros::{prompt, workflow};
    pub use futures::future::BoxFuture;
}
