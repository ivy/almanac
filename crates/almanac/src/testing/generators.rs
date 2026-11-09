//! Generators for property tests, shared by every crate's tests so a property names what it
//! draws and not how. Built only with the `hegel` feature, which nothing that ships enables.
use crate::patch::DEFAULT_POLICY;
use crate::runtime::Limits;
use hegel::TestCase;
use hegel::generators::{self as gs, Generator};
use std::time::Duration;

/// Any limits a binary or a config file could set.
#[hegel::composite]
pub fn limits(tc: &TestCase) -> Limits {
    let cap = || gs::optional(gs::integers::<u32>().max_value(9_999));
    Limits {
        max_depth: tc.draw(gs::integers::<u32>().max_value(99)),
        runs_cap: tc.draw(cap()),
        overall_cap: tc.draw(cap()),
        concurrency: tc.draw(gs::integers::<usize>().min_value(1).max_value(63)),
        scan_interval: Duration::from_secs(
            tc.draw(gs::integers::<u64>().min_value(1).max_value(99_999)),
        ),
    }
}

/// `usual` three times in four, and otherwise any of `all`, so a property reaches a rare
/// branch often while every value stays reachable.
#[hegel::composite]
pub fn mostly<T: Clone + std::fmt::Debug + Send + Sync>(tc: &TestCase, usual: T, all: Vec<T>) -> T {
    if tc.draw(gs::weighted_booleans(0.75)) {
        usual
    } else {
        tc.draw(gs::sampled_from(all))
    }
}

/// A release version as `(major, minor, patch)`, over the whole range semver allows.
#[hegel::composite]
pub fn versions(tc: &TestCase) -> (u64, u64, u64) {
    let part = || gs::integers::<u64>();
    (tc.draw(part()), tc.draw(part()), tc.draw(part()))
}

/// A semver pre-release's dot-separated identifiers, each numeric or alphanumeric, and
/// empty for a release.
#[hegel::composite]
pub fn prereleases(tc: &TestCase) -> Vec<String> {
    let numeric = gs::integers::<u64>().map(|n| n.to_string());
    let alphanumeric = gs::from_regex("[0-9A-Za-z-]{0,3}[A-Za-z-][0-9A-Za-z-]{0,3}");
    tc.draw(gs::vecs(gs::one_of(vec![numeric.boxed(), alphanumeric.boxed()])).max_size(4))
}

/// Ways of spelling a path so it reaches a denied file while looking like something else.
#[hegel::composite]
pub fn denied_path_spellings(tc: &TestCase) -> String {
    let path = tc.draw(gs::sampled_from(vec![
        ".github/workflows/ci.yml",
        ".git/hooks/pre-commit",
        "renovate.json",
        "mise.toml",
        ".gitmodules",
    ]));
    let disguise = tc.draw(gs::sampled_from(vec![
        "{}",
        "./{}",
        "src/../{}",
        "a/b/../../{}",
        "../{}",
        "/{}",
        "//{}",
        "src//{}",
        "{}/",
        "{}.",
        "{} ",
        "src\\..\\{}",
        "{}\0",
        "\n{}",
        "C:{}",
        "C:/{}",
    ]));
    let spelled = if tc.draw(gs::booleans()) {
        path.to_uppercase()
    } else {
        path.to_string()
    };
    disguise.replace("{}", &spelled)
}

/// A source file name no rule denies.
pub fn safe_file_names() -> gs::RegexGenerator {
    gs::from_regex("[a-z]{1,8}\\.rs")
}

/// A path the default policy accepts: up to four plain components of any case, dotted or
/// not, at any depth, naming nothing a rule denies.
#[hegel::composite]
pub fn source_paths(tc: &TestCase) -> String {
    let part = gs::from_regex("\\.?[A-Za-z0-9_-]([A-Za-z0-9_.-]{0,6}[A-Za-z0-9_-])?");
    let denied = |p: &String, rules: &[&str]| rules.iter().any(|d| d.eq_ignore_ascii_case(p));
    let parts = tc.draw(gs::vecs(part).min_size(1).max_size(4));
    tc.assume(!parts.iter().any(|p| denied(p, DEFAULT_POLICY.denied_names)));
    tc.assume(!denied(&parts[0], DEFAULT_POLICY.denied_at_root));
    parts.join("/")
}
