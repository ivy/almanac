# 2. Rewrite in Go, with workflows as a checked model built by a Go DSL

## Status

Accepted for the `go-rewrite` branch. The Rust implementation stays in the tree and in service until the operator decides on a cutover.

## Context

The ideas in Almanac hold up, but the Rust implementation is hard to read without a lot of familiarity, and it hides what an automation is for. A workflow mixes plumbing with intent: JSON digging, `Untrusted` mapping, `?` and `.clone()` sit beside the few lines that decide what happens. Two spikes, `spike/workflows-in-lua` and `spike/workflows-in-fennel`, ported `build_failures` to embedded languages that ran `gather` and `decide` as scripts. Both recommended against it. The reads and taint handling had to stay in the host, the host grew to about 2,000 lines, and about half of the safety rules moved from build time to run time.

The rest of the operator's tooling is Go, on PostgreSQL through pgx, with Ginkgo specs and mise-pinned tools.

## Decision

### Go, on the operator's usual stack

| Concern | Choice |
|---|---|
| Language | Go 1.25 |
| Database | PostgreSQL through `pgx/v5`, migrations with `goose`, queries with `sqlc` |
| CLI | `cobra` |
| Config | `BurntSushi/toml`, then `ALMANAC_*` variables |
| Logging | `log/slog`, JSON in the daemon |
| Tests | Ginkgo and Gomega, `testcontainers-go` on Podman |
| Lint | `golangci-lint`, with `depguard` and `forbidigo` carrying design rules |
| Tooling | `mise`, `hk`, one `Makefile` gate |

One Go module, `github.com/ivy/almanac`, at the repository root. Library packages are public so an almanac can import them. The Renovate almanac is in the same module under `almanacs/renovate`, and shares its `prompts/` and `fixtures/` with the Rust almanac while both exist.

### A workflow is a model

The source of truth for a workflow is a **model**: a Go value of type `model.Workflow`, with a canonical JSON form whose digest names a version. It declares the trigger, filter, key, limits, allowed effects, scan and facts, then lists steps. A step observes, stops, branches, measures a release's age, applies an effect partway through a run, calls an agent, or decides with ordered rules. A rule's condition is a tree of named predicates. There is no expression language and no code in the model.

What a model refers to is the **vocabulary**: events and their parsing, observers, predicates, narrowing functions, maps of hostile values, agent profiles and prompts, effect kinds, scans, and limit ceilings. Vocabulary is Go, compiled in, and registered by name with a label a person can read and a signature that says which facts it reads and returns and whether they are hostile. A model can only combine the vocabulary, so it cannot widen anything the binary declares (principle 2).

Workflows are written in a **Go DSL** whose combinators build the model. Typed fact handles (`Fact[T]` and `Hostile[T]`) let the Go compiler refuse a fact of the wrong type or a hostile fact in a condition before the checker runs.

One **checker** runs on every model at start-up and in `make check`. It refuses a model that names unknown vocabulary, reads a fact before observing it, uses an effect not in `may`, puts a hostile fact anywhere but a narrowing predicate, `showing` or an agent's input, builds a title from anything but a literal or clean fact, gives hostile input to an agent whose profile has network or secrets, pushes anything but the patch policy's output, or leaves a `decide` without an ending. Almanac's compile-fail cases become checker cases and Go type-check cases.

An **engine** interprets a model as a workflow. It implements the same seam the Rust runtime uses (`Registered`: identify, execute, scan, manifest), so the runtime does not know that workflows are models. Every observation, age check, attempts count, hold and agent call goes through `Cx` and is recorded by name and occurrence, so a replay re-walks the model against what was recorded.

Building a model, rather than running scripts, is what makes the views possible: the flow, every way a run can end, and the difference between two versions are all walks over data. It also keeps the door open for other front ends (Lua, Fennel) that build the same model. They are not part of this rewrite.

### Taint

Hostile values are `hostile.Of[T]`, with no exported field and no accessor. `hostile.Map` keeps taint. `hostile.Narrow` returns `bool`, and `hostile.Classify` returns only a type in a closed set, marked by embedding `hostile.Closed`. Titles are `hostile.Title`, built from literals and clean values; a model holds a title's format as a `hostile.Format`, which is made only from a string constant, as `Titlef`'s format is. Recorded reads and agent answers are JSON: `hostile.Decode` keeps an answer hostile under a type, and `hostile.DecodeClean` lets it out only into a type `hostile.CheckClean` accepts, one that holds numbers, bools, validated text and closed sets. A `go vet` analyzer, `tools/hostilevet`, runs in `make go-lint`. It refuses formatting a hostile value, a narrowing function that is not a declared function or that formats or stores the text it reads, and a closed value outside its declared set. It also keeps `hostile/reveal`, the one way out of taint, to the boundary packages it lists, which hand text to the outside: a forge's API, an issue quoted as inert text, the patch policy. Hostile text is never a plain Go `string` outside those packages.

### The runtime keeps its semantics

Dedupe by delivery, one active run per key with at most one rerun, chain depth with echo inference, budgets over real time windows, lanes and concurrency, the outbox with exactly-once application, replay, recovery, scans and backfill all behave as they do in Rust. The Rust tests named in `docs/coverage.md` are the behavioural specification. Each is ported to a Ginkgo spec with the same scenario.

Effects are applied only in `live` mode. A dry run stores effects as `would-apply`, never as `pending`, so delivery can never pick them up, and `--dry-run` always wins over the configured mode. `ALMANAC_READ_ONLY=1` refuses every write and every model call.

### What runs beside the Rust Almanac

The Go binaries take their own ports (`127.0.0.1:8789` for the broker, `127.0.0.1:8790` for the daemon's health) and their own database, so both can be installed at once. Until the operator decides on a cutover, the Go almanac runs read-only, from scans, and acts on nothing. `compare` puts its report beside the Rust report for the same pull requests.

### Processes

`os/exec` is allowed only in `sandbox/podman` and `harness/claude`, enforced by `forbidigo`. The agent loop still runs outside the sandbox, as [ADR 1](0001-agent-sandbox.md) decided.

## Consequences

- The compiler enforces less than `rustc` did. The checker, the typed fact handles and the analyzer carry the rest, and each refusal is a test.
- Every rule fires before an event arrives, because a model exists in full once the binary starts. That is the difference from the script spikes, where rules fired when a run reached their line.
- The vocabulary must carry labels a person can read, because the views and the manifest are only as clear as those labels.
- Two implementations exist until cutover. The Rust one is not changed by this branch, so the deployed service is unaffected.

## Options rejected

**A Go port that keeps imperative `gather` and `decide`.** It is the smallest change, and it keeps the readability problem: a run's logic is spread across early returns, and nothing can list a workflow's outcomes without running it.

**Lua or Fennel as the authoring language.** The spikes show the trade does not pay while the workflow runs as a script. As front ends that only build a model, they remain open, and nothing here rules them out.

**YAML or another configuration language.** It moves conditions into strings that no compiler sees, and principle 2 rules it out.
