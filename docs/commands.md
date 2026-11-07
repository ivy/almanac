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
