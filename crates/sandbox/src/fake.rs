//! A sandbox that keeps nothing on the host, proving no caller depends on a host path.
use crate::{ExecOutput, Sandbox, SandboxError, SandboxId, SandboxSpec, Sandboxed};
use futures::future::BoxFuture;
use std::collections::HashMap;
use std::sync::Mutex;
use std::time::Duration;

type Reply = Box<dyn Fn(&str) -> ExecOutput + Send + Sync>;

/// Records calls, and answers `exec` from a function.
pub struct FakeSandbox {
    reply: Reply,
    state: Mutex<State>,
}

#[derive(Default)]
struct State {
    next: u64,
    alive: HashMap<String, Vec<Vec<u8>>>,
    commands: Vec<String>,
    specs: Vec<SandboxSpec>,
}

impl FakeSandbox {
    pub fn answering(reply: impl Fn(&str) -> ExecOutput + Send + Sync + 'static) -> Self {
        Self {
            reply: Box::new(reply),
            state: Mutex::default(),
        }
    }

    pub fn commands(&self) -> Vec<String> {
        self.state.lock().unwrap().commands.clone()
    }

    pub fn specs(&self) -> Vec<SandboxSpec> {
        self.state.lock().unwrap().specs.clone()
    }

    pub fn alive(&self) -> usize {
        self.state.lock().unwrap().alive.len()
    }
}

impl Default for FakeSandbox {
    fn default() -> Self {
        Self::answering(|_| ExecOutput {
            exit_code: 0,
            stdout: String::new(),
            stderr: String::new(),
            timed_out: false,
        })
    }
}

fn unknown(id: &SandboxId) -> SandboxError {
    SandboxError(format!("no sandbox `{}`", id.0))
}

impl Sandbox for FakeSandbox {
    fn create<'a>(&'a self, spec: &'a SandboxSpec) -> BoxFuture<'a, Sandboxed<SandboxId>> {
        Box::pin(async move {
            spec.validate()?;
            let mut s = self.state.lock().unwrap();
            s.next += 1;
            let id = format!("fake-{}", s.next);
            s.alive.insert(id.clone(), vec![]);
            s.specs.push(spec.clone());
            Ok(SandboxId(id))
        })
    }

    fn exec<'a>(
        &'a self,
        id: &'a SandboxId,
        command: &'a str,
        _timeout: Duration,
    ) -> BoxFuture<'a, Sandboxed<ExecOutput>> {
        Box::pin(async move {
            let mut s = self.state.lock().unwrap();
            if !s.alive.contains_key(&id.0) {
                return Err(unknown(id));
            }
            s.commands.push(command.to_string());
            Ok((self.reply)(command))
        })
    }

    fn upload<'a>(&'a self, id: &'a SandboxId, archive: Vec<u8>) -> BoxFuture<'a, Sandboxed<()>> {
        Box::pin(async move {
            let mut s = self.state.lock().unwrap();
            s.alive
                .get_mut(&id.0)
                .ok_or_else(|| unknown(id))?
                .push(archive);
            Ok(())
        })
    }

    fn download<'a>(
        &'a self,
        id: &'a SandboxId,
        _path: &'a str,
    ) -> BoxFuture<'a, Sandboxed<Vec<u8>>> {
        Box::pin(async move {
            let s = self.state.lock().unwrap();
            let uploads = s.alive.get(&id.0).ok_or_else(|| unknown(id))?;
            Ok(uploads.last().cloned().unwrap_or_default())
        })
    }

    fn destroy<'a>(&'a self, id: &'a SandboxId) -> BoxFuture<'a, Sandboxed<()>> {
        Box::pin(async move {
            self.state.lock().unwrap().alive.remove(&id.0);
            Ok(())
        })
    }
}
