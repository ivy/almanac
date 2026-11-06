//! What a `Fixer` hands back, and the deterministic check that decides whether it may be pushed.
//! A patch is a list of file changes, not diff text, so nothing parses hostile diff syntax, and a
//! rename is a delete and a write, so both of its paths are checked.
use crate::taint::{Sha, Untrusted};
use serde::{Deserialize, Serialize};

/// A file's new state. Paths and content are hostile.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub enum Change {
    Write {
        path: String,
        /// Base64, because a patch can change binary files.
        content: String,
        executable: bool,
    },
    Delete {
        path: String,
    },
    /// A symbolic link. Always rejected: a link can point a later write somewhere else.
    Link {
        path: String,
    },
}

impl Change {
    fn path(&self) -> &str {
        match self {
            Self::Write { path, .. } | Self::Delete { path } | Self::Link { path } => path,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Patch {
    pub changes: Vec<Change>,
    /// The same changes as a diff, for people to read. Never applied.
    pub diff: String,
}

crate::clean! {
    /// Why a patch was refused. It names a reason and not a path, because paths are hostile.
    pub enum Rejection { Empty, UnsafePath, Denied, Link, TooManyFiles, TooLarge }
}

/// Which paths a patch may not touch. Matching ignores case.
#[derive(Clone, Copy, Debug)]
pub struct PatchPolicy {
    /// Refused at any depth, as a directory or a file.
    pub denied_names: &'static [&'static str],
    /// Refused at the repository root only.
    pub denied_at_root: &'static [&'static str],
    pub max_files: usize,
    pub max_bytes: usize,
}

/// A pushed patch runs in CI, which may hold secrets, so everything that configures CI, the
/// toolchain, or Renovate is out of reach unless a workflow declares otherwise.
pub const DEFAULT_POLICY: PatchPolicy = PatchPolicy {
    denied_names: &[
        ".git",
        ".github",
        ".gitlab-ci.yml",
        ".circleci",
        ".buildkite",
        ".husky",
        ".envrc",
        ".gitmodules",
        ".gitattributes",
    ],
    denied_at_root: &[
        "renovate.json",
        "renovate.json5",
        ".renovaterc",
        ".renovaterc.json",
        ".renovaterc.json5",
        "mise.toml",
        ".mise.toml",
        "mise.local.toml",
        ".tool-versions",
    ],
    max_files: 50,
    max_bytes: 1024 * 1024,
};

/// A patch the policy accepted, for a known base commit. Only `PatchPolicy::check` makes one, and
/// it cannot be deserialized, so stored or model-written data cannot pose as checked.
#[derive(Clone, Debug)]
pub struct Checked {
    patch: Patch,
    base: Sha,
}

impl Checked {
    pub fn patch(&self) -> &Patch {
        &self.patch
    }

    pub fn base(&self) -> &Sha {
        &self.base
    }
}

/// A path's components, or `None` if it cannot be trusted to mean what it appears to mean.
fn components(path: &str) -> Option<Vec<String>> {
    let forbidden = |c: char| c.is_control() || c == '\\' || c == ':';
    if path.is_empty() || path.starts_with('/') || path.chars().any(forbidden) {
        return None;
    }
    let parts: Vec<&str> = path.split('/').collect();
    let unsafe_part = |p: &&str| {
        // Some filesystems drop trailing dots and spaces, so `.github.` could mean `.github`.
        p.is_empty() || p.ends_with('.') || p.ends_with(' ')
    };
    if parts.iter().any(unsafe_part) {
        return None;
    }
    Some(parts.iter().map(|p| p.to_lowercase()).collect())
}

impl PatchPolicy {
    fn allows(&self, parts: &[String]) -> bool {
        let named = |p: &String| self.denied_names.iter().any(|d| d.eq_ignore_ascii_case(p));
        let at_root = |p: &String| {
            self.denied_at_root
                .iter()
                .any(|d| d.eq_ignore_ascii_case(p))
        };
        !parts.iter().any(named) && !parts.first().is_some_and(at_root)
    }

    fn review(&self, patch: &Patch) -> Result<(), Rejection> {
        if patch.changes.is_empty() {
            return Err(Rejection::Empty);
        }
        if patch.changes.len() > self.max_files {
            return Err(Rejection::TooManyFiles);
        }
        let mut bytes = 0;
        for change in &patch.changes {
            let parts = components(change.path()).ok_or(Rejection::UnsafePath)?;
            if !self.allows(&parts) {
                return Err(Rejection::Denied);
            }
            match change {
                Change::Link { .. } => return Err(Rejection::Link),
                Change::Write { content, .. } => bytes += content.len(),
                Change::Delete { .. } => {}
            }
        }
        if bytes > self.max_bytes {
            return Err(Rejection::TooLarge);
        }
        Ok(())
    }

    /// The only way to a `Checked`. A forge later refuses it unless `base` is still the branch's
    /// head, which is how a patch is known to apply cleanly.
    pub fn check(&self, patch: &Untrusted<Patch>, base: &Sha) -> Result<Checked, Rejection> {
        let inner = patch.inner();
        self.review(inner)?;
        Ok(Checked {
            patch: inner.clone(),
            base: base.clone(),
        })
    }
}
