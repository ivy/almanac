//! Proof that a release has aged past a cooldown. A status check can be set by anything with
//! write access, so this rests only on a publish time read from the registry.
use serde::{Deserialize, Serialize};
use std::time::Duration;
use time::OffsetDateTime;

/// A release's publish time, as a registry reports it. Many have none.
#[derive(Clone, Copy, Debug, PartialEq, Serialize, Deserialize)]
pub enum Published {
    At(#[serde(with = "time::serde::rfc3339")] OffsetDateTime),
    Unknown,
}

/// What is known about how old a release is.
#[derive(Debug)]
pub enum Age {
    Passed(Cooled),
    Young,
    /// Nothing says when it was published, so waiting will not help.
    Unknown,
}

/// Held only by code that passed `check`. It has no public constructor, and neither `Clone`
/// nor `Deserialize`, so a recorded or forged value cannot stand in for it.
#[derive(Debug)]
pub struct Cooled(());

impl Cooled {
    /// `None` unless the release is known to be older than `min_age`. A release published in
    /// the future counts as not cooled, and so does a missing timestamp.
    pub fn check(published: Published, min_age: Duration, now: OffsetDateTime) -> Option<Self> {
        let Published::At(at) = published else {
            return None;
        };
        let age = now - at;
        (age.is_positive() && age >= min_age).then_some(Self(()))
    }
}
