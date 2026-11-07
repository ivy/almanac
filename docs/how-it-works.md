# About how an almanac works

An almanac is a program that responds to things that happen to your repositories. It looks like a small event-driven service, and the design choices come from one worry: an agent is expensive, unpredictable, and reads text attackers can write, so it should run as rarely as possible and hold as little as possible.

## How events arrive

GitHub sends webhooks to one service, the broker. It checks the signature, appends the delivery to a log in PostgreSQL, and answers. It knows nothing about workflows, so adding a workflow never touches GitHub.

Each almanac reads that log through its own cursor. One that was down catches up when it returns. Webhooks can still be lost, so scans list current subjects on a schedule. A scanned event takes the same path as a webhook, and a stable delivery ID means scanning twice changes nothing.

## From event to run

Events are deduplicated, then correlated by a key such as a repository and branch. One run is active per key, so a burst of webhooks about one pull request starts one run. Events during a run cause at most one rerun.

This is the difference from a system that starts an agent per event, where a burst of webhooks becomes a burst of spending.

## Gather, then decide

A workflow has two halves. `gather` does the I/O: it reads current state and can call an agent, and everything goes through `Cx`, so it is recorded. `policy` is a plain function from those facts to effects.

The split is what makes a workflow testable. A policy needs no network, so it can be tested as a table. And because every read is recorded, a run can be replayed later, with the policy re-run against what was seen, without calling GitHub or a model.

## Effects

A workflow cannot act. Its policy proposes effects from a small closed set, and the runtime checks each against what the workflow declared. Effects are recorded before they are applied, so a crash cannot lose one or apply one twice.

Merging is not an effect. Almanac gets a pull request to a mergeable state, and Renovate and the repository's rules do the merge.

## What it costs

Spending is limited at several levels, so a misbehaving outside world cannot run up a bill:

- One run per key, however many events arrive.
- Each workflow has a budget of runs per hour, and a tripped budget holds work and asks a person once.
- Three repair attempts per pull request, then a person is asked.
- A cap of $6 and a token budget on each agent session.
- Chains of events are stopped at a depth of three, so Almanac's own actions cannot feed each other.

In practice a repair is far below the cap. On `ivy/thicket`, repairs cost between $0.11 and $0.31 each, in six to nine turns and about two minutes, on Claude Sonnet.

## What it needs

Almanac works with GitHub and with Claude, through the Claude Code CLI. The `Sandbox`, `Forge`, and `Harness` interfaces leave room for others, and none is built. State is in PostgreSQL. It serves one operator, and is not built to be multi-tenant.
