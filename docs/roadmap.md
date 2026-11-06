# Roadmap

Milestones are defined by what becomes possible, not by dates. Anything deferred is here with the milestone it belongs to.

## M0: Proven locally

Everything in [prd.md](prd.md), running against a simulated forge and PostgreSQL, with a read-only pass over real pull requests and an evaluation of real repair quality.

**Done when** the PRD's "Done when" list holds.

## M1: Running

The operator deploys `almanacs/renovate` across their repositories, starting with a dry run and one repository.

- One GitHub App across all repositories, its webhook URL pointed at the broker, because GitHub has no account-level webhook for personal accounts
- The broker behind a public HTTPS ingress, and each almanac as a lingering systemd user service
- `gitIgnoredAuthors` and `vulnerabilityAlerts.labels` set in the Renovate configuration
- `platformAutomerge: false`, or required checks, on each repository in scope
- One-binary deployment

**Done when** every open Renovate pull request is mergeable or names the person or setting it is waiting on, and a broken one is repaired and merged with no human involvement.

## M2: Exposure

Learning about a vulnerability before a fix pull request exists.

- An inventory of what each repository, image, and host contains, beyond lockfiles
- A feed from cve.now joined against that inventory, reporting blast radius on publication
- Linking a later Renovate pull request to the exposure it resolves
- Filing work when no fix appears within a severity's window

**Done when** a new advisory reports its blast radius before any pull request exists.

## M3: More sources

- Error triage from Sentry, taking an ID from the webhook and reading typed data from the API
- Scheduled workflows beyond catch-up scans
- A model-backed guard reviewing actions it cannot be injected by
- A cheap classifier gating spend where volume justifies it

**Done when** a second almanac ships with no change to the library.

## M4: Joined up

Almanac feeds the rest of the stack.

- A tracker provider: `File` and `Ask` become planned work and human gates
- Repair moves to a separate worker if that proves to be the better home, which depends on what M0's evaluation shows about the sandbox and harness

**Done when** an event from outside becomes tracked work that a worker delivers.

## M5: Hardened

- An IDS over the sandbox: blocked egress attempts raise alerts, and canary credentials detect exfiltration
- A Daytona sandbox provider, generated from `specs/daytona/`. Per-sandbox network rules need Daytona Tier 3 or above, because lower tiers cannot override organization restrictions, and a `Fixer` depends on them
- Stronger isolation for agents, such as a microVM tier
- A seal on `Untrusted` serialization, and on `clean!`'s hidden traits
- Prompt output sections generated from agent output types

**Done when** a seeded malicious dependency is detected in the sandbox and raises an alert.

## Deliberately unscheduled

- **Dependabot.** Renovate covers the need.
- **Merging from Almanac.** Refused by principle 8.
- **A workflow DSL or a web editor.** Refused by principle 2.
- **Multi-tenancy.** Refused by the vision.
