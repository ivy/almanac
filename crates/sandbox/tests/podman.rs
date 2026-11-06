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
async fn a_session_can_write_only_inside_its_checkout() {
    let p = PodmanSandbox;
    let id = started(&p).await;
    assert_eq!(run(&p, &id, "echo x > /work/file").await.exit_code, 0);
    for outside in ["/tmp/x", "/etc/x", "/x", "/usr/x", "/var/tmp/x", "/root/x"] {
        let out = run(&p, &id, &format!("echo x > {outside}")).await;
        assert_ne!(out.exit_code, 0, "{outside} was writable");
    }
    p.destroy(&id).await.unwrap();
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

#[tokio::test]
async fn an_agent_sandbox_cannot_be_given_egress() {
    let mut spec = SandboxSpec::agent(IMAGE);
    spec.egress = sandbox::Egress::Domains(vec!["example.com".to_string()]);
    let refused = PodmanSandbox.create(&spec).await.unwrap_err();
    assert!(refused.0.contains("no network access"), "{refused}");
}
