use almanac::prelude::*;
use registry::{Ecosystem, Endpoints, HttpRegistry, Package, PackageRegistry};
use time::macros::datetime;
use wiremock::matchers::path;
use wiremock::{Mock, MockServer, ResponseTemplate};

fn fixture(name: &str) -> String {
    let dir = concat!(env!("CARGO_MANIFEST_DIR"), "/fixtures");
    std::fs::read_to_string(format!("{dir}/{name}")).unwrap()
}

async fn serving(at: &str, name: &str) -> (MockServer, HttpRegistry) {
    let server = MockServer::start().await;
    let body = ResponseTemplate::new(200).set_body_raw(fixture(name), "application/json");
    Mock::given(path(at))
        .respond_with(body)
        .mount(&server)
        .await;
    let u = server.uri();
    let endpoints = Endpoints {
        npm: u.clone(),
        crates: u.clone(),
        pypi: u.clone(),
        go: u.clone(),
        github: u.clone(),
        docker: u,
    };
    (server, HttpRegistry::new(endpoints))
}

fn package(ecosystem: Ecosystem, name: &str, version: &str) -> Package {
    Package::new(ecosystem, name.parse().unwrap(), version.parse().unwrap()).unwrap()
}

async fn published(at: &str, file: &str, p: Package) -> Published {
    let (_server, registry) = serving(at, file).await;
    registry.published(&p).await.unwrap()
}

#[tokio::test]
async fn npm() {
    let p = package(Ecosystem::Npm, "is-odd", "3.0.1");
    let found = published("/is-odd", "npm-is-odd.json", p).await;
    assert_eq!(found, Published::At(datetime!(2018-05-31 20:04:53.306 UTC)));
}

#[tokio::test]
async fn an_npm_version_the_registry_has_no_time_for_is_unknown() {
    let p = package(Ecosystem::Npm, "is-odd", "9.9.9");
    assert_eq!(
        published("/is-odd", "npm-is-odd.json", p).await,
        Published::Unknown
    );
}

#[tokio::test]
async fn crates_io() {
    let p = package(Ecosystem::Crates, "itoa", "1.0.11");
    let found = published("/api/v1/crates/itoa/1.0.11", "crates-itoa.json", p).await;
    assert!(
        matches!(found, Published::At(at) if at.year() == 2024),
        "{found:?}"
    );
}

#[tokio::test]
async fn pypi_uses_the_newest_file() {
    let p = package(Ecosystem::PyPi, "six", "1.16.0");
    let found = published("/pypi/six/1.16.0/json", "pypi-six.json", p).await;
    assert!(
        matches!(found, Published::At(at) if at.year() == 2021),
        "{found:?}"
    );
}

#[tokio::test]
async fn go_modules_escape_capitals() {
    let p = package(Ecosystem::Go, "github.com/Azure/x", "v0.14.0");
    let at = "/github.com/!azure/x/@v/v0.14.0.info";
    let found = published(at, "go-text.json", p).await;
    assert_eq!(found, Published::At(datetime!(2023-10-11 21:58:48 UTC)));
}

#[tokio::test]
async fn github_releases() {
    let p = package(Ecosystem::GithubRelease, "actions/checkout", "v4.1.1");
    let at = "/repos/actions/checkout/releases/tags/v4.1.1";
    let found = published(at, "github-checkout.json", p).await;
    assert!(
        matches!(found, Published::At(at) if at.year() == 2023),
        "{found:?}"
    );
}

#[tokio::test]
async fn docker_hub_official_images() {
    let p = package(Ecosystem::Docker, "alpine", "3.19");
    let at = "/v2/repositories/library/alpine/tags/3.19";
    let found = published(at, "docker-alpine.json", p).await;
    assert!(
        matches!(found, Published::At(at) if at.year() == 2025),
        "{found:?}"
    );
}

#[tokio::test]
async fn a_registry_with_no_timestamps_is_unknown_with_no_request() {
    let p = package(Ecosystem::Unsupported, "ghcr.io/owner/image", "1");
    let (server, registry) = serving("/never", "go-text.json").await;
    assert_eq!(registry.published(&p).await.unwrap(), Published::Unknown);
    assert!(server.received_requests().await.unwrap().is_empty());
}

#[tokio::test]
async fn a_release_the_registry_does_not_know_is_unknown() {
    let p = package(Ecosystem::Crates, "missing", "1.0.0");
    assert_eq!(
        published("/elsewhere", "crates-itoa.json", p).await,
        Published::Unknown
    );
}

#[tokio::test]
async fn an_outage_is_an_error_and_not_an_answer() {
    let server = MockServer::start().await;
    Mock::given(path("/is-odd"))
        .respond_with(ResponseTemplate::new(503))
        .mount(&server)
        .await;
    let endpoints = Endpoints {
        npm: server.uri(),
        ..Endpoints::default()
    };
    let p = package(Ecosystem::Npm, "is-odd", "3.0.1");
    assert!(HttpRegistry::new(endpoints).published(&p).await.is_err());
}

#[test]
fn a_name_cannot_walk_out_of_a_registry_path() {
    let bad = |name: &str| {
        Package::new(Ecosystem::Npm, name.parse().unwrap(), "1".parse().unwrap()).is_err()
    };
    assert!(bad("../../admin"));
    assert!(bad("a/../b"));
    assert!(bad("./a"));
}
