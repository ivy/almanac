use almanac::cooled::{Cooled, Published};
use std::time::Duration;
use time::macros::datetime;

const DAYS_3: Duration = Duration::from_secs(3 * 86400);
const NOW: time::OffsetDateTime = datetime!(2026-11-06 12:00 UTC);

#[test]
fn a_release_older_than_the_cooldown_is_cooled() {
    let published = Published::At(datetime!(2026-11-03 11:59 UTC));
    assert!(Cooled::check(published, DAYS_3, NOW).is_some());
}

#[test]
fn a_release_exactly_at_the_cooldown_is_cooled() {
    let published = Published::At(datetime!(2026-11-03 12:00 UTC));
    assert!(Cooled::check(published, DAYS_3, NOW).is_some());
}

#[test]
fn a_younger_release_is_not_cooled() {
    let published = Published::At(datetime!(2026-11-03 12:01 UTC));
    assert!(Cooled::check(published, DAYS_3, NOW).is_none());
}

#[test]
fn a_missing_timestamp_is_never_cooled() {
    assert!(Cooled::check(Published::Unknown, Duration::ZERO, NOW).is_none());
}

#[test]
fn a_timestamp_in_the_future_is_never_cooled() {
    let published = Published::At(datetime!(2030-01-01 0:00 UTC));
    assert!(Cooled::check(published, Duration::ZERO, NOW).is_none());
}
