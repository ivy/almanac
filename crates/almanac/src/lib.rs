//! Almanac: event-driven workflows, deterministic up to the point of inference.
extern crate self as almanac;

pub mod agent;
pub mod app;
pub mod clock;
pub mod config;
pub mod consumer;
pub mod cooled;
pub mod cx;
pub mod daemon;
pub mod diag;
pub mod effect;
pub mod patch;
pub mod providers;
pub mod runtime;
pub mod store;
pub mod taint;
pub mod testing;
pub mod wire;
pub mod workflow;

pub use app::{Said, Verb, main, main_with};
pub use taint::__private;

/// Everything a workflow author needs.
pub mod prelude {
    pub use crate::agent::{
        AgentSpec, Classifier, Fixer, Harness, Pass, Quarantined, Template, Worker,
    };
    pub use crate::config::Config;
    pub use crate::cooled::{Cooled, Published};
    pub use crate::cx::{Attempts, Cx, Halt, StepError};
    pub use crate::effect::{AttestState, Effect, EffectKind, Forge, Mode, Push, Sweep, Tracker};
    pub use crate::patch::{Change, Checked, DEFAULT_POLICY, Patch, PatchPolicy, Rejection};
    pub use crate::providers::Providers;
    pub use crate::taint::{Clean, Finite, Invalid, Observed, Sha, Slug, Title, Untrusted};
    pub use crate::wire::{Event, FromRest, Malformed, Payload};
    pub use crate::workflow::{Registered, Scanned, Scanner, Subject, Workflow};
    pub use crate::{clean, observed, output, title};
    pub use almanac_macros::{prompt, workflow};
    pub use futures::future::BoxFuture;
}
