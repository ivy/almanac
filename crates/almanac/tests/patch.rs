use almanac::prelude::*;
use base64::{Engine, engine::general_purpose::STANDARD};
use hegel::TestCase;
use hegel::generators as gs;

fn sha() -> Sha {
    "a".repeat(40).parse().unwrap()
}

fn patch(changes: Vec<Change>) -> Untrusted<Patch> {
    Untrusted::new(Patch {
        changes,
        diff: String::new(),
        answer: Default::default(),
        usage: Default::default(),
    })
}

fn write(path: &str) -> Change {
    Change::Write {
        path: path.to_string(),
        content: "eA==".to_string(),
        executable: false,
    }
}

fn delete(path: &str) -> Change {
    Change::Delete {
        path: path.to_string(),
    }
}

fn verdict(changes: Vec<Change>) -> Result<(), Rejection> {
    DEFAULT_POLICY.check(&patch(changes), &sha()).map(|_| ())
}

#[test]
fn a_clean_repair_is_accepted() {
    let changes = vec![
        write("src/main.rs"),
        write("Cargo.lock"),
        delete("src/old.rs"),
    ];
    assert_eq!(verdict(changes), Ok(()));
}

#[test]
fn an_injected_agent_cannot_edit_ci() {
    let changes = vec![write("src/main.rs"), write(".github/workflows/ci.yml")];
    assert_eq!(verdict(changes), Err(Rejection::Denied));
}

#[test]
fn every_default_rule_holds() {
    for path in [
        ".github/workflows/ci.yml",
        ".github/CODEOWNERS",
        "nested/.github/workflows/ci.yml",
        ".gitlab-ci.yml",
        ".circleci/config.yml",
        ".buildkite/pipeline.yml",
        ".husky/pre-commit",
        ".git/config",
        ".gitmodules",
        ".gitattributes",
        ".envrc",
        "renovate.json",
        "renovate.json5",
        ".renovaterc",
        ".renovaterc.json",
        "mise.toml",
        ".mise.toml",
        ".tool-versions",
    ] {
        assert_eq!(verdict(vec![write(path)]), Err(Rejection::Denied), "{path}");
    }
}

#[test]
fn a_rename_out_of_a_denied_path_is_refused_by_its_delete() {
    let changes = vec![delete(".github/workflows/ci.yml"), write("ci.yml")];
    assert_eq!(verdict(changes), Err(Rejection::Denied));
}

#[test]
fn a_nested_file_named_like_a_root_rule_is_allowed() {
    assert_eq!(verdict(vec![write("templates/renovate.json")]), Ok(()));
}

#[test]
fn a_symbolic_link_is_refused_anywhere() {
    let link = Change::Link {
        path: "src/link".to_string(),
    };
    assert_eq!(verdict(vec![link]), Err(Rejection::Link));
}

#[test]
fn a_binary_file_is_refused_and_text_is_not() {
    let write = |content: &str| Change::Write {
        path: "src/state".to_string(),
        content: content.to_string(),
        executable: false,
    };
    assert_eq!(verdict(vec![write("AAECAw==")]), Err(Rejection::Binary));
    assert!(verdict(vec![write("Zml4ZWQK")]).is_ok());
}

#[test]
fn an_empty_patch_is_refused() {
    assert_eq!(verdict(vec![]), Err(Rejection::Empty));
}

#[test]
fn too_many_files_are_refused() {
    let changes = (0..51).map(|i| write(&format!("src/{i}.rs"))).collect();
    assert_eq!(verdict(changes), Err(Rejection::TooManyFiles));
}

#[test]
fn too_many_bytes_are_refused() {
    let big = Change::Write {
        path: "src/big".to_string(),
        content: "x".repeat(DEFAULT_POLICY.max_bytes + 1),
        executable: false,
    };
    assert_eq!(verdict(vec![big]), Err(Rejection::TooLarge));
}

#[test]
fn a_checked_patch_remembers_its_base() {
    let checked = DEFAULT_POLICY
        .check(&patch(vec![write("src/a.rs")]), &sha())
        .unwrap();
    assert_eq!(checked.base(), &sha());
    assert_eq!(checked.patch().changes.len(), 1);
}

/// Ways of spelling a path so it reaches a denied file while looking like something else.
#[hegel::composite]
fn evasion(tc: &TestCase) -> String {
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

fn safe_name() -> gs::RegexGenerator {
    gs::from_regex("[a-z]{1,8}\\.rs")
}

#[hegel::test]
fn no_spelling_of_a_denied_path_is_accepted(tc: TestCase) {
    let path = tc.draw(evasion());
    assert!(
        verdict(vec![write(&path)]).is_err(),
        "{path:?} was accepted"
    );
}

#[hegel::test]
fn a_rename_cannot_hide_either_of_its_paths(tc: TestCase) {
    let path = tc.draw(evasion());
    let safe = tc.draw(safe_name());
    let out = vec![delete(&path), write(&format!("src/{safe}"))];
    let back = vec![delete(&format!("src/{safe}")), write(&path)];
    assert!(verdict(out).is_err());
    assert!(verdict(back).is_err());
}

#[hegel::test]
fn a_denied_path_hidden_among_safe_ones_is_found(tc: TestCase) {
    let path = tc.draw(evasion());
    let safe = tc.draw(gs::vecs(safe_name()).max_size(9));
    let at = tc.draw(gs::integers::<usize>().max_value(9));
    let mut changes: Vec<Change> = safe.iter().map(|s| write(&format!("src/{s}"))).collect();
    changes.insert(at.min(changes.len()), write(&path));
    assert!(verdict(changes).is_err());
}

#[hegel::test]
fn plain_source_paths_are_accepted(tc: TestCase) {
    let dir = tc.draw(gs::from_regex("[a-z]{1,8}"));
    let name = tc.draw(gs::from_regex("[a-z]{1,8}\\.(rs|go|ts|py)"));
    assert_eq!(verdict(vec![write(&format!("{dir}/{name}"))]), Ok(()));
}

#[test]
fn a_patch_keeps_a_line_it_leaves_alone_or_only_moves() {
    let content = STANDARD.encode("  \"tar\": \"7.0.0\"\n");
    let changes = vec![Change::Write {
        path: "package.json".to_string(),
        content,
        executable: false,
    }];
    let patch = Patch {
        changes,
        diff: String::new(),
        answer: Default::default(),
        usage: Default::default(),
    };
    assert!(patch.keeps("package.json", "    \"tar\": \"7.0.0\","));
    assert!(patch.keeps("other.json", "anything"));
    assert!(!patch.keeps("package.json", "\"tar\": \"6.0.0\""));
}

#[test]
fn a_patch_deleting_a_file_keeps_none_of_it() {
    let changes = vec![Change::Delete {
        path: "package.json".to_string(),
    }];
    let patch = Patch {
        changes,
        diff: String::new(),
        answer: Default::default(),
        usage: Default::default(),
    };
    assert!(!patch.keeps("package.json", "\"tar\": \"7.0.0\""));
}
