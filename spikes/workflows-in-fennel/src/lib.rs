//! A spike: Almanac workflows written in Fennel, hosted by Rust.
//!
//! Rust stays the trusted host. `compile` reads and compiles a workflow once. `gather` runs its
//! I/O half through `Cx` in a VM of its own, and `pure` runs its policy in another, so the two share
//! nothing but the facts. `value` is the boundary both cross.
#![forbid(unsafe_code)]

pub mod compile;
pub mod gather;
pub mod github;
pub mod host;
pub mod manifest;
pub mod pure;
pub mod sandbox;
pub mod value;
pub mod workflow;

/// The ported workflow, built into the binary as a Rust workflow is.
pub const BUILD_FAILURES: &str = include_str!("../workflows/build_failures.fnl");

pub use compile::LoadError;
pub use host::Host;
pub use workflow::FennelWorkflow;

/// Loads the ported workflow.
pub fn build_failures() -> Result<FennelWorkflow, LoadError> {
    FennelWorkflow::load(&Host::renovate(), "build_failures.fnl", BUILD_FAILURES)
}
