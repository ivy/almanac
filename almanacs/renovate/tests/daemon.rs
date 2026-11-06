//! The binary as a service: readiness, SIGTERM, and a clean exit. `make test-db` runs this.
use std::os::unix::net::UnixDatagram;
use std::process::Stdio;
use std::time::Duration;
use testcontainers_modules::postgres::Postgres;
use testcontainers_modules::testcontainers::runners::AsyncRunner;
use tokio::process::Command;

async fn healthy(url: &str) -> bool {
    for _ in 0..500 {
        let answered = reqwest::get(url).await;
        if answered.is_ok_and(|r| r.status() == 200) {
            return true;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    false
}

fn free_port() -> u16 {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    listener.local_addr().unwrap().port()
}

#[tokio::test(flavor = "multi_thread")]
async fn the_daemon_reports_ready_answers_health_and_stops_on_sigterm() {
    let container = Postgres::default().start().await.unwrap();
    let port = container.get_host_port_ipv4(5432).await.unwrap();
    let url = format!("postgres://postgres:postgres@127.0.0.1:{port}/postgres");
    let dir = tempfile::tempdir().unwrap();
    let socket_path = dir.path().join("notify");
    let socket = UnixDatagram::bind(&socket_path).unwrap();
    socket
        .set_read_timeout(Some(Duration::from_secs(20)))
        .unwrap();
    let health_port = free_port();

    let mut daemon = Command::new(env!("CARGO_BIN_EXE_renovate-triage"))
        .arg("run")
        .env_clear()
        .env("DATABASE_URL", &url)
        .env("NOTIFY_SOCKET", &socket_path)
        .env("ALMANAC_LISTEN", format!("127.0.0.1:{health_port}"))
        .env("ALMANAC_CONFIG", dir.path().join("none.toml"))
        .stderr(Stdio::piped())
        .kill_on_drop(true)
        .spawn()
        .unwrap();

    let mut message = [0u8; 64];
    let received = tokio::task::block_in_place(|| socket.recv(&mut message)).unwrap();
    assert!(String::from_utf8_lossy(&message[..received]).contains("READY=1"));
    assert!(healthy(&format!("http://127.0.0.1:{health_port}/healthz")).await);

    let pid = daemon.id().unwrap().to_string();
    Command::new("kill")
        .args(["-TERM", &pid])
        .status()
        .await
        .unwrap();
    let exit = tokio::time::timeout(Duration::from_secs(20), daemon.wait()).await;
    assert!(exit.unwrap().unwrap().success(), "it should exit cleanly");
    let received = tokio::task::block_in_place(|| socket.recv(&mut message)).unwrap();
    assert!(String::from_utf8_lossy(&message[..received]).contains("STOPPING=1"));
}

#[tokio::test]
async fn run_needs_a_database() {
    let dir = tempfile::tempdir().unwrap();
    let out = Command::new(env!("CARGO_BIN_EXE_renovate-triage"))
        .args(["--format", "json", "run"])
        .env_clear()
        .env("ALMANAC_CONFIG", dir.path().join("none.toml"))
        .output()
        .await
        .unwrap();
    assert!(!out.status.success());
    let text = String::from_utf8_lossy(&out.stderr);
    assert!(text.contains("almanac::config::env_missing"), "{text}");
}

#[tokio::test]
async fn startup_fails_with_a_diagnostic_when_a_provider_is_missing() {
    let dir = tempfile::tempdir().unwrap();
    let out = Command::new(env!("CARGO_BIN_EXE_renovate-triage"))
        .args(["--format", "json", "run"])
        .env_clear()
        .env("DATABASE_URL", "postgres://unreachable.invalid/none")
        .env("DEMO_NO_REGISTRY", "1")
        .env("ALMANAC_CONFIG", dir.path().join("none.toml"))
        .output()
        .await
        .unwrap();
    assert!(!out.status.success());
    let text = String::from_utf8_lossy(&out.stderr);
    assert!(text.contains("almanac::provider::missing"), "{text}");
}
