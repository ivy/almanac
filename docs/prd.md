# Product Requirements

The requirements for Almanac v0. [vision.md](vision.md) says why it exists and [core-principles.md](core-principles.md) holds the decision filters. Requirements are numbered so work can cite them, and [coverage.md](coverage.md) says which test proves each.

## Goal

A ready-to-run almanac, `almanacs/renovate`, that gets every open Renovate pull request across all of the operator's repositories into a mergeable state, proven end to end against a simulated forge.

A pull request is **mergeable** when all of these hold: it has no merge conflict, its required and reported checks pass, and any `almanac/trust` status it carries is `success`. Renovate then merges it wherever the repository's configuration allows.

## The first almanac

Renovate proposes updates and merges them. Almanac repairs what is broken and validates what is urgent. Routine healthy updates need no Almanac involvement. Dependabot is out of scope.

### Fleet coverage

- **R-47** The almanac covers every non-archived repository owned by `ivy` with an open Renovate pull request, discovered at run time rather than listed by hand. Renovate pull requests are those authored by `ivy-s-renovate-bot[bot]`.
- **R-48** Every open Renovate pull request ends each scan with exactly one state: `mergeable`, `in-progress`, `waiting` (with what for), `needs-person` (with why), or `blocked-by-config` (with the setting to change).
- **R-49** `report` prints that state for every pull request, grouped by repository, as text or JSON. A repository whose Renovate configuration cannot automerge is reported, and its pull requests still count as mergeable when they meet the definition above.
- **R-50** A pull request with a merge conflict is not repaired by an agent while Renovate can rebase it. A conflict on a branch carrying an Almanac patch is reported as `needs-person`.
- **R-51** A repository with no CI reports no failures, so it is never repaired, and it is reported as having no checks.

### Workflow: build failures

Repairs a Renovate pull request whose CI failed, after the update's cooldown has passed.

- **R-1** It triggers when CI finishes red on a `renovate/` branch: `check_suite.completed` with conclusion `failure`, or a `status` event with state `failure` or `error`.
- **R-2** No agent reads a pull request until the update has passed its cooldown. Almanac verifies this itself from the registry's publish time, using a minimum release age it declares. A `renovate/stability-days` status is not sufficient evidence.
- **R-3** A cooldown that has not passed produces `Await`, and the condition is reconsidered later by a scan.
- **R-4** A fix agent receives the failure logs and the checkout as tainted input and returns a patch. It does not push.
- **R-5** A patch must pass the patch policy (R-20), then Almanac pushes it to the pull request's branch.
- **R-6** Attempts per key are capped. When the cap is reached the workflow asks a person.
- **R-7** A patched pull request carries an `almanac/trust` status from the time the agent starts until checks finish, so Renovate cannot merge it midway.

### Workflow: security fixes

Validates a Renovate security pull request and gets its build passing.

- **R-8** It triggers on a `pull_request` `opened` or `labeled` event carrying the `security` label.
- **R-9** On trigger it posts `almanac/trust` as `pending` before any other work, because a security pull request skips the cooldown and an absent status holds nothing.
- **R-10** It checks deterministically that an advisory exists for the package, that the current version is affected, and that the pull request's target is the advisory's patched version.
- **R-11** It checks that the target release is older than the declared cooldown. A younger release asks a person.
- **R-12** A quarantined reviewer inspects the package diff from the registry. It can veto and never approve (principle 4).
- **R-13** All checks passing posts `almanac/trust` as `success`, and a failing check posts `failure` and asks a person.
- **R-14** If CI is red, repair follows the same route as R-4 to R-6.

## The library

### Authoring

- **R-15** A workflow is an async function under `#[workflow(...)]`, declaring its trigger, key, intent, allowed effects, budget, subjects, and policy. See `almanacs/renovate`.
- **R-16** `gather` does I/O and inference through `Cx`. Policy is a plain function of gathered facts.
- **R-17** Prompts are Markdown files in `prompts/`, bound to a struct with `#[prompt("file.md")]`, embedded at compile time, and checked against the struct at compile time.
- **R-18** Agents are `const` specs carrying a capability profile and a prompt type. Agents and providers used in a workflow body are discovered by the macro for the manifest and the startup check.

### Safety

- **R-19** Agent profiles are `Quarantined`, `Fixer`, and `Worker`. A `Fixer` accepts tainted input, holds no secrets, and has no general network access. Its output is a patch, which stays tainted.
- **R-20** The patch policy is deterministic and rejects a patch that changes anything under `.github/`, Renovate configuration, or `mise.toml`, that holds a binary file, that exceeds a size limit, or that does not apply cleanly. The rules are data the workflow declares.
- **R-55** A tracker shows a title or question as it is, so each is a `Title`, built only by `title!` from a literal and `Clean` values. Hostile text goes in the body, which stays `Untrusted` to the adapter.
- **R-21** `Effect::Push` is the only way a patch reaches a branch. It targets the pull request's own branch only. There is no merge effect.
- **R-22** A `Cooled` proof value can be obtained only by a check against a registry's publish time, and an agent call that requires it does not compile without one.
- **R-23** Capabilities are enforced by a sandbox: code run for a `Fixer` cannot reach the network at all, and cannot read host credentials. The agent loop runs outside the sandbox and its only tools are sandbox calls. See [ADR 1](adrs/0001-agent-sandbox.md).
- **R-52** `Sandbox` is a provider with a remote-shaped interface: create from a spec, execute a command, upload and download files, destroy. It assumes no host path or bind mount, so rootless Podman and a hosted service such as Daytona both fit. Egress is declared as none or a list of allowed domains.

### Coordination

- **R-24** Events are deduplicated by source and delivery ID, durably, and marked seen only once persisted.
- **R-25** One run is active per workflow and key. Events during a run cause at most one rerun.
- **R-26** Chains are contained by causation depth. Almanac infers its own echoes from the event's actor and a ledger of its recent effects, because webhooks do not carry causation.
- **R-27** Budgets use a real time window, per workflow and overall. A tripped budget holds work and asks a person once.
- **R-28** Runs for independent keys execute concurrently up to a limit. Scanned events run in a lower-priority lane than webhooks, so a backfill cannot starve a live security event.

### Entry paths

- **R-29** The broker is the single webhook ingress for every almanac. It verifies the signature in constant time, appends the raw delivery to a shared log, and acknowledges within GitHub's 10-second limit. It knows nothing about workflows, so adding one never touches GitHub.
- **R-53** Each almanac consumes the log through its own durable cursor, woken by `LISTEN/NOTIFY` with polling as a fallback. Delivery to an almanac is at-least-once, an almanac that was down catches up from its cursor, and it ignores kinds it has no workflow for.
- **R-54** An almanac runs as a long-lived daemon with `run`: graceful shutdown, health reporting, and systemd readiness, suitable for a lingering user service that can start rootless Podman.
- **R-30** A workflow may declare `scan = ...`, a `#[scan]` function that lists current subjects and returns the same event type as its trigger. What it needs is in the manifest and checked at startup, like what the workflow needs.
- **R-31** `backfill` runs a workflow's scan once. Scanned events carry stable delivery IDs such as `scan:ivy/thicket#159@<sha>`, so rerunning is idempotent. `--repo` narrows it to one repository, and naming one the scan cannot see is an error.
- **R-32** `simulate` injects a fixture through the production path.
- **R-33** Every event records its origin: `webhook`, `scan`, or `simulated`.
- **R-34** Effects are applied in `live` or `dry-run` mode, independently of an event's origin. A dry run records what would have happened, in state of its own: a live run on the same database still has everything to do. Agents still run, so a dry run keeps its budgets.
- **R-35** A scheduled scan catches up on lost deliveries, because GitHub does not redeliver failures.
- **R-36** `gather` reads current state rather than trusting a payload, so all origins behave the same.

### Integrations

- **R-37** Event types are generated by a projection: a declared list of field paths checked against a pinned spec in `specs/`. A string is `Untrusted` unless the projection names a validated type.
- **R-38** Generated enums tolerate unknown values.
- **R-39** A source with no spec uses a hand-written `observed!` type and `Payload`, which cannot produce a bare `String`.
- **R-40** Providers are traits, supplied once in `providers.rs` and checked at startup. Harnesses and classifiers are swappable.
- **R-41** A `Tracker` provider receives `File` and `Ask` effects. v0 ships a GitHub Issues tracker and a local fake.

### Operating

- **R-42** Errors use `miette` with stable codes, help text, and source spans for config. `--format json` renders them for agents, and `almanac explain CODE` prints the long form.
- **R-43** `synth` prints a manifest of triggers, effects, budgets, agents, grants, prompts, and required providers.
- **R-44** Config comes from a file then `ALMANAC_*` variables, and can only tighten limits. Secrets come from the environment.
- **R-45** State is in PostgreSQL through `DATABASE_URL`, shared by the almanacs of one deployment. In-memory storage remains for tests.
- **R-46** `almanac new` and `almanac generate` scaffold an almanac, a workflow, a prompt, and a source.

## Stack

| Concern | Choice | Why |
|---|---|---|
| Language | Rust, 2024 edition, stable | The type system enforces taint and capabilities |
| Database | PostgreSQL with `sqlx` | Mature, with compile-time checked queries and built-in migrations |
| HTTP server | `axum` | The mature default on `tokio` |
| HTTP client | `reqwest` with rustls | The mature default |
| Errors | `miette`, `thiserror` | Rich diagnostics with a JSON renderer |
| CLI | `clap` derive | Mature |
| Logging | `tracing` | Structured, with JSON output |
| Event transport | The shared PostgreSQL log, with `LISTEN/NOTIFY` | One operator's volume needs no message broker product |
| Sandbox | Rootless Podman, behind a provider | Needs no root on a single host; Daytona can replace it |
| Webhook signatures | `hmac`, `sha2` | RustCrypto, constant-time comparison |
| Tests | `cargo-nextest`, `insta`, `trybuild`, `proptest`, `testcontainers` on podman | Snapshot, compile-fail, property, and real-database tests |
| Code generation | An `xtask` | Matches `ivy/nwscript` |
| Tooling | `mise`, `hk`, `cargo-deny`, `clippy`, `rustfmt`, a `Makefile` | Pinned tools, local hooks, and one gate |

## Out of scope for v0

Integration with an external tracker or worker, Dependabot, advisory-driven blast-radius scanning, an IDS, a web console, a Daytona sandbox implementation, and multi-host deployment. [roadmap.md](roadmap.md) says where each belongs.

## Done when

- `make check` passes from a clean checkout with no network beyond dependency download and no live service.
- An end-to-end suite runs both workflows against the simulated forge and PostgreSQL, covering delivery failure, duplicates, chain reactions, and budget exhaustion.
- `backfill --dry-run` runs read-only across all of the operator's repositories and `report` gives one state for every open Renovate pull request.
- An evaluation records whether a sandboxed agent can repair real failures from those pull requests, as patches that are not pushed.
- A runbook lets the operator deploy with no further design work.
