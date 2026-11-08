//! Needs podman. `make test-sandbox` runs this.
use sandbox::{ExecOutput, PodmanSandbox, Sandbox, SandboxId, SandboxSpec};
use std::time::Duration;

const IMAGE: &str = "localhost/almanac-fixer:test";
const LIMIT: Duration = Duration::from_secs(20);

async fn started(p: &PodmanSandbox) -> SandboxId {
    p.create(&SandboxSpec::agent(IMAGE)).await.unwrap()
}

async fn run(p: &PodmanSandbox, id: &SandboxId, command: &str) -> ExecOutput {
    p.exec(id, command, LIMIT).await.unwrap()
}

#[tokio::test]
async fn a_session_can_write_only_inside_its_checkout_and_private_scratch() {
    let p = PodmanSandbox;
    let id = started(&p).await;
    assert_eq!(run(&p, &id, "echo x > /work/file").await.exit_code, 0);
    assert_eq!(run(&p, &id, "echo x > /tmp/scratch").await.exit_code, 0);
    for outside in ["/etc/x", "/x", "/usr/x", "/var/tmp/x", "/root/x"] {
        let out = run(&p, &id, &format!("echo x > {outside}")).await;
        assert_ne!(out.exit_code, 0, "{outside} was writable");
    }
    p.destroy(&id).await.unwrap();
}

#[tokio::test]
async fn scratch_space_is_not_shared_between_sandboxes() {
    let p = PodmanSandbox;
    let first = started(&p).await;
    run(&p, &first, "echo secret > /tmp/left-behind").await;
    let second = started(&p).await;
    let out = run(&p, &second, "cat /tmp/left-behind").await;
    assert_ne!(out.exit_code, 0, "{out:?}");
    p.destroy(&first).await.unwrap();
    p.destroy(&second).await.unwrap();
}

#[tokio::test]
async fn a_session_has_no_network_and_no_credentials() {
    let p = PodmanSandbox;
    let id = started(&p).await;
    let out = run(&p, &id, "wget -q -T 3 -O- http://1.1.1.1/").await;
    assert_ne!(out.exit_code, 0, "{out:?}");
    assert!(out.stderr.contains("unreachable"), "{out:?}");
    let env = run(&p, &id, "env").await.stdout.to_lowercase();
    assert!(
        !env.contains("anthropic") && !env.contains("token"),
        "{env}"
    );
    p.destroy(&id).await.unwrap();
}

#[tokio::test]
async fn a_session_runs_with_no_privilege() {
    let p = PodmanSandbox;
    let id = started(&p).await;
    let caps = run(&p, &id, "grep CapEff /proc/self/status").await.stdout;
    assert!(caps.contains("0000000000000000"), "{caps}");
    assert_eq!(run(&p, &id, "id -u").await.stdout.trim(), "1000");
    p.destroy(&id).await.unwrap();
}

#[tokio::test]
async fn a_command_over_its_limit_is_killed() {
    let p = PodmanSandbox;
    let id = started(&p).await;
    let out = p
        .exec(&id, "sleep 30", Duration::from_secs(1))
        .await
        .unwrap();
    assert!(out.timed_out, "{out:?}");
    p.destroy(&id).await.unwrap();
}

#[tokio::test]
async fn files_move_only_as_archives_through_the_api() {
    let p = PodmanSandbox;
    let id = started(&p).await;
    run(
        &p,
        &id,
        "mkdir src && echo hello > src/a.txt && tar -cf /work/t.tar src",
    )
    .await;
    let archive = p.download(&id, "t.tar").await.unwrap();
    p.destroy(&id).await.unwrap();

    let second = started(&p).await;
    // `download` wraps the file in a tar of its own, so the inner archive is uploaded as a file.
    p.upload(&second, archive).await.unwrap();
    let out = run(&p, &second, "tar -xf t.tar && cat src/a.txt").await;
    assert_eq!(out.stdout.trim(), "hello", "{out:?}");
    p.destroy(&second).await.unwrap();
}

#[tokio::test]
async fn a_destroyed_sandbox_is_gone() {
    let p = PodmanSandbox;
    let id = started(&p).await;
    p.destroy(&id).await.unwrap();
    assert!(
        p.exec(&id, "true", LIMIT)
            .await
            .is_ok_and(|o| o.exit_code != 0)
            || p.exec(&id, "true", LIMIT).await.is_err()
    );
}

const ABANDONING: &str = "ALMANAC_TEST_ABANDON";

/// The process half of the test below: abandons the named sandbox and exits at once.
#[test]
fn abandon_then_exit() {
    let Ok(name) = std::env::var(ABANDONING) else {
        return;
    };
    PodmanSandbox.abandon(&SandboxId(name));
    std::process::exit(0);
}

/// systemd kills what is left in a unit's cgroup once its main process exits, so a stop that
/// outlives `abandon` never happens. This runs the abandoning process in such a unit.
#[tokio::test]
async fn a_sandbox_abandoned_by_a_stopping_service_is_gone() {
    let p = PodmanSandbox;
    let id = started(&p).await;
    let service = std::process::Command::new("systemd-run")
        .args(["--user", "--wait", "--collect", "--quiet"])
        .args(["--property", "KillMode=control-group"])
        .arg(format!("--setenv={ABANDONING}={}", id.0))
        .arg(std::env::current_exe().unwrap())
        .args(["--exact", "abandon_then_exit", "--quiet"])
        .output()
        .unwrap();
    assert!(service.status.success(), "{service:?}");
    let left = std::process::Command::new("podman")
        .args(["container", "exists", &id.0])
        .status()
        .unwrap();
    if left.success() {
        p.destroy(&id).await.unwrap();
        panic!("{} outlived the service that abandoned it", id.0);
    }
}

#[tokio::test]
async fn an_agent_sandbox_cannot_be_given_egress() {
    let mut spec = SandboxSpec::agent(IMAGE);
    spec.egress = sandbox::Egress::Domains(vec!["example.com".to_string()]);
    let refused = PodmanSandbox.create(&spec).await.unwrap_err();
    assert!(refused.0.contains("no network access"), "{refused}");
}
