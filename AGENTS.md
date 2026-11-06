# AGENTS.md

@AGENTS.local.md

## Purpose

- Almanac is a Rust library and CLI for building almanacs: self-contained binaries of event-driven workflows, deterministic up to the point of inference.
- The goal is a ready-to-run almanac that gets every Renovate pull request across the operator's repositories into a mergeable state. Success is defined in `docs/prd.md` under "Done when".
- Everything must work locally against fakes and a simulated forge. No live service, model, or network is needed to build or test.

## Repo map

```text
crates/almanac/       the library: taint, Cx, agents, effects, workflows, runtime, config, app
crates/macros/        #[workflow] and #[prompt]
crates/cli/           the `almanac` scaffolding binary and its templates
crates/github/        event types generated from specs/github by tools/gen_github.py
crates/definitions/   sample workflows and the behaviour tests
crates/fails/         trybuild compile-fail cases; these prove the safety rules
examples/             standalone almanacs, each its own Cargo workspace
specs/                pinned API specs for sources
tools/                the throwaway generator and the publication check
docs/                 vision, principles, requirements, roadmap
```

## How to work here

- **Test everything:** `cargo test --workspace`, then `cargo test` inside `examples/renovate-triage`, which is outside the workspace.
- **Lint:** `cargo clippy --workspace --all-targets`. It must report no warnings.
- **Regenerate event types:** `python3 -I tools/gen_github.py specs/github/api.github.com.json crates/github/projection.toml > crates/github/src/lib.rs`.
- **Accept compile-fail output:** `TRYBUILD=overwrite cargo test -p fails`, then read every changed `.stderr`. A case must fail for its intended reason, not for an unrelated error.
- **Try an almanac:** `cargo run -- synth` or `cargo run -- ingest KIND FILE` inside `examples/renovate-triage`.

When a `Makefile` exists, `make check` is the canonical gate and takes precedence over the commands above.

## Working in the open

This repository is public, and everything pushed to it stays public.

- Commit small, coherent changes to `main` with conventional commit messages, and push to `origin` once the gate passes. There are no pull requests.
- Never publish anything that identifies the operator's infrastructure or private work: hostnames, network names, home-directory paths, private repository names, or unreleased project names.
- Fixtures are synthetic or come from public repositories. Reports, patches, and anything else derived from a private repository go in the gitignored `private/` directory.
- `tools/check-public.sh` runs before every push. Never bypass it. It only catches what it knows about, so read what you are about to publish.
- If something private is pushed, stop and report it. Do not try to rewrite public history.
- If `docs/` is wrong, fix it in the same change. Documents state current facts.
- Record decisions you can make yourself in `docs/adrs/`.

## Change hygiene

- `docs/core-principles.md` overrides convenience. A change that violates a principle needs that principle revisited first, in writing.
- Never weaken a compile-fail case to make a build pass. Those cases are the safety rules.
- Never add a public way out of `Untrusted`, a merge effect, or a config key that widens a limit.
- Every error reachable from input follows principle 9: what happened, why it matters, what to do next.
- Prefer mature crates. Add a dependency to the workspace table, not to one crate.
- No live calls in tests. Use the fakes in `crates/almanac/src/testing.rs` and fixtures.
- Comments explain this code, not upstream libraries. Link upstream docs instead.
- Prose in Markdown is one line per paragraph.

## Where to look first

- `examples/renovate-triage/src/workflows/dependency_updates.rs`: what authoring looks like.
- `crates/almanac/src/taint.rs`: the taint rules, and `crates/fails/tests/ui/` for what they refuse.
- `crates/almanac/src/runtime.rs`: dedupe, keys, budgets, and replay.
- `crates/macros/src/lib.rs`: what `#[workflow]` and `#[prompt]` generate.
- `docs/prd.md`: numbered requirements. Nodes cite them as `R-n`.
