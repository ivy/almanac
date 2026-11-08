# A worker agent is given hostile input

## What happened

An agent step gives a worker agent an input that is, or holds, hostile text. A worker can change things: it has network and secrets.

## Why it matters

An agent that can act must never read text an attacker could have written, because that text could instruct it. Only a quarantined agent or a fixer, which have no network and no secrets, may read hostile input.

## What to do next

Give the worker only clean facts. To act on hostile text, read it with a quarantined agent first, classify its answer into a closed set, and give the worker that.
