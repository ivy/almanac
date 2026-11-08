# A workflow produces an effect it does not declare

## What happened

A rule produces an effect, or a `hold` step sets a status, of a kind missing from the workflow's `may`. A `hold` is an attest, so it needs `Attest`, and an ask that awaits once a person was asked needs `Await`.

## Why it matters

`may` is the reviewable list of everything a workflow can do to the world, and the manifest shows it. An effect outside it would be one no reviewer agreed to, so the runtime would refuse it anyway; the checker refuses it before the workflow starts.

## What to do next

If the workflow is meant to produce the effect, add its kind to `may`, where review sees the change. Otherwise remove the effect from the rule.
