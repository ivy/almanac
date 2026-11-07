use advisory::{
    Advisories, Advisory, CveNow, FakeAdvisories, Severity, Validation, compare, validate,
};
use almanac::prelude::*;
use almanac::testing::generators::versions;
use hegel::TestCase;
use std::cmp::Ordering;
use wiremock::matchers::{path, query_param};
use wiremock::{Mock, MockServer, ResponseTemplate};

const SDK: &str = "@modelcontextprotocol/sdk";
const FIXTURE: &str = include_str!("../fixtures/cve-now-entry.json");

async fn feed() -> (MockServer, CveNow) {
    let server = MockServer::start().await;
    let body = ResponseTemplate::new(200).set_body_raw(FIXTURE, "application/json");
    Mock::given(path("/api/v1/entries/GHSA-6QXP-VCCF-F47H"))
        .respond_with(body.clone())
        .mount(&server)
        .await;
    Mock::given(path("/api/v1/entries/CVE-2026-104850"))
        .respond_with(body)
        .mount(&server)
        .await;
    let found = serde_json::json!({"entries": [{"id": "CVE-2026-104850"}]});
    Mock::given(path("/api/v1/search"))
        .and(query_param("package", SDK))
        .respond_with(ResponseTemplate::new(200).set_body_json(found))
        .mount(&server)
        .await;
    let client = CveNow::new(server.uri());
    (server, client)
}

async fn real() -> Advisory {
    let (_server, feed) = feed().await;
    let id: Slug = "GHSA-6qxp-vccf-f47h".parse().unwrap();
    feed.by_id(&id).await.unwrap().unwrap()
}

#[tokio::test]
async fn a_real_entry_reads_as_clean_facts_with_its_text_tainted() {
    let a = real().await;
    assert_eq!(a.id.to_string(), "CVE-2026-104850");
    assert_eq!(a.aliases[0].to_string(), "GHSA-6QXP-VCCF-F47H");
    assert_eq!(a.severity, Severity::High);
    assert!(!a.kev);
    assert_eq!(a.affected.len(), 2, "a vendor-only entry is left out");
    assert_eq!(format!("{:?}", a.summary), "Untrusted(..)");
}

#[tokio::test]
async fn a_lookup_by_package_finds_the_same_advisory() {
    let (_server, feed) = feed().await;
    let found = feed
        .by_package(&"npm".parse().unwrap(), &SDK.parse().unwrap())
        .await
        .unwrap();
    assert_eq!(found.len(), 1);
}

#[tokio::test]
async fn an_unknown_advisory_is_none() {
    let (_server, feed) = feed().await;
    let id: Slug = "CVE-2099-0001".parse().unwrap();
    assert!(feed.by_id(&id).await.unwrap().is_none());
    let fake = FakeAdvisories::default();
    assert!(fake.by_id(&id).await.unwrap().is_none());
}

#[tokio::test]
async fn an_outage_is_an_error_and_not_a_clean_bill_of_health() {
    let server = MockServer::start().await;
    Mock::given(path("/api/v1/entries/CVE-2099-0001"))
        .respond_with(ResponseTemplate::new(503))
        .mount(&server)
        .await;
    let feed = CveNow::new(server.uri());
    assert!(feed.by_id(&"CVE-2099-0001".parse().unwrap()).await.is_err());
}

#[tokio::test]
async fn a_target_equal_to_the_patched_version_validates() {
    let verdict = validate(&real().await, "npm", SDK, "1.20.0", "1.31.0");
    assert_eq!(verdict, Validation::Valid);
}

#[tokio::test]
async fn a_target_below_the_patched_version_fails() {
    let verdict = validate(&real().await, "npm", SDK, "1.20.0", "1.30.9");
    assert_eq!(verdict, Validation::TargetStillAffected);
}

#[tokio::test]
async fn a_target_past_the_patched_version_is_not_the_fix() {
    let verdict = validate(&real().await, "npm", SDK, "1.20.0", "1.32.0");
    assert_eq!(verdict, Validation::NotThePatchedVersion);
}

#[tokio::test]
async fn a_current_version_outside_the_range_is_not_affected() {
    let a = real().await;
    assert_eq!(
        validate(&a, "npm", SDK, "1.11.0", "1.31.0"),
        Validation::NotAffected
    );
    assert_eq!(
        validate(&a, "npm", SDK, "1.31.0", "1.31.1"),
        Validation::NotAffected
    );
}

#[tokio::test]
async fn another_package_is_not_listed() {
    let verdict = validate(&real().await, "npm", "left-pad", "1.20.0", "1.31.0");
    assert_eq!(verdict, Validation::PackageNotListed);
}

#[tokio::test]
async fn versions_that_cannot_be_compared_prove_nothing() {
    let verdict = validate(&real().await, "npm", SDK, "not-a-version", "1.31.0");
    assert_eq!(verdict, Validation::Incomparable);
}

#[test]
fn semver_pre_releases_sort_before_their_release() {
    assert_eq!(
        compare("npm", "1.31.0-rc.1", "1.31.0"),
        Some(Ordering::Less)
    );
}

#[test]
fn go_versions_keep_their_prefix() {
    assert_eq!(compare("Go", "v1.2.3", "v1.10.0"), Some(Ordering::Less));
}

#[test]
fn shortened_versions_are_padded() {
    assert_eq!(compare("npm", "1.2", "1.2.0"), Some(Ordering::Equal));
}

#[test]
fn pypi_compares_plain_releases_and_refuses_the_rest() {
    assert_eq!(compare("PyPI", "1.26", "1.26.0"), Some(Ordering::Equal));
    assert_eq!(compare("PyPI", "1.9", "1.10"), Some(Ordering::Less));
    assert_eq!(compare("PyPI", "1.0rc1", "1.0"), None);
}

#[test]
fn an_ecosystem_without_rules_only_knows_equality() {
    assert_eq!(compare("Maven", "1.0", "1.0"), Some(Ordering::Equal));
    assert_eq!(compare("Maven", "1.0", "2.0"), None);
}

fn text((a, b, c): (u64, u64, u64)) -> String {
    format!("{a}.{b}.{c}")
}

#[hegel::test]
fn semver_order_is_numeric_and_not_textual(tc: TestCase) {
    let x = tc.draw(versions());
    let y = tc.draw(versions());
    assert_eq!(compare("npm", &text(x), &text(y)), Some(x.cmp(&y)));
}

#[hegel::test]
fn order_reverses_when_the_versions_swap(tc: TestCase) {
    let x = tc.draw(versions());
    let y = tc.draw(versions());
    let forward = compare("crates.io", &text(x), &text(y)).unwrap();
    let backward = compare("crates.io", &text(y), &text(x)).unwrap();
    assert_eq!(forward, backward.reverse());
}

#[hegel::test]
fn a_v_prefix_never_changes_the_order(tc: TestCase) {
    let x = tc.draw(versions());
    let y = tc.draw(versions());
    let plain = compare("go", &text(x), &text(y));
    let prefixed = compare("go", &format!("v{}", text(x)), &format!("v{}", text(y)));
    assert_eq!(plain, prefixed);
}
