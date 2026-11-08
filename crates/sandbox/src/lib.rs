//! Where hostile code runs. The interface is remote-shaped: nothing here names a host path, so
//! a hosted provider fits as well as Podman. See `docs/adrs/0001-agent-sandbox.md`.
mod fake;
mod podman;

pub use fake::FakeSandbox;
pub use podman::PodmanSandbox;

use futures::future::BoxFuture;
use std::time::Duration;

/// Where a sandbox's files live. Besides a private scratch `/tmp`, it is the only place a
/// sandboxed process can write.
pub const WORKDIR: &str = "/work";

#[derive(Clone, Debug, PartialEq)]
pub struct SandboxError(pub String);

impl std::fmt::Display for SandboxError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "sandbox: {}", self.0)
    }
}

impl std::error::Error for SandboxError {}

pub type Sandboxed<T> = Result<T, SandboxError>;

#[derive(Clone, Debug, PartialEq)]
pub enum Egress {
    None,
    /// Only for `Purpose::Fetch`. Podman cannot filter by domain, so there this means the
    /// default network, which is safe only because a fetch sandbox holds nothing and runs no
    /// agent.
    Domains(Vec<String>),
}

/// Why a sandbox exists, which decides what it may be given.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Purpose {
    /// Runs commands chosen by an agent. Never has network access.
    Agent,
    /// Fetches dependencies with no agent present and nothing worth stealing.
    Fetch,
}

#[derive(Clone, Debug, PartialEq)]
pub struct SandboxSpec {
    pub image: String,
    pub purpose: Purpose,
    pub egress: Egress,
    pub memory_mb: u32,
    pub cpus: u32,
    pub pids: u32,
    pub work_mb: u32,
    pub env: Vec<(String, String)>,
}

impl SandboxSpec {
    pub fn agent(image: impl Into<String>) -> Self {
        Self {
            image: image.into(),
            purpose: Purpose::Agent,
            egress: Egress::None,
            memory_mb: 4096,
            cpus: 2,
            pids: 512,
            work_mb: 4096,
            env: vec![],
        }
    }

    pub fn fetch(image: impl Into<String>, domains: Vec<String>) -> Self {
        Self {
            purpose: Purpose::Fetch,
            egress: Egress::Domains(domains),
            ..Self::agent(image)
        }
    }

    /// An agent sandbox with any egress is a mistake, whatever the provider.
    pub fn validate(&self) -> Sandboxed<()> {
        if self.purpose == Purpose::Agent && self.egress != Egress::None {
            return Err(SandboxError(
                "an agent sandbox must have no network access".to_string(),
            ));
        }
        Ok(())
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct SandboxId(pub String);

#[derive(Clone, Debug, PartialEq)]
pub struct ExecOutput {
    pub exit_code: i32,
    pub stdout: String,
    pub stderr: String,
    pub timed_out: bool,
}

pub trait Sandbox: Send + Sync {
    fn create<'a>(&'a self, spec: &'a SandboxSpec) -> BoxFuture<'a, Sandboxed<SandboxId>>;

    /// Runs `command` through a shell in the working directory.
    fn exec<'a>(
        &'a self,
        id: &'a SandboxId,
        command: &'a str,
        timeout: Duration,
    ) -> BoxFuture<'a, Sandboxed<ExecOutput>>;

    /// Unpacks a tar archive into the working directory.
    fn upload<'a>(&'a self, id: &'a SandboxId, archive: Vec<u8>) -> BoxFuture<'a, Sandboxed<()>>;

    /// A tar archive of `path`, relative to the working directory.
    fn download<'a>(
        &'a self,
        id: &'a SandboxId,
        path: &'a str,
    ) -> BoxFuture<'a, Sandboxed<Vec<u8>>>;

    fn destroy<'a>(&'a self, id: &'a SandboxId) -> BoxFuture<'a, Sandboxed<()>>;

    /// Ends a sandbox without waiting, for an owner being dropped that cannot await `destroy`.
    fn abandon(&self, id: &SandboxId);
}
