//! What `luau-analyze` catches before a workflow runs. It is a separate binary, not part of the
//! host, so these need it on `PATH`: `mise exec luau@0.745 -- cargo test`. Without it they say so
//! and pass, because nothing about the host depends on it.
use std::path::{Path, PathBuf};
use std::process::Command;

const ROOT: &str = env!("CARGO_MANIFEST_DIR");

fn analyze(file: &Path) -> Option<(bool, String)> {
    let output = Command::new("luau-analyze")
        .arg("--mode=strict")
        .arg(file)
        .output()
        .ok()?;
    let text = String::from_utf8_lossy(&output.stderr).to_string();
    Some((output.status.success(), text))
}

fn fixtures(folder: &str) -> Vec<PathBuf> {
    let directory = Path::new(ROOT).join("tests/typecheck").join(folder);
    let mut files: Vec<PathBuf> = std::fs::read_dir(directory)
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .collect();
    files.sort();
    files
}

fn available() -> bool {
    // A version manager's shim can be on `PATH` with no version behind it, so this runs it.
    let found = Command::new("luau-analyze")
        .arg("--help")
        .output()
        .is_ok_and(|output| output.status.success());
    if !found {
        println!("luau-analyze is not on PATH, so nothing was checked");
    }
    found
}

#[test]
fn the_ported_workflow_is_clean_in_strict_mode() {
    if !available() {
        return;
    }
    for name in ["build_failures.luau", "almanac.luau"] {
        let file = Path::new(ROOT).join("workflows").join(name);
        let (clean, text) = analyze(&file).unwrap();
        assert!(clean, "{text}");
    }
}

#[test]
fn every_fixture_in_fail_is_refused_for_its_own_line() {
    if !available() {
        return;
    }
    for file in fixtures("fail") {
        let (clean, text) = analyze(&file).unwrap();
        let name = file.file_stem().unwrap().to_string_lossy();
        assert!(!clean, "{name} passed the analyzer");
        // Lines 1 to 10 are the header every fixture shares. A failure there, such as a `require`
        // that does not resolve, would make every case here pass for the wrong reason.
        let errors: Vec<u32> = text
            .lines()
            .filter(|line| line.contains("Error:"))
            .filter_map(|line| line.split(['(', ',']).nth(1)?.parse().ok())
            .collect();
        println!("fail   | {name} | lines {errors:?}");
        assert!(!errors.is_empty(), "{name}: {text}");
        assert!(errors.iter().all(|line| *line > 10), "{name}: {text}");
    }
}

/// The analyzer's limits, kept visible. Each of these is refused by the host or is a known gap,
/// and a fixture moving out of this folder means the analyzer got stronger.
#[test]
fn every_fixture_in_missed_passes_the_analyzer() {
    if !available() {
        return;
    }
    for file in fixtures("missed") {
        let (clean, text) = analyze(&file).unwrap();
        let name = file.file_stem().unwrap().to_string_lossy();
        println!("missed | {name}");
        assert!(clean, "{name} is caught after all: {text}");
    }
}
