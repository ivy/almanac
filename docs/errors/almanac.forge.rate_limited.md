# GitHub's rate limit was reached

## What happened

GitHub refused a request because this client has used its allowance, and the wait is longer than Almanac will sit idle.

## Why it matters

Nothing is lost. Effects stay in the outbox and are retried, and a catch-up scan picks up anything missed.

## What to do next

Wait for the limit to reset. If it happens often, lower `concurrency` or the frequency of scans.
