//! Comparing versions by each ecosystem's rules. `None` means it cannot be said, which callers
//! must treat as not proved.
use std::cmp::Ordering;

fn semver(text: &str) -> Option<semver::Version> {
    let bare = text.strip_prefix('v').unwrap_or(text);
    if let Ok(version) = semver::Version::parse(bare) {
        return Some(version);
    }
    // Registries also use shortened forms such as `1.2`.
    let parts = bare.split('.').count();
    let padded = match parts {
        1 => format!("{bare}.0.0"),
        2 => format!("{bare}.0"),
        _ => return None,
    };
    semver::Version::parse(&padded).ok()
}

/// A plain dotted release, such as `1.26.0`. Anything fancier is not compared.
fn dotted(text: &str) -> Option<Vec<u64>> {
    text.split('.').map(|part| part.parse().ok()).collect()
}

fn compare_dotted(a: &str, b: &str) -> Option<Ordering> {
    let (mut a, mut b) = (dotted(a)?, dotted(b)?);
    let width = a.len().max(b.len());
    a.resize(width, 0);
    b.resize(width, 0);
    Some(a.cmp(&b))
}

/// Orders two versions of a package in `ecosystem`, named as OSV names them.
pub fn compare(ecosystem: &str, a: &str, b: &str) -> Option<Ordering> {
    match ecosystem.to_lowercase().as_str() {
        "npm" | "crates.io" | "go" | "packagist" | "rubygems" => {
            Some(semver(a)?.cmp_precedence(&semver(b)?))
        }
        "pypi" => compare_dotted(a, b),
        _ => (a == b).then_some(Ordering::Equal),
    }
}
