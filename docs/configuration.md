# Configuration and limits

Settings come from `almanac.toml`, then `ALMANAC_*` variables, which win. Configuration can only tighten what the binary declares, and secrets come from the environment only: a secret in the file is refused.

The sections up to [The Go almanac](#the-go-almanac) describe the Rust binaries. That section says where the Go binaries of the rewrite differ.

## `almanac.toml`

| Key | Variable | Default | Meaning |
|---|---|---|---|
| `listen` | `ALMANAC_LISTEN` | `127.0.0.1:8788` | Where the daemon answers `/healthz`. |
| `max_chain_depth` | `ALMANAC_MAX_CHAIN_DEPTH` | 3 | How many events can follow from one another. Can only be lowered. |
| `runs_per_hour_cap` | `ALMANAC_RUNS_PER_HOUR_CAP` | none | Lowers each workflow's own budget. |
| `overall_runs_per_hour_cap` | `ALMANAC_OVERALL_RUNS_PER_HOUR_CAP` | none | Caps all workflows together. |
| `concurrency` | `ALMANAC_CONCURRENCY` | 4 | Runs at once. Can only be lowered. |
| `scan_interval_minutes` | `ALMANAC_SCAN_INTERVAL_MINUTES` | 15 | How often scans run. Can only be lengthened. |

`ALMANAC_CONFIG` names the file, and defaults to `almanac.toml`.

## Environment

### Any almanac

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | PostgreSQL. Without it, state is held in memory. |
| `ALMANAC_MODE` | `live` applies effects. Anything else is a dry run. |

### `almanac-renovate`

| Variable | Required | Meaning |
|---|---|---|
| `ALMANAC_OWNER` | Yes | The account to scan. |
| `ALMANAC_RENOVATE_AUTHOR` | Yes | The login that opens your Renovate pull requests. |
| `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, `GITHUB_APP_PRIVATE_KEY` | One of these or `GITHUB_TOKEN` | A GitHub App. Required to run live. |
| `GITHUB_TOKEN` | See above | A token, for read-only use. |
| `ANTHROPIC_API_KEY` | One of these to repair | A model API key. Used in preference to a token. |
| `CLAUDE_CODE_OAUTH_TOKEN` | See above | A token from `claude setup-token`. |
| `ALMANAC_BOT_LOGIN` | To run live | Your App's login, so Almanac recognises its own actions. |
| `ALMANAC_ASK` | No | Who is mentioned and assigned when a person is needed. Defaults to `ALMANAC_OWNER`. |
| `ALMANAC_ALERTS_REPO` | No | Where a tripped budget files an issue. |
| `ALMANAC_READ_ONLY` | No | `1` refuses every write and every model call. |
| `ALMANAC_SANDBOX_IMAGE` | No | Defaults to `localhost/almanac-fixer:latest`. |
| `ALMANAC_REGISTRY_URL`, `ALMANAC_ADVISORIES_URL`, `GITHUB_API_URL`, `ANTHROPIC_BASE_URL` | No | Point at a mirror or a test double. |

### The broker

| Variable | Required | Meaning |
|---|---|---|
| `ALMANAC_WEBHOOK_SECRET` | Yes | The secret set on the GitHub App. |
| `DATABASE_URL` | Yes | The same database as the almanacs. |
| `ALMANAC_BROKER_LISTEN` | No | Defaults to `127.0.0.1:8787`. |

## Fixed limits in `almanac-renovate`

| Limit | Value |
|---|---|
| Runs per hour, each workflow | 20 |
| Repair attempts per pull request | 3 |
| Spend per agent session | $6 |
| Minimum release age before an agent reads an update | 72 hours |
| Time CI gets to report on a security update before a person is asked | 1 hour |
| Attempts to apply an effect | 5 |

A tripped budget holds its work, which resumes as the hourly window opens.

## The Go almanac

The Go binaries read the same `almanac.toml` keys and `ALMANAC_*` variables, with the same defaults, and every limit can still only tighten. A Go binary may declare its own limits, which config then tightens. These differ:

| Setting | Go |
|---|---|
| `listen`, `ALMANAC_LISTEN` | Defaults to `127.0.0.1:8790`. It must be a loopback IP address and port, such as `127.0.0.1:8790` or `[::1]:8790`; anything else, `localhost` included, is refused with `almanac::config::listen`, as is a port another process holds. |
| `ALMANAC_BROKER_LISTEN` | Defaults to `127.0.0.1:8789`, and is loopback only in the same way. |
| `ALMANAC_READ_ONLY` | Anything except unset, empty or `0` is read-only, so a typo can only make an almanac do less. A read-only command is a dry run whatever `ALMANAC_MODE` says, refuses every write to a forge, tracker or sweep, and calls no classifier or agent: a run that reaches one fails there, recording how far it got. It still keeps its own rows, under the dry-run scope. |
| `ALMANAC_MODE` | `live` applies effects, and anything else is a dry run, as before. `--dry-run` always wins, on `run`, `backfill` and `simulate`. |
| `DATABASE_URL` | Required by `run`. `backfill`, `simulate` and `check` use it when it is set, and `backfill` and `simulate` hold state in memory without it. The broker and every almanac share one database. |
| `ALMANAC_WEBHOOK_SECRET`, `DATABASE_URL` for the broker | An empty value is refused like an unset one. |

An almanac keeps its rows, and its cursor in the deliveries log, under its binary's name, such as `almanac-renovate`. A dry run keeps them under the name with `.dry-run` after it, so that what a dry run has seen never stands in for work a live run still has to do. A new cursor starts at the end of the log: history is `backfill`'s job.

The daemon asks every 30 seconds whether a scheduled scan is due, and scans once `scan_interval_minutes` has passed since the last. A run under way when the daemon is asked to stop gets 30 seconds to finish; one still going then is requeued when the daemon next starts.
