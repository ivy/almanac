//! What registries say about a release, and what changed in it. Publish times feed the cooldown
//! proof; artifacts are read as data and never unpacked to disk or run.
mod diff;
mod http;

pub use diff::{Limits, PackageDiff, diff_archives};
pub use http::{Endpoints, HttpRegistry};

use almanac::prelude::*;

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Ecosystem {
    Npm,
    Crates,
    PyPi,
    Go,
    /// An action or anything else versioned by GitHub releases: the name is `owner/repo`.
    GithubRelease,
    Docker,
    /// A registry with no timestamps, such as ghcr.io. Never cooled.
    Unsupported,
}

/// A release, by validated name and version, because both arrive in hostile text.
#[derive(Clone, Debug, PartialEq)]
pub struct Package {
    pub ecosystem: Ecosystem,
    pub name: Slug,
    pub version: Slug,
}

impl Package {
    pub fn new(ecosystem: Ecosystem, name: Slug, version: Slug) -> Result<Self, StepError> {
        let traverses = |s: &Slug| {
            s.to_string()
                .split('/')
                .any(|part| part == ".." || part == ".")
        };
        if traverses(&name) || traverses(&version) {
            return Err(StepError(format!("{name}@{version} is not a package name")));
        }
        Ok(Self {
            ecosystem,
            name,
            version,
        })
    }
}

/// Reads registries. Implementations must report an unknown time as `Published::Unknown`.
pub trait PackageRegistry: Send + Sync {
    fn published<'a>(&'a self, package: &'a Package)
    -> BoxFuture<'a, Result<Published, StepError>>;

    /// The release's source archive, for diffing.
    fn artifact<'a>(&'a self, package: &'a Package) -> BoxFuture<'a, Result<Vec<u8>, StepError>>;
}
