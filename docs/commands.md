# Commands

## `almanac`

The scaffolding command, built with `cargo build --release --package almanac-cli`.

| Command | What it does |
|---|---|
| `almanac new NAME` | Creates an almanac in a new directory. It depends on your clone by path. |
| `almanac generate workflow NAME` | Adds a workflow to the almanac in the current directory. |
| `almanac generate prompt NAME` | Adds a prompt file and its struct. |
| `almanac generate source NAME` | Adds a projection for a new event source. |
| `almanac explain CODE` | Prints the long form of an error. |

## An almanac binary

Each almanac, such as `almanac-renovate`, has these. Put `--format json` first for output that a program can read, and `--help` prints the usage.

| Command | What it does |
|---|---|
| `synth` | Prints the manifest. Needs nothing set up. |
| `check` | Confirms that every required provider exists, and prints the limits. |
| `run` | Runs as a daemon: consumes the broker's log, scans on a schedule, and answers health checks. |
| `backfill [--repo OWNER/NAME] [--dry-run]` | Runs each workflow's scan once. A repository the credentials cannot see is an error. |
| `simulate KIND FILE [--dry-run]` | Sends a JSON file through the production path as an event of that kind. |
| `explain CODE` | Prints the long form of an error. |

`backfill` and `simulate` are safe beside a running daemon. Only `run` recovers interrupted work, when it starts.

`almanac-renovate` adds:

| Command | What it does |
|---|---|
| `report` | Prints the state of every open Renovate pull request. See [Report states](report-states.md). |

## Exit status

Commands exit `0` on success and `1` on an error. `simulate` exits `0` when an event starts nothing, and says why.

## A Go almanac binary

The Go rewrite ([ADR 2](adrs/0002-go-and-a-workflow-model.md)) builds each almanac binary on the `app` package, so every Go almanac has the same verbs. `--format json` may go anywhere on the line, and `--help` after a verb prints that verb's flags.

| Command | What it does |
|---|---|
| `synth` | Prints each workflow's manifest, with the digest of its model. Reads no config and sets nothing up. |
| `check` | Confirms that every provider a workflow needs is set up, and prints the mode and the limits in force. With `DATABASE_URL` set, it also reaches the database and brings its schema up to date. |
| `run [--dry-run]` | Runs as a daemon: follows the deliveries log, scans on a schedule, and answers `GET /healthz` on `ALMANAC_LISTEN`. |
| `backfill [--repo OWNER/NAME] [--dry-run]` | Runs each workflow's scan once, then runs what it found. A repository the credentials cannot see is an error. |
| `simulate KIND FILE [--dry-run]` | Sends a JSON file through the production path as an event of that kind, then runs it. |
| `explain CODE` | Prints the long form of an error. Pages for codes only the Go almanac has are in [`errors-go/`](errors-go/). |

An almanac adds verbs of its own after these, and `--help` lists them. The Go `almanac-renovate` adds:

| Command | What it does |
|---|---|
| `report` | Prints the state of every open Renovate pull request. See [Report states](report-states.md). |
| `compare --rust FILE [--go FILE]` | Prints the Rust and Go reports' states side by side, marking every difference. Without `--go`, the Go report is made now. |
| `compare-effects [--since DURATION]` | Prints what the Go dry run would do to each open Renovate pull request beside what the Rust version did, as GitHub shows it. Reads the dry run's runs from the database `DATABASE_URL` names. |

[Compare the Go and Rust versions](comparing.md) says how to run and read both comparisons.

Every command starts by checking each workflow model; a binary whose model the checker refuses prints every problem and exits `1`. Every command but `synth` and `explain` then reads config, sets up providers, and refuses to go on if a provider a workflow needs is missing. Effects apply only with `ALMANAC_MODE=live`, `--dry-run` always wins over it, and `ALMANAC_READ_ONLY` makes every command a dry run that writes nothing and calls no model. See [Configuration](configuration.md#the-go-almanac).

`backfill` and `simulate` keep their state in the database `DATABASE_URL` names, or in memory without one. They are safe beside a running daemon. Only `run` recovers interrupted work, when it starts.

### Output

Text is for people. With `--format json`, each verb prints one line of JSON:

| Command | JSON |
|---|---|
| `synth` | An array of manifests: `workflow`, `intent`, `on`, `scans`, `runs_per_hour`, `may`, `agents`, `requires`, and `digest` for a model. |
| `check` | `{"almanac", "ok": true, "workflows", "mode", "read_only", "limits": {"max_chain_depth", "runs_per_hour_cap", "overall_runs_per_hour_cap", "concurrency", "scan_interval_minutes"}}`. A cap with no bound is `null`. |
| `backfill` | `{"backfill": {"found", "queued", "unchanged", "rejected", "skipped"}, "held", "outcomes"}`. |
| `simulate` | `{"simulate", "why", "outcomes"}`. `simulate` is what became of the event, such as `queued`, `unrouted`, `rejected` or `ignored`, and `why` says why it started nothing, or is `null`. |
| `explain` | `{"code", "page"}`. |

Each outcome is `{"result", "run_id", "workflow", "reason", "denied", "held", "recorded"}`, with empty fields left out. `result` is `ran`, `stopped`, `failed` or `budget-tripped`, and `recorded` is where each effect a run recorded is in delivery: `would-apply` in a dry run, and `pending`, `applied` or `failed` once live.

An error goes to stderr. As text it is the code, what happened, why it matters and what to do next. With `--format json` it is one object per line, with `code`, `message`, `why` and `help`; a binary that refuses several problems at once prints one line for each.

### Exit status

Every command exits `0` on success and `1` on an error, including a command line that is not one (`almanac::usage`). `--help` exits `0`. `simulate` exits `0` when an event starts nothing, and says why. `run` exits `0` when it is asked to stop, by SIGTERM or SIGINT.
