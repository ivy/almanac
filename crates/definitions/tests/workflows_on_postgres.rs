//! Runs the whole `workflows` suite against PostgreSQL. Needs podman. `make test-db` runs this.
use almanac::store::PostgresStore;
use std::process::Command;
use testcontainers_modules::postgres::Postgres;
use testcontainers_modules::testcontainers::runners::AsyncRunner;

#[tokio::test]
async fn every_workflow_test_passes_on_postgres() {
    let container = Postgres::default().start().await.unwrap();
    let port = container.get_host_port_ipv4(5432).await.unwrap();
    let url = format!("postgres://postgres:postgres@127.0.0.1:{port}/postgres");
    PostgresStore::connect(&url, "migrate").await.unwrap();
    let status = Command::new(env!("CARGO"))
        .args([
            "test",
            "--locked",
            "-p",
            "definitions",
            "--test",
            "workflows",
        ])
        .env("ALMANAC_TEST_DATABASE_URL", &url)
        .status()
        .unwrap();
    assert!(status.success());
}
