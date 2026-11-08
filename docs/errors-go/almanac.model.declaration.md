# A workflow declaration is missing or out of range

## What happened

A workflow's declarations are incomplete or contradictory: it has no name or intent, no trigger or key, a runs-per-hour that is not positive, an effect kind listed twice in `may`, a condition that tests spent attempts with no attempts limit, a fact declared that no step produces, a step whose kind and contents disagree, or an observed fact named like something the run's context records itself (`cooldown`, `attempts`, `hold`, `screen`, `guard`, `model` or an agent's name), which the engine refuses.

## Why it matters

A workflow's declarations are what review, the manifest and the runtime's budgets read. One that is missing or out of range would let a workflow run without a limit or say less than it does.

## What to do next

Fix the declaration the message names. Every workflow declares a name, an intent, a trigger, a key and a positive runs-per-hour.
