use almanac::config::Config;
use almanac::testing::generators::{config_texts, limits};
use hegel::TestCase;

fn no_wider(after: Option<u32>, before: Option<u32>) -> bool {
    match (after, before) {
        (Some(a), Some(b)) => a <= b,
        (None, Some(_)) => false,
        (_, None) => true,
    }
}

/// R-44: however often config tightens, no limit gets wider.
#[hegel::test]
fn tighten_never_widens_any_limit(tc: TestCase) {
    let base = tc.draw(limits());
    let other = tc.draw(limits());
    let more = tc.draw(limits());
    let once = base.tighten(other);
    assert!(once.max_depth <= base.max_depth);
    assert!(once.concurrency <= base.concurrency);
    assert!(no_wider(once.runs_cap, base.runs_cap));
    assert!(no_wider(once.overall_cap, base.overall_cap));
    assert!(once.scan_interval >= base.scan_interval);

    let twice = once.tighten(more);
    assert!(twice.max_depth <= once.max_depth);
    assert!(twice.concurrency <= once.concurrency);
    assert!(no_wider(twice.runs_cap, once.runs_cap));
    assert!(no_wider(twice.overall_cap, once.overall_cap));
    assert!(twice.scan_interval >= once.scan_interval);
}

/// R-44: each limit becomes the tighter of the two, and a cap is unlimited only when both are.
#[hegel::test]
fn tighten_takes_the_tighter_of_each_limit(tc: TestCase) {
    let a = tc.draw(limits());
    let b = tc.draw(limits());
    let tighter_cap = |x: Option<u32>, y: Option<u32>| match (x, y) {
        (Some(x), Some(y)) => Some(x.min(y)),
        (Some(x), None) | (None, Some(x)) => Some(x),
        (None, None) => None,
    };
    let t = a.tighten(b);
    assert_eq!(t.max_depth, a.max_depth.min(b.max_depth));
    assert_eq!(t.runs_cap, tighter_cap(a.runs_cap, b.runs_cap));
    assert_eq!(t.overall_cap, tighter_cap(a.overall_cap, b.overall_cap));
    assert_eq!(t.concurrency, a.concurrency.min(b.concurrency));
    assert_eq!(t.scan_interval, a.scan_interval.max(b.scan_interval));
}

/// Principle 9, R-44: any config file that parses gives limits, never a panic, and they are
/// no wider than the binary's own.
#[hegel::test]
fn any_config_that_parses_tightens_without_panicking(tc: TestCase) {
    let text = tc.draw(config_texts());
    let Ok(config) = Config::parse("almanac.toml", text) else {
        return;
    };
    let binary = almanac::runtime::Limits::default();
    let set = binary.tighten(config.limits());
    assert!(set.max_depth <= binary.max_depth && set.concurrency <= binary.concurrency);
    assert!(set.scan_interval >= binary.scan_interval);
}
