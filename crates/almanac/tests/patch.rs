use almanac::prelude::*;
use proptest::prelude::*;

fn sha() -> Sha {
    "a".repeat(40).parse().unwrap()
}

fn patch(changes: Vec<Change>) -> Untrusted<Patch> {
    Untrusted::new(Patch {
        changes,
        diff: String::new(),
        answer: Default::default(),
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
fn evasion() -> impl Strategy<Value = String> {
    let denied = prop::sample::select(vec![
        ".github/workflows/ci.yml",
        ".git/hooks/pre-commit",
        "renovate.json",
        "mise.toml",
        ".gitmodules",
    ]);
    let disguise = prop::sample::select(vec![
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
    ]);
    let upper = any::<bool>();
    (denied, disguise, upper).prop_map(|(path, disguise, upper)| {
        let spelled = if upper {
            path.to_uppercase()
        } else {
            path.to_string()
        };
        disguise.replace("{}", &spelled)
    })
}

proptest! {
    #[test]
    fn no_spelling_of_a_denied_path_is_accepted(path in evasion()) {
        prop_assert!(verdict(vec![write(&path)]).is_err(), "{path:?} was accepted");
    }

    #[test]
    fn a_rename_cannot_hide_either_of_its_paths(path in evasion(), safe in "[a-z]{1,8}\\.rs") {
        let out = vec![delete(&path), write(&format!("src/{safe}"))];
        let back = vec![delete(&format!("src/{safe}")), write(&path)];
        prop_assert!(verdict(out).is_err());
        prop_assert!(verdict(back).is_err());
    }

    #[test]
    fn a_denied_path_hidden_among_safe_ones_is_found(
        path in evasion(),
        safe in prop::collection::vec("[a-z]{1,8}\\.rs", 0..10),
        at in 0usize..10,
    ) {
        let mut changes: Vec<Change> = safe.iter().map(|s| write(&format!("src/{s}"))).collect();
        changes.insert(at.min(changes.len()), write(&path));
        prop_assert!(verdict(changes).is_err());
    }

    #[test]
    fn plain_source_paths_are_accepted(
        dir in "[a-z]{1,8}",
        name in "[a-z]{1,8}\\.(rs|go|ts|py)",
    ) {
        prop_assert_eq!(verdict(vec![write(&format!("{dir}/{name}"))]), Ok(()));
    }
}
