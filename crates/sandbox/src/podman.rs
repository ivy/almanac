//! Rootless Podman. Files move with `podman cp`, never a bind mount, so this stays
//! interchangeable with a remote provider.
use crate::{
    Egress, ExecOutput, Purpose, Sandbox, SandboxError, SandboxId, SandboxSpec, Sandboxed, WORKDIR,
};
use futures::future::BoxFuture;
use std::process::Stdio;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;
use tokio::io::AsyncWriteExt;
use tokio::process::Command;

/// How long past its limit a command gets before the client gives up on it too.
const EXEC_GRACE: Duration = Duration::from_secs(5);
/// Exit code when `timeout -s KILL` kills a command.
const KILLED: i32 = 137;
const UNPRIVILEGED: &str = "1000:1000";
/// Longer than any session. A sandbox whose owner was killed outright ends itself by this.
const LIFETIME_SECONDS: u32 = 7200;
/// How long `abandon` holds its thread. A stop with no grace period takes about a second.
const ABANDON_LIMIT: Duration = Duration::from_secs(10);
const SCRATCH_MB: u32 = 1024;

static NEXT: AtomicU64 = AtomicU64::new(0);

#[derive(Default)]
pub struct PodmanSandbox;

fn failed(what: &str, stderr: &[u8]) -> SandboxError {
    let reason = String::from_utf8_lossy(stderr);
    SandboxError(format!("{what}: {}", reason.trim()))
}

async fn podman(args: &[String]) -> Sandboxed<std::process::Output> {
    Command::new("podman")
        .args(args)
        .kill_on_drop(true)
        .output()
        .await
        .map_err(|e| SandboxError(format!("cannot run podman: {e}")))
}

fn create_args(name: &str, spec: &SandboxSpec) -> Vec<String> {
    let mut args: Vec<String> = [
        "run",
        "--detach",
        "--rm",
        "--name",
        name,
        "--cap-drop=all",
        "--security-opt=no-new-privileges",
        "--read-only",
        "--read-only-tmpfs=false",
        "--user",
        UNPRIVILEGED,
        "--workdir",
        WORKDIR,
    ]
    .map(String::from)
    .into();
    args.push(format!("--memory={}m", spec.memory_mb));
    args.push(format!("--cpus={}", spec.cpus));
    args.push(format!("--pids-limit={}", spec.pids));
    args.push("--tmpfs".to_string());
    args.push(format!(
        "{WORKDIR}:rw,exec,size={}m,mode=0755,U",
        spec.work_mb
    ));
    // Some tools insist on /tmp whatever TMPDIR says. This one is private and in memory, and it
    // goes when the sandbox does.
    args.push("--tmpfs".to_string());
    args.push(format!("/tmp:rw,size={SCRATCH_MB}m,mode=1777"));
    if spec.egress == Egress::None {
        args.push("--network=none".to_string());
    }
    // Home is scratch space: tools write state there, and the checkout must hold only the patch.
    let tmp = format!("TMPDIR={WORKDIR}/.tmp");
    let home = format!("HOME={WORKDIR}/.tmp");
    for pair in spec
        .env
        .iter()
        .map(|(k, v)| format!("{k}={v}"))
        .chain([tmp, home])
    {
        args.push("--env".to_string());
        args.push(pair);
    }
    args.push(spec.image.clone());
    args.extend(["sleep".to_string(), LIFETIME_SECONDS.to_string()]);
    args
}

impl Sandbox for PodmanSandbox {
    fn create<'a>(&'a self, spec: &'a SandboxSpec) -> BoxFuture<'a, Sandboxed<SandboxId>> {
        Box::pin(async move {
            spec.validate()?;
            if spec.purpose == Purpose::Agent && spec.egress != Egress::None {
                return Err(SandboxError("agent sandboxes have no egress".to_string()));
            }
            let n = NEXT.fetch_add(1, Ordering::Relaxed);
            let name = format!("almanac-{}-{n}", std::process::id());
            let out = podman(&create_args(&name, spec)).await?;
            if !out.status.success() {
                return Err(failed("create", &out.stderr));
            }
            let id = SandboxId(name);
            let tmp = format!("mkdir -p {WORKDIR}/.tmp");
            self.exec(&id, &tmp, Duration::from_secs(30)).await?;
            Ok(id)
        })
    }

    fn exec<'a>(
        &'a self,
        id: &'a SandboxId,
        command: &'a str,
        timeout: Duration,
    ) -> BoxFuture<'a, Sandboxed<ExecOutput>> {
        Box::pin(async move {
            let seconds = timeout.as_secs().max(1).to_string();
            let args: Vec<String> = [
                "exec", &id.0, "timeout", "-s", "KILL", &seconds, "sh", "-c", command,
            ]
            .map(String::from)
            .into();
            let run = podman(&args);
            let Ok(out) = tokio::time::timeout(timeout + EXEC_GRACE, run).await else {
                return Ok(ExecOutput {
                    exit_code: KILLED,
                    stdout: String::new(),
                    stderr: "killed: over the time limit".to_string(),
                    timed_out: true,
                });
            };
            let out = out?;
            let exit_code = out.status.code().unwrap_or(KILLED);
            Ok(ExecOutput {
                exit_code,
                stdout: String::from_utf8_lossy(&out.stdout).into_owned(),
                stderr: String::from_utf8_lossy(&out.stderr).into_owned(),
                timed_out: exit_code == KILLED,
            })
        })
    }

    fn upload<'a>(&'a self, id: &'a SandboxId, archive: Vec<u8>) -> BoxFuture<'a, Sandboxed<()>> {
        Box::pin(async move {
            let mut child = Command::new("podman")
                .args(["cp", "-", &format!("{}:{WORKDIR}", id.0)])
                .stdin(Stdio::piped())
                .stderr(Stdio::piped())
                .kill_on_drop(true)
                .spawn()
                .map_err(|e| SandboxError(format!("cannot run podman: {e}")))?;
            let mut stdin = child.stdin.take().expect("stdin was piped");
            let write = stdin.write_all(&archive).await;
            drop(stdin);
            let out = child
                .wait_with_output()
                .await
                .map_err(|e| SandboxError(e.to_string()))?;
            write.map_err(|e| SandboxError(format!("upload: {e}")))?;
            if !out.status.success() {
                return Err(failed("upload", &out.stderr));
            }
            Ok(())
        })
    }

    fn download<'a>(
        &'a self,
        id: &'a SandboxId,
        path: &'a str,
    ) -> BoxFuture<'a, Sandboxed<Vec<u8>>> {
        Box::pin(async move {
            let source = format!("{}:{WORKDIR}/{path}", id.0);
            let args = ["cp".to_string(), source, "-".to_string()];
            let out = podman(&args).await?;
            if !out.status.success() {
                return Err(failed("download", &out.stderr));
            }
            Ok(out.stdout)
        })
    }

    fn destroy<'a>(&'a self, id: &'a SandboxId) -> BoxFuture<'a, Sandboxed<()>> {
        Box::pin(async move {
            let args = ["stop", "--time", "0", &id.0].map(String::from);
            let out = podman(&args).await?;
            if !out.status.success() {
                return Err(failed("destroy", &out.stderr));
            }
            Ok(())
        })
    }

    /// The same stop as `destroy`, waited for on this thread. The caller is being dropped, usually
    /// because the process is shutting down, and a stop left running when it exits is killed
    /// with it. The container was started with `--rm`, so stopping removes it.
    fn abandon(&self, id: &SandboxId) {
        let stopping = std::process::Command::new("podman")
            .args(["stop", "--time", "0", &id.0])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn();
        let mut child = match stopping {
            Ok(child) => child,
            Err(error) => {
                tracing::warn!(sandbox = %id.0, %error, "could not abandon a sandbox");
                return;
            }
        };
        let deadline = std::time::Instant::now() + ABANDON_LIMIT;
        while matches!(child.try_wait(), Ok(None)) {
            if std::time::Instant::now() >= deadline {
                tracing::warn!(sandbox = %id.0, "a sandbox was still stopping when abandoned");
                return;
            }
            std::thread::sleep(Duration::from_millis(50));
        }
    }
}
