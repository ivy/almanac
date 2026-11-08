# A fixer agent can be reached before the release has cooled

## What happened

An agent step calls a fixer, and a run can reach it while the age a `measure` step produced is still young or unknown: no `decide` between the measure and the agent ends those runs.

## Why it matters

No agent reads an update until it has cooled, because a fresh release is when a compromised one is most likely to be caught by others first. The engine hands the fixer the cooldown proof only for a release that has cooled, so a run that reached the fixer any earlier would fail there, after the hold and the reads it paid for. The checker refuses the model instead, before any event arrives.

## What to do next

Add a `decide` between the `measure` step and the agent that ends every run whose age is not `Cooled`: one rule, `Not(Age.Is(Cooled))`, or one rule for `Young` and one for `Unknown`. The checker reads only rules whose condition tests the age alone, so put any other test in a rule of its own.
