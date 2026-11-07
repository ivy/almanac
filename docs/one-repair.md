# One broken pull request, start to finish

This is a real repair from Almanac's first live run, on [`ivy/thicket#159`](https://github.com/ivy/thicket/pull/159).

## Before

Renovate proposed `@types/node` 26. The build failed on one line, because the new types are stricter about what a `Blob` accepts:

```text
Uint8Array is not assignable to BlobPart
```

Automerge would never touch it, and nobody was going to look. Renovate pull requests that fail like this sit for weeks.

## Noticing

Almanac heard about the failure from a webhook, or from a scan that runs every 15 minutes in case a webhook was lost. It read the pull request's current state instead of trusting the event, and confirmed that Renovate opened it and that CI was really red.

## Waiting

An agent reads text anyone could have written, so nothing reads an update until it has cooled. Almanac asked the registry when this release was published, and checked it was older than 72 hours. A release too new to trust waits, and a later scan looks again.

## Holding

Before starting an agent, Almanac posted `almanac/trust` as `pending`, so that Renovate could not merge the pull request halfway through.

## Repairing

The agent ran in a Podman sandbox holding the checkout and its dependencies, with no network and no credentials. It received the failure logs as data, not as instructions, and the only tools it had were calls into that sandbox. It made one change, then ran the project's build:

```diff
-      body: new Blob([bytes]),
+      body: new Blob([new Uint8Array(bytes)]),
```

It handed back a patch. It pushed nothing, because it cannot.

## Checking

Plain code then checked the patch: it must not touch CI configuration, Renovate configuration, or tool versions, must hold no binary files, must stay small, and must not take back what the update changed. A patch that fails asks a person.

## Pushing

Almanac committed the patch to the pull request's own branch, as `almanac[bot]`, with `pending` on the new commit:

```text
fix(deps): repair the build after this update

Almanac-Key: run-7:0
```

## Finishing

CI ran on the new commit and passed. Almanac then posted `almanac/trust` as `success`, and the pull request was `CLEAN`. From there the merge belongs to Renovate, under the repository's own rules.

## When it does not work

In the same run, TypeScript 7 broke `thicket#142`. The agent fixed the build, but lint still crashed, because `typescript-eslint` does not support TypeScript 7. A second attempt changed nothing, and it explained why instead of downgrading. Almanac stopped and asked a person, with that explanation.

That is the intended ending when a repair is out of reach. See [About who is responsible for what](responsibilities.md) for where the line sits, and [What it costs](how-it-works.md#what-it-costs) for what a session spends.
