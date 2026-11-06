# AGENTS.md

@AGENTS.local.md

## Purpose

- Almanac is a Rust library and CLI for building almanacs: self-contained binaries of event-driven workflows, deterministic up to the point of inference.
- The goal is a ready-to-run almanac that gets every Renovate pull request across the operator's repositories into a mergeable state. Success is defined in `docs/prd.md` under "Done when".
- Everything must work locally against fakes and a simulated forge. No live service, model, or network is needed to build or test.

## Repo map

```text
crates/almanac/       the library: taint, Cx, agents, effects, workflows, runtime, store, config, app
crates/macros/        #[workflow] and #[prompt]
crates/cli/           the `almanac` scaffolding binary and its templates
crates/sandbox/       the Sandbox provider: rootless Podman and a fake
crates/claude/        a Harness on the Claude Code CLI, its sandbox tools, and a stub model
crates/registry/      publish times and package diffs, read as data
crates/github/        event types generated from specs/github by tools/gen_github.py
crates/simforge/      a local GitHub for tests: HTTP API, scenarios, signed webhooks, failure injection
crates/definitions/   sample workflows and the behaviour tests
crates/fails/         trybuild compile-fail cases; these prove the safety rules
almanacs/              standalone almanacs, each its own Cargo workspace
images/               sandbox images
specs/                pinned API specs for sources
spikes/               throwaway proofs behind decisions in docs/adrs
tools/                the throwaway generator and the publication check
docs/                 vision, principles, requirements, roadmap
```

## How to work here

- **Set up:** `mise install`, then `hk install --mise`. If a global `core.hooksPath` is set, first run `git config --local core.hooksPath .git/hooks`.
- **The gate:** `make check` runs lint, every test, the publication check, and `cargo deny`. A change is not done until it passes.
- **Regenerate event types:** `make generate`.
- **Change a query or migration:** run `make sqlx-prepare` with `DATABASE_URL` pointing at a migrated database, and commit `crates/almanac/.sqlx`. Builds use that metadata and need no database.
- **Slower tests:** `make test-db` and `make test-sandbox` need podman, and the second needs the `claude` CLI. Neither calls a model. `make check` runs both.
- **Accept compile-fail output:** `TRYBUILD=overwrite cargo test -p fails`, then read every changed `.stderr`. A case must fail for its intended reason, not for an unrelated error.
- **Try an almanac:** `cargo run -- synth` or `cargo run -- ingest KIND FILE` inside `almanacs/renovate`.

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

- `almanacs/renovate/src/workflows/dependency_updates.rs`: what authoring looks like.
- `crates/almanac/src/taint.rs`: the taint rules, and `crates/fails/tests/ui/` for what they refuse.
- `crates/almanac/src/runtime.rs`: routing, limits, and replay. `crates/almanac/src/store/` holds dedupe, keys, and the run log.
- `crates/macros/src/lib.rs`: what `#[workflow]` and `#[prompt]` generate.
- `docs/prd.md`: numbered requirements. Nodes cite them as `R-n`.
