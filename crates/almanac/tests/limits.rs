use almanac::runtime::Limits;
use proptest::prelude::*;

fn limits() -> impl Strategy<Value = Limits> {
    let cap = prop::option::of(0u32..10_000);
    (0u32..100, cap.clone(), cap, 1usize..64).prop_map(
        |(max_depth, runs_cap, overall_cap, concurrency)| Limits {
            max_depth,
            runs_cap,
            overall_cap,
            concurrency,
        },
    )
}

fn no_wider(after: Option<u32>, before: Option<u32>) -> bool {
    match (after, before) {
        (Some(a), Some(b)) => a <= b,
        (None, Some(_)) => false,
        (_, None) => true,
    }
}

proptest! {
    #[test]
    fn tighten_never_widens_any_limit(base in limits(), other in limits(), more in limits()) {
        let once = base.tighten(other);
        prop_assert!(once.max_depth <= base.max_depth);
        prop_assert!(once.concurrency <= base.concurrency);
        prop_assert!(no_wider(once.runs_cap, base.runs_cap));
        prop_assert!(no_wider(once.overall_cap, base.overall_cap));

        let twice = once.tighten(more);
        prop_assert!(twice.max_depth <= once.max_depth);
        prop_assert!(twice.concurrency <= once.concurrency);
        prop_assert!(no_wider(twice.runs_cap, once.runs_cap));
        prop_assert!(no_wider(twice.overall_cap, once.overall_cap));
    }
}
