//! Generators for property tests, shared by every crate's tests so a property names what it
//! draws and not how. Built only with the `hegel` feature, which nothing that ships enables.
use crate::runtime::Limits;
use hegel::TestCase;
use hegel::generators as gs;
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

/// A release version as `(major, minor, patch)`.
#[hegel::composite]
pub fn versions(tc: &TestCase) -> (u64, u64, u64) {
    let part = || gs::integers::<u64>().max_value(49);
    (tc.draw(part()), tc.draw(part()), tc.draw(part()))
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

/// A path to an ordinary source file, which the default policy accepts.
#[hegel::composite]
pub fn source_paths(tc: &TestCase) -> String {
    let dir = tc.draw(gs::from_regex("[a-z]{1,8}"));
    let name = tc.draw(gs::from_regex("[a-z]{1,8}\\.(rs|go|ts|py)"));
    format!("{dir}/{name}")
}
