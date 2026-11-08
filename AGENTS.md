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
crates/broker/        the one webhook ingress: verifies, logs, acknowledges, knows no workflows
crates/deliveries/    the shared log the broker writes and almanacs read
crates/sandbox/       the Sandbox provider: rootless Podman and a fake
crates/claude/        a Harness on the Claude Code CLI, its sandbox tools, and a stub model
crates/registry/      publish times and package diffs, read as data
crates/advisory/      vulnerability advisories, and checking an update against one
crates/github/        event types generated from specs/github, and GitHub providers
crates/simforge/      a local GitHub for tests: HTTP API, scenarios, signed webhooks, failure injection
crates/definitions/   sample workflows and the runtime's behaviour tests
crates/fails/         trybuild compile-fail cases; these prove the safety rules
almanacs/              standalone almanacs, each its own Cargo workspace
images/               sandbox images
contrib/              example systemd user units
specs/                pinned API specs for sources
spikes/               throwaway proofs behind decisions in docs/adrs
tools/                the publication check
xtask/                code generation: projections checked against pinned specs
docs/                 user docs, plus vision, principles, requirements, roadmap
```

## How to work here

- **Set up:** `mise install`, then `hk install --mise`. If a global `core.hooksPath` is set, first run `git config --local core.hooksPath .git/hooks`.
- **The gate:** `make check` runs lint, every test, the publication check, and `cargo deny`. A change is not done until it passes.
- **Regenerate event types:** `make generate`.
- **Change a query or migration:** run `make sqlx-prepare` with `DATABASE_URL` pointing at a migrated database, and commit `crates/almanac/.sqlx`. Builds use that metadata and need no database.
- **Slower tests:** `make test-db`, `make test-sandbox`, and `make test-e2e` need podman, and the last two need the `claude` CLI. None calls a model. `make test-scaffold` compiles a new almanac. `make check` runs them all.
- **Accept compile-fail output:** `TRYBUILD=overwrite cargo test -p fails`, then read every changed `.stderr`. A case must fail for its intended reason, not for an unrelated error.
- **Try an almanac:** `cargo run -- synth` inside `almanacs/renovate`.

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

## Go rewrite

The `go-rewrite` branch rewrites Almanac in Go, with workflows written in a Go DSL that builds a checked model. [ADR 2](docs/adrs/0002-go-and-a-workflow-model.md) is the design. Until the cutover the Rust implementation stays in the tree, unchanged and still gated by `make check`.

- **Set up:** `mise install` adds Go, `golangci-lint`, `ginkgo` and `sqlc`.
- **The Go gate:** `make go-check` runs `go-lint`, `go-test`, `go-test-db`, `go-test-sandbox`, `go-test-e2e` and `check-public`.
- **Queries:** `store/postgres` writes its SQL in `queries.sql` and generates `internal/queries` with `sqlc`. `make go-generate` regenerates it, and `make go-lint` fails while it is stale.
- **Specs** are Ginkgo and Gomega. `testing` appears only in `*_suite_test.go`. A spec needing PostgreSQL carries the `db` label, one needing Podman or the `claude` CLI the `sandbox` label, the end-to-end suite `e2e`.
- **Processes:** only `sandbox/podman` and `harness/claude` may import `os/exec`.
- **Taint:** `tools/hostilevet` runs in `make go-lint`. Only the packages its `boundaries` list names may import `hostile/reveal`; adding one is a reviewed change.
- **GitHub's events** are generated from `github/events/projection.toml` by `tools/eventgen`. Run `make go-generate` after changing the projection, and commit `github/events/events.go`: `make go-lint` fails while it is stale.
- **Workflows** are models (`model/`) of parts registered in a vocabulary (`vocab/`), written with the DSL (`dsl/`) and refused or accepted by `check/`. A workflow's spec runs `check.Check` on it. Its model errors have pages in `docs/errors-go/` until the cutover.
- **A worktree** needs `private/denylist.txt` copied from the main checkout, or `check-public` checks machine details only.

## Where to look first

- `almanacs/renovate/src/workflows/build_failures.rs`: what authoring looks like.
- `crates/almanac/src/taint.rs`: the taint rules, and `crates/fails/tests/ui/` for what they refuse.
- `crates/almanac/src/runtime.rs`: routing, limits, and replay. `crates/almanac/src/store/` holds dedupe, keys, and the run log.
- `crates/macros/src/lib.rs`: what `#[workflow]` and `#[prompt]` generate.
- `docs/prd.md`: numbered requirements, cited as `R-n`. `docs/coverage.md` says which test proves each.
