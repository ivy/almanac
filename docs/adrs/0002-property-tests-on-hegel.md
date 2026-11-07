# 2. Property tests run on Hegel

## Status

Accepted.

## Context

The patch policy, version comparison, limit tightening, and fleet states are each checked by properties as well as examples. Those properties ran on `proptest`, and later changes will add more, for parsers and for state machines whose inputs depend on earlier draws.

Principle 10 says dependencies favour maturity over novelty. `proptest` is mature. `hegeltest`, the Rust library for [Hegel](https://hegel.dev/), is a beta, and its README says it "may make breaking changes". Choosing it over a mature crate that works needs a reason in writing.

## Decision

Property tests use `hegeltest`, imported as `hegel`, as a dev-dependency only, built with `default-features = false, features = ["static-engine"]`. A `hegel.toml` at the repository root keeps proptest's 256 cases a property, and `HEGEL_TEST_CASES` overrides it. Principle 10 now says when a younger test-only crate is acceptable.

## Why Hegel

- Its engine is Hypothesis's, which reduces a failure through the choices that produced it rather than through each value's own shrinker. A generator built from other generators shrinks with no shrinking code of its own, and it tends to reach the smallest case rather than merely a smaller one.
- A generator is a function that draws. `#[hegel::composite]` and `tc.draw` let one draw depend on another, such as a position chosen within a list already drawn, as plain Rust. In `proptest` each dependency is a `prop_flat_map`, or one of the two variants its documentation offers for different shrinking trade-offs, so a chain of dependent draws becomes a chain of combinators. The suites planned next need such chains.
- A test draws inside its body, so a property reads as ordinary Rust, with `assert!` in place of `prop_assert!` inside a macro.
- Failures are saved and replayed first on the next run, and a failing run prints a `#[hegel::reproduce_failure]` line.

## Why `static-engine`

The engine is a Rust crate, `hegeltest-c`. By default, `hegeltest`'s build script (`build.rs` in hegeltest 0.52.0) builds it as a shared library: it writes a shim crate with its own `[workspace]` and no lockfile into `OUT_DIR`, then runs a nested `cargo build` on it. That build is outside our `Cargo.lock`, so its dependency versions are resolved afresh, unpinned, on every clean build. It is passed neither `--locked` nor `--offline`, so it may need the network, which principle 11 forbids, and `cargo deny` never sees its graph.

With `static-engine`, `hegeltest-c` is an ordinary dependency, pinned in `Cargo.lock` with everything it uses, and the build script skips the engine build. `hegeltest-c` 0.50.0 has no build script and spawns no process. Both workspaces build with `cargo build --locked --offline --all-targets`.

The cost is the one hegeltest's documentation names: the engine's dependencies join our graph, where cargo unifies their features with ours, and a new trait implementation can change type inference in unrelated code. What ships is unaffected: `cargo tree -e normal` is unchanged in both workspaces. Every test target builds and passes `clippy -D warnings`. A future `hegeltest` upgrade must check the same.

## The risk, and what contains it

A breaking release can change the API under the property suites. That is contained as follows.

- It is a dev-dependency only. Nothing that ships links it, so a defect in it can fail a test but cannot reach an operator, and `cargo tree -e normal` shows no change.
- The version requirement is `0.52`. Cargo treats 0.52.x releases as compatible and `Cargo.lock` pins 0.52.0, so even a patch release arrives only through a lockfile change that passes the gate.
- Each suite's generators are a few functions in its own test file, and its properties draw from them and use the standard assertions. Moving to another library means rewriting those functions and the draws, not the assertions.
- Its license, and those of its new dependencies, are MIT, Apache-2.0, or Zlib, all already allowed by `deny.toml`. The gate's `cargo deny` sets `exclude-dev = true` and so never checks them, but a run with dev-dependencies included passes in both workspaces. An upgrade must repeat that run.

## Evidence

The four suites were ported with the same properties and names, and the same input ranges. To confirm that each property still detects what it is for, each of the twelve invariants was broken in the code under test in turn, and every property failed with a shrunk counterexample. For example, with `compare` ordering npm versions as text, `semver_order_is_numeric_and_not_textual` reported the smallest pair that tells text from numbers:

```text
let x = (2, 0, 0);
let y = (10, 0, 0);
assertion `left == right` failed
  left: Some(Greater)
 right: Some(Less)
```

## Options rejected

**Stay on `proptest`.** It is mature and works for the current properties. The next suites rely on dependent draws, which it expresses only through flat-map combinators, and its shrinking is written per strategy rather than shared by every generator.

**`quickcheck`.** Mature, but generation and shrinking are per type through `Arbitrary`, so a dependent draw needs a hand-written type and shrinker.

**`hegeltest` with its default engine build.** Rejected for the nested, unlocked build described above.

## Consequences

- Each package that runs Hegel tests keeps a `.hegel/` directory of saved failures, which is ignored.
- A test with an explicit `test_cases` attribute is not changed by `HEGEL_TEST_CASES`, so suites set case counts in `hegel.toml` instead.
- On a CI server, Hegel's `ci` profile derandomizes runs and turns the saved failures off.
