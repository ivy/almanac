# Compare the Go and Rust versions

The Go `almanac-renovate` ([ADR 2](adrs/0002-go-and-a-workflow-model.md)) runs read-only beside the Rust one before a cutover. Two verbs set its decisions beside the Rust version's for the same pull requests:

- `compare` puts the two reports' states side by side.
- `compare-effects` puts what the Go version would do beside what the Rust version did.

`compare-effects` is the evidence that matters. On repositories the Rust version already runs on, most of the `almanac/trust` statuses, repair commits and questions GitHub shows are the Rust version's own effects, so two reports that agree mostly show that both versions read GitHub the same way. Whether the Go version would act the same is what the effects comparison shows.

## Before you start

- The Go version runs as a daemon with `ALMANAC_READ_ONLY=1`, so it writes nothing and calls no model. Its runs are kept under the dry-run scope, `almanac-renovate.dry-run`, in the database `DATABASE_URL` names. See [Configuration](configuration.md#the-go-almanac).
- Both binaries see the same repositories: the same `ALMANAC_OWNER`, `ALMANAC_RENOVATE_AUTHOR`, and `GITHUB_TOKEN` or `GITHUB_APP_` settings.
- Both binaries are called `almanac-renovate`. The commands below call the Rust build `$RUST` and the Go build `$GO`.

## Run both comparisons back to back

A report derives every state from what GitHub says now, so two reports made minutes apart can differ because a pull request moved. Make the Rust report and compare it at once:

```bash
"$RUST" --format json report > rust.json
"$GO" compare --rust rust.json
"$GO" compare-effects --since 72h
```

Without `--go FILE`, `compare` makes the Go report itself, straight after reading the Rust one. To compare two reports already on disk, pass both:

```bash
"$GO" --format json report > go.json
"$GO" compare --rust rust.json --go go.json
```

`compare-effects` reads the Go version's runs from its database, and GitHub for the open pull requests and what the Rust version did to them. `--since` compares only the Go runs started that long ago or later. Without it, every run in the database is compared. A scan runs a pull request again only when something about it changes, so a window shorter than the time the Go daemon has been running leaves out pull requests that have not changed since.

Both verbs only read. Put `--format json` anywhere on the line for the same comparison as data.

## Read `compare`

Each pull request in either report gets one line, and two more where the reports differ:

```text
agree             owner/alpha#1  mergeable  renovate/left-pad-2.x  nothing is in its way
differ            owner/beta#1  differs in state, reason
    rust: needs-person  renovate/tar-7.x  nothing says when this update was published
    go:   in-progress   renovate/tar-7.x  a repair is due
rust-in-progress  owner/beta#4  the Rust version is working on it now; not counted
    rust: in-progress  renovate/glob-10.x  Almanac is validating it
    go:   mergeable  renovate/glob-10.x  nothing is in its way
only-rust         owner/gamma#1
    rust: waiting  renovate/minimist-1.x  CI to finish
3 repositories scanned by both; 4 pull requests: 1 agree, 1 differ, 1 rust-in-progress, 1 only-rust, 0 only-go
```

| Agreement | Meaning |
|---|---|
| `agree` | The same branch, state, reason and note. |
| `differ` | Both reports list it, and differ in what `differs in` names: `branch`, `state`, `reason` or `note`. A pull request one report lists as unreadable has the state `unreadable`. |
| `rust-in-progress` | The Rust report has it `in-progress` and the Go report does not. The Rust version is working on it now, and the Go version, with no run of its own, reports what GitHub shows. It is not counted as a difference. Compare it again once the Rust run finishes. |
| `only-rust`, `only-go` | Only one report lists it. Usually the pull request opened or closed between the two reports. |

The last line says whether both reports scanned the same number of repositories. [Report states](report-states.md) says what each state and reason means.

## Read `compare-effects`

Each open Renovate pull request gets a verdict, the Go runs about it, and its actions:

```text
agree            owner/repo#1  renovate/tar-7.x  head eeeeeee, almanac/trust success
    go run run-1 (build_failures) on cd26621: stopped where an agent would run
    both       go would start a repair on cd26621  ~  rust left almanac/trust pending on cd26621; pushed repair eeeeeee onto cd26621, Almanac-Key run-7:0
disagree         owner/repo#2  renovate/npm-tar-vulnerability  head 5f0c1a2, almanac/trust absent
    go run run-2 (security_fixes) on 5f0c1a2: would act
    go only    would fail almanac/trust on 5f0c1a2
    go only    would ask a person: This update is not the fix an advisory names
2 open Renovate pull requests, against Go runs since 2026-11-09T12:00:00Z: 1 agree, 1 disagree, 0 rust-in-progress, 0 not-compared
```

| Verdict | Meaning |
|---|---|
| `agree` | Every Go action has a Rust action that answers it, and every Rust action a Go one. Two versions that both did nothing agree. |
| `disagree` | At least one `go only` or `rust only` line. |
| `rust-in-progress` | `almanac/trust` is pending on the head: the Rust version is working on it, and where its run will leave it is not on GitHub yet. Nothing unmatched on it is counted. |
| `not-compared` | No Go run in the window was about it. |

| Line | Meaning |
|---|---|
| `go run` | A Go run about the pull request, the commit its event named, and how it ended: `would act`, `decided nothing`, `stopped where an agent would run`, `stopped` or `did not finish`. |
| `both` | A Go action, and the Rust actions that answer it. |
| `go only` | Something the Go version would do that the Rust version did not. |
| `rust only` | Something the Rust version did that no Go action answers. |
| `go waits` | Something the Go version would wait for. A wait leaves nothing on GitHub to compare. |
| `unseen` | A Rust action on a commit no Go run in the window looked at, so the Go version never had the chance to do the same. |

The Rust version's actions are read from GitHub:

- `almanac/trust` on each commit a Go run looked at: `success` and `failure`, and `pending` on any commit but the head.
- Commits on the branch whose message carries an `Almanac-Key:` trailer, each on the commit it was pushed onto. `as almanac[bot]` shows when the commit's author is the one Almanac commits as.
- Comments on the pull request carrying the `<!-- almanac:KEY -->` marker Almanac's tracker puts on a question.

A Go action is answered like this:

| Go action | Answered by |
|---|---|
| would pass or fail `almanac/trust` on a commit | the same state on that commit |
| would push a repair onto a commit | a repair commit pushed onto it |
| would start a repair on a commit | a repair commit pushed onto it, or the failed or pending status a repair that came to nothing leaves on it |
| would ask the reviewer about a commit | a final status on it, or the pending one the Rust version leaves while it waits for tests |
| would ask a person | any question on the pull request |

A read-only run calls no agent. It records how far it got and stops where the fixer or the reviewer would start, so it can never push a repair or give a verdict. `compare-effects` replays each run that recorded no effect from its log, reading nothing and calling no agent, to find where it stopped, and reports that point as `would start a repair` or `would ask the reviewer`. What the agent would have done is past what a read-only run can see, so the Rust version's repair commit, verdict, and any question asked after it answer the Go run that stopped there. A missing push is never a disagreement.

## What agreement does and does not prove

Agreement shows that, for the commits it looked at, the Go version reached the same decisions as the Rust version up to the point where an agent would run, and would apply the same deterministic effects. It does not show:

- **What an agent would do.** A read-only run stops before the fixer and the reviewer. Their patches and verdicts are not compared, and a Go run that would start a repair is answered by any repair commit or failed status on that commit.
- **That the Go version counts attempts as the Rust one does.** Only a live run remembers its pushes and questions, so a dry run never reaches `repairs did not fix the build` and asks again where the Rust version asked once. Where the Rust version gave up after three repairs, the Go version would start a fourth, and that is answered by the Rust version's failed status.
- **Anything about a commit no Go run looked at.** Those Rust actions are `unseen`, and a pull request no Go run was about is `not-compared`.
- **Order in time.** A Go run that reads a pull request after the Rust version has finished with it sees the Rust version's status and may decide nothing, which shows as `rust only`. Look at when the Go run happened before reading one as a difference in behaviour.
- **Which question was asked.** Questions are matched per pull request, not per commit or wording.
- **Who wrote a trailer or marker.** Both are text anyone who can push or comment could write. A commit's author is shown where it is Almanac's.
- **The history of a status.** GitHub's combined status gives only the latest `almanac/trust` on each commit.
