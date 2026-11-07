use almanac::testing::generators::limits;
use hegel::TestCase;

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
