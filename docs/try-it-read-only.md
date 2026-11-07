# See what Almanac would do to your Renovate pull requests

This shows where every open Renovate pull request in your account stands, and what Almanac would do about each one. It writes nothing to GitHub and calls no model. You need a GitHub token, and no database, ingress, or GitHub App.

## Build it

```bash
git clone https://github.com/ivy/almanac
cd almanac
mise install
cd almanacs/renovate
cargo build --release
```

The binary is `target/release/almanac-renovate`. Without `mise`, install the Rust toolchain named in `rust-toolchain.toml`.

## Point it at your account

```bash
export ALMANAC_READ_ONLY=1
export GITHUB_TOKEN="$(gh auth token)"
export ALMANAC_OWNER=your-account
export ALMANAC_RENOVATE_AUTHOR='renovate[bot]'
```

`ALMANAC_RENOVATE_AUTHOR` is the login that opens your Renovate pull requests, which depends on how you run Renovate. Pull requests from any other author are left alone.

`ALMANAC_READ_ONLY=1` makes the GitHub client refuse every write, and replaces the agent with one that stops before calling a model.

## See where each pull request stands

```bash
target/release/almanac-renovate report
```

```text
you/website
  #41    mergeable          renovate/astro-5.x  nothing is in its way
  #38    in-progress        renovate/node-26.x  a repair is due
you/api
  #112   waiting            renovate/go-modules  the update's cooldown to pass
12 repositories scanned: 1 mergeable, 1 waiting, 1 in-progress
```

[Report states](report-states.md) says what each state means and who acts on it. Add `--format json` for the same report as data.

## See what it would act on

```bash
target/release/almanac-renovate backfill --dry-run
```

This runs each workflow's scan and takes each pull request that needs attention through the real workflow. A run that would repair something stops at `read-only: an agent would run here`, which is the point it would spend money. Use `--repo you/website` to look at one repository. Naming one the token cannot see is an error.

## Next

To let it act, follow [Run Almanac on your repositories](deploying.md). To understand what a repair does first, read [One broken pull request](one-repair.md).
