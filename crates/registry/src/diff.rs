//! Compares two published archives entirely in memory. Nothing is written to disk and nothing
//! in an archive is run, so a hostile install script is only text.
use almanac::prelude::*;
use flate2::read::GzDecoder;
use similar::TextDiff;
use std::collections::BTreeMap;
use std::io::{Cursor, Read};
use std::path::{Component, Path};

/// Bounds on what an archive may expand to, against decompression bombs.
#[derive(Clone, Copy, Debug)]
pub struct Limits {
    pub max_total_bytes: u64,
    pub max_file_bytes: u64,
    pub max_entries: usize,
    pub max_diff_bytes: usize,
}

impl Default for Limits {
    fn default() -> Self {
        Self {
            max_total_bytes: 256 * 1024 * 1024,
            max_file_bytes: 16 * 1024 * 1024,
            max_entries: 50_000,
            max_diff_bytes: 512 * 1024,
        }
    }
}

observed! {
    /// What changed between two releases. The facts are typed and the diff stays tainted.
    pub struct PackageDiff {
        pub install_script_added: bool,
        pub files_changed: u64,
        pub truncated: bool,
        pub diff: Untrusted<String>,
    }
}

type Files = BTreeMap<String, Vec<u8>>;

fn refused(reason: impl std::fmt::Display) -> StepError {
    StepError(format!("archive refused: {reason}"))
}

fn safe_path(path: &Path) -> Result<String, StepError> {
    let hostile = path
        .components()
        .any(|c| !matches!(c, Component::Normal(_) | Component::CurDir));
    if hostile {
        return Err(refused(format!("unsafe path {}", path.display())));
    }
    Ok(path.to_string_lossy().into_owned())
}

struct Budget {
    limits: Limits,
    total: u64,
    entries: usize,
}

impl Budget {
    fn new(limits: Limits) -> Self {
        Self {
            limits,
            total: 0,
            entries: 0,
        }
    }

    /// Reads an entry, trusting no size the archive claims.
    fn read(&mut self, entry: &mut impl Read) -> Result<Vec<u8>, StepError> {
        self.entries += 1;
        if self.entries > self.limits.max_entries {
            return Err(refused("too many entries"));
        }
        let mut bytes = vec![];
        let cap = self.limits.max_file_bytes + 1;
        entry.take(cap).read_to_end(&mut bytes).map_err(refused)?;
        if bytes.len() as u64 >= cap {
            return Err(refused("a file is too large"));
        }
        self.total += bytes.len() as u64;
        if self.total > self.limits.max_total_bytes {
            return Err(refused("it expands too far"));
        }
        Ok(bytes)
    }
}

fn read_tar(bytes: &[u8], limits: Limits) -> Result<Files, StepError> {
    let mut archive = tar::Archive::new(GzDecoder::new(bytes));
    let mut budget = Budget::new(limits);
    let mut files = Files::new();
    for entry in archive.entries().map_err(refused)? {
        let mut entry = entry.map_err(refused)?;
        let path = entry.path().map_err(refused)?.into_owned();
        let name = safe_path(&path)?;
        let kind = entry.header().entry_type();
        if kind.is_symlink() || kind.is_hard_link() {
            return Err(refused(format!("link at {name}")));
        }
        if kind.is_file() {
            let content = budget.read(&mut entry)?;
            files.insert(name, content);
        }
    }
    Ok(files)
}

fn read_zip(bytes: &[u8], limits: Limits) -> Result<Files, StepError> {
    let mut archive = zip::ZipArchive::new(Cursor::new(bytes)).map_err(refused)?;
    let mut budget = Budget::new(limits);
    let mut files = Files::new();
    for i in 0..archive.len() {
        let mut entry = archive.by_index(i).map_err(refused)?;
        let raw = entry.name().map_err(refused)?.into_owned();
        let name = safe_path(Path::new(&raw))?;
        if entry.is_symlink() {
            return Err(refused(format!("link at {name}")));
        }
        if entry.is_file() {
            let content = budget.read(&mut entry)?;
            files.insert(name, content);
        }
    }
    Ok(files)
}

fn read_archive(bytes: &[u8], limits: Limits) -> Result<Files, StepError> {
    match bytes {
        [0x1f, 0x8b, ..] => read_tar(bytes, limits),
        [b'P', b'K', ..] => read_zip(bytes, limits),
        _ => Err(refused("not a tar.gz or zip")),
    }
}

/// A Go module zip nests everything under `module/path@version/`, which shares nothing between
/// two versions, so this is the prefix to remove. It is `None` for any other layout.
fn module_root(files: &Files) -> Option<String> {
    let first = files.keys().next()?;
    let at = first.find('@')?;
    let root = &first[..at + first[at..].find('/')? + 1];
    files
        .keys()
        .all(|name| name.starts_with(root))
        .then(|| root.to_string())
}

/// Archives usually wrap everything in one top directory that changes per version.
fn without_top(files: Files) -> Files {
    if let Some(root) = module_root(&files) {
        let inside = |(name, bytes): (String, Vec<u8>)| (name[root.len()..].to_string(), bytes);
        return files.into_iter().map(inside).collect();
    }
    let tops: std::collections::BTreeSet<&str> =
        files.keys().filter_map(|k| k.split('/').next()).collect();
    let wrapped = tops.len() == 1 && files.keys().all(|k| k.contains('/'));
    if !wrapped {
        return files;
    }
    files
        .into_iter()
        .map(|(k, v)| {
            (
                k.split_once('/').map_or(k.clone(), |(_, r)| r.to_string()),
                v,
            )
        })
        .collect()
}

const NPM_HOOKS: [&str; 3] = ["preinstall", "install", "postinstall"];

fn npm_hooks(files: &Files) -> Vec<String> {
    let manifest = files
        .get("package.json")
        .and_then(|bytes| serde_json::from_slice::<serde_json::Value>(bytes).ok());
    let Some(manifest) = manifest else {
        return vec![];
    };
    NPM_HOOKS
        .iter()
        .filter_map(|hook| {
            let script = manifest["scripts"][hook].as_str()?;
            Some(format!("{hook}:{script}"))
        })
        .collect()
}

/// Build-time code a package manager runs for you.
fn install_script_added(old: &Files, new: &Files) -> bool {
    let old_hooks = npm_hooks(old);
    let hook_added = npm_hooks(new).iter().any(|h| !old_hooks.contains(h));
    let appeared = ["build.rs", "setup.py"]
        .iter()
        .any(|f| new.contains_key(*f) && !old.contains_key(*f));
    hook_added || appeared
}

/// A change as a reviewer reads it. Binary content cannot be read as text, so it is named rather
/// than shown, and a reviewer still sees that it changed.
fn shown_change(name: &str, before: &[u8], after: &[u8]) -> String {
    if before.contains(&0) || after.contains(&0) {
        return format!("Binary file changed, and not shown: {name}\n");
    }
    let (a, b) = (
        String::from_utf8_lossy(before),
        String::from_utf8_lossy(after),
    );
    let diff = TextDiff::from_lines(a.as_ref(), b.as_ref());
    diff.unified_diff()
        .header(&format!("a/{name}"), &format!("b/{name}"))
        .to_string()
}

/// Diffs two archives of a package, in memory.
pub fn diff_archives(old: &[u8], new: &[u8], limits: Limits) -> Result<PackageDiff, StepError> {
    let old = without_top(read_archive(old, limits)?);
    let new = without_top(read_archive(new, limits)?);
    let empty = vec![];
    let names: std::collections::BTreeSet<&String> = old.keys().chain(new.keys()).collect();
    let mut text = String::new();
    let mut changed = 0;
    let mut truncated = false;
    for name in names {
        let before = old.get(name).unwrap_or(&empty);
        let after = new.get(name).unwrap_or(&empty);
        if before == after {
            continue;
        }
        changed += 1;
        if truncated {
            continue;
        }
        let shown = shown_change(name, before, after);
        if text.len() + shown.len() > limits.max_diff_bytes {
            truncated = true;
            continue;
        }
        text.push_str(&shown);
    }
    Ok(PackageDiff {
        install_script_added: install_script_added(&old, &new),
        files_changed: changed,
        truncated,
        diff: Untrusted::new(text),
    })
}
