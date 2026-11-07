use almanac::runtime::Limits;
use hegel::TestCase;
use hegel::generators as gs;
use std::time::Duration;

#[hegel::composite]
fn limits(tc: &TestCase) -> Limits {
    let cap = || gs::optional(gs::integers::<u32>().max_value(9_999));
    Limits {
        max_depth: tc.draw(gs::integers::<u32>().max_value(99)),
        runs_cap: tc.draw(cap()),
        overall_cap: tc.draw(cap()),
        concurrency: tc.draw(gs::integers::<usize>().min_value(1).max_value(63)),
        scan_interval: Duration::from_secs(
            tc.draw(gs::integers::<u64>().min_value(1).max_value(99_999)),
        ),
    }
}

fn no_wider(after: Option<u32>, before: Option<u32>) -> bool {
    match (after, before) {
        (Some(a), Some(b)) => a <= b,
        (None, Some(_)) => false,
        (_, None) => true,
    }
}

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
