//! Checks on a security update. What a pull request claims is hostile, so the update arrives
//! tainted and is opened only here, at the boundary, and all that goes back is facts.
use crate::update::{Update, UpdatedPackage, ecosystem, opened};
use advisory::{Advisories, Validation, validate};
use almanac::prelude::*;
use registry::{Limits, Package, PackageRegistry, diff_archives};
use std::sync::Arc;

observed! {
    /// What changed in the update's packages.
    pub struct UpdateDiff {
        pub readable: bool,
        pub install_script_added: bool,
        // Part of the diff was cut to fit, so nothing has read the whole of this update.
        pub truncated: bool,
        // Files that changed and that nothing can read, by name.
        pub unshown: Untrusted<String>,
        pub diff: Untrusted<String>,
    }
}

/// The deterministic checks on a security update.
pub trait Vetting: Send + Sync {
    /// Whether an advisory names every package's target as its fix, from an affected version.
    fn names_the_fix<'a>(
        &'a self,
        update: &'a Untrusted<Update>,
    ) -> BoxFuture<'a, Result<bool, StepError>>;

    fn diff<'a>(
        &'a self,
        update: &'a Untrusted<Update>,
    ) -> BoxFuture<'a, Result<UpdateDiff, StepError>>;
}

pub struct RealVetting {
    pub advisories: Arc<dyn Advisories>,
    pub registry: Arc<dyn PackageRegistry>,
}

/// How advisory feeds name an ecosystem, which differs from Renovate's datasource names.
fn feed_ecosystem(datasource: &str) -> Option<&'static str> {
    match datasource {
        "npm" => Some("npm"),
        "crate" => Some("crates.io"),
        "pypi" => Some("PyPI"),
        "go" => Some("Go"),
        _ => None,
    }
}

fn version_of(p: &UpdatedPackage, version: &str) -> Option<Package> {
    let name: Slug = p.name.parse().ok()?;
    let version: Slug = version.parse().ok()?;
    Package::new(ecosystem(&p.datasource), name, version).ok()
}

/// Adds a package's unshown files to the list. It stays tainted, so that no step can lose it.
pub fn with_unshown((all, names): &(String, Vec<String>)) -> String {
    let mut lines: Vec<&str> = all.lines().collect();
    lines.extend(names.iter().map(String::as_str));
    lines.join("\n")
}

impl RealVetting {
    async fn fixed_by(&self, p: &UpdatedPackage) -> Result<bool, StepError> {
        let Some(feed) = feed_ecosystem(&p.datasource) else {
            return Ok(false);
        };
        let (Ok(eco), Ok(name)) = (feed.parse::<Slug>(), p.name.parse::<Slug>()) else {
            return Ok(false);
        };
        let found = self.advisories.by_package(&eco, &name).await?;
        let valid = |a: &advisory::Advisory| {
            validate(a, feed, &p.name, &p.current, &p.version) == Validation::Valid
        };
        Ok(found.iter().any(valid))
    }

    async fn one_diff(
        &self,
        p: &UpdatedPackage,
    ) -> Result<Option<registry::PackageDiff>, StepError> {
        let (Some(old), Some(new)) = (version_of(p, &p.current), version_of(p, &p.version)) else {
            return Ok(None);
        };
        let before = self.registry.artifact(&old).await?;
        let after = self.registry.artifact(&new).await?;
        Ok(diff_archives(&before, &after, Limits::default()).ok())
    }
}

impl Vetting for RealVetting {
    fn names_the_fix<'a>(
        &'a self,
        update: &'a Untrusted<Update>,
    ) -> BoxFuture<'a, Result<bool, StepError>> {
        Box::pin(async move {
            let update = opened(update)?;
            if update.packages.is_empty() {
                return Ok(false);
            }
            for package in &update.packages {
                if !self.fixed_by(package).await? {
                    return Ok(false);
                }
            }
            Ok(true)
        })
    }

    fn diff<'a>(
        &'a self,
        update: &'a Untrusted<Update>,
    ) -> BoxFuture<'a, Result<UpdateDiff, StepError>> {
        Box::pin(async move {
            let update = opened(update)?;
            let unreadable = || UpdateDiff {
                readable: false,
                install_script_added: false,
                truncated: false,
                unshown: Untrusted::new(String::new()),
                diff: Untrusted::new(String::new()),
            };
            let mut text = Untrusted::new(String::new());
            let mut install_script_added = false;
            let mut truncated = false;
            let mut unshown = Untrusted::new(String::new());
            for package in &update.packages {
                let Some(found) = self.one_diff(package).await? else {
                    return Ok(unreadable());
                };
                install_script_added |= found.install_script_added;
                truncated |= found.truncated;
                unshown = unshown.zip(&found.unshown).map(with_unshown);
                text = text
                    .zip(&found.diff)
                    .map(|(all, diff)| format!("{all}{diff}"));
            }
            Ok(UpdateDiff {
                readable: !update.packages.is_empty(),
                install_script_added,
                truncated,
                unshown,
                diff: text,
            })
        })
    }
}
