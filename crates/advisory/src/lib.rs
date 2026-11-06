//! Vulnerability advisories, and whether an update really is the fix one names. Summaries from a
//! feed may be model-written, so all text stays tainted: only ranges and flags are `Clean`.
mod cve_now;
mod version;

pub use cve_now::CveNow;
pub use version::compare;

use almanac::prelude::*;
use std::cmp::Ordering;
use std::collections::HashMap;
use std::sync::Mutex;

clean! {
    pub enum Severity { Low, Medium, High, Critical, Unknown }
}

clean! {
    /// Affected from `introduced` up to, but not including, `fixed`.
    pub struct Range {
        pub introduced: Option<Slug>,
        pub fixed: Option<Slug>,
    }
}

clean! {
    pub struct Affected {
        pub ecosystem: Slug,
        pub package: Slug,
        pub ranges: Vec<Range>,
    }
}

observed! {
    pub struct Advisory {
        pub id: Slug,
        pub aliases: Vec<Slug>,
        pub severity: Severity,
        pub kev: bool,
        pub affected: Vec<Affected>,
        pub summary: Untrusted<String>,
    }
}

clean! {
    /// Whether an update does what an advisory says fixes it.
    pub enum Validation {
        Valid,
        /// The advisory does not list this package.
        PackageNotListed,
        /// The current version is outside every affected range.
        NotAffected,
        /// The target is still inside an affected range.
        TargetStillAffected,
        /// The target is past the patched version, so it is more than the fix.
        NotThePatchedVersion,
        /// The versions could not be compared, so nothing is proved.
        Incomparable,
    }
}

fn inside(range: &Range, ecosystem: &str, version: &str) -> Option<bool> {
    let at_or_after = match &range.introduced {
        Some(from) => compare(ecosystem, version, &from.to_string())? != Ordering::Less,
        None => true,
    };
    let before_fix = match &range.fixed {
        Some(fixed) => compare(ecosystem, version, &fixed.to_string())? == Ordering::Less,
        None => true,
    };
    Some(at_or_after && before_fix)
}

/// Checks an update against an advisory. Anything unclear fails closed.
pub fn validate(
    advisory: &Advisory,
    ecosystem: &str,
    package: &str,
    current: &str,
    target: &str,
) -> Validation {
    let listed = |a: &&Affected| {
        a.ecosystem.to_string().eq_ignore_ascii_case(ecosystem) && a.package.to_string() == package
    };
    let Some(affected) = advisory.affected.iter().find(listed) else {
        return Validation::PackageNotListed;
    };
    for range in &affected.ranges {
        let Some(current_inside) = inside(range, ecosystem, current) else {
            return Validation::Incomparable;
        };
        if !current_inside {
            continue;
        }
        let Some(target_inside) = inside(range, ecosystem, target) else {
            return Validation::Incomparable;
        };
        if target_inside {
            return Validation::TargetStillAffected;
        }
        let Some(fixed) = &range.fixed else {
            return Validation::TargetStillAffected;
        };
        return match compare(ecosystem, target, &fixed.to_string()) {
            Some(Ordering::Equal) => Validation::Valid,
            Some(Ordering::Greater) => Validation::NotThePatchedVersion,
            Some(Ordering::Less) => Validation::TargetStillAffected,
            None => Validation::Incomparable,
        };
    }
    Validation::NotAffected
}

/// Where advisories come from.
pub trait Advisories: Send + Sync {
    /// By GHSA or CVE ID. `None` when the feed does not know it.
    fn by_id<'a>(&'a self, id: &'a Slug) -> BoxFuture<'a, Result<Option<Advisory>, StepError>>;

    fn by_package<'a>(
        &'a self,
        ecosystem: &'a Slug,
        package: &'a Slug,
    ) -> BoxFuture<'a, Result<Vec<Advisory>, StepError>>;
}

/// A feed held in memory, for tests.
#[derive(Default)]
pub struct FakeAdvisories {
    known: Mutex<HashMap<String, Advisory>>,
}

impl FakeAdvisories {
    pub fn with(self, advisory: Advisory) -> Self {
        let id = advisory.id.to_string();
        self.known.lock().unwrap().insert(id, advisory);
        self
    }
}

impl Advisories for FakeAdvisories {
    fn by_id<'a>(&'a self, id: &'a Slug) -> BoxFuture<'a, Result<Option<Advisory>, StepError>> {
        Box::pin(async move {
            let known = self.known.lock().unwrap();
            Ok(known.get(&id.to_string()).cloned())
        })
    }

    fn by_package<'a>(
        &'a self,
        ecosystem: &'a Slug,
        package: &'a Slug,
    ) -> BoxFuture<'a, Result<Vec<Advisory>, StepError>> {
        Box::pin(async move {
            let known = self.known.lock().unwrap();
            let lists = |a: &&Advisory| {
                a.affected
                    .iter()
                    .any(|x| x.ecosystem == *ecosystem && x.package == *package)
            };
            Ok(known.values().filter(lists).cloned().collect())
        })
    }
}
