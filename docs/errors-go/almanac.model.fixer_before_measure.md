# A fixer agent is called before the release's age is measured

## What happened

An agent step calls a fixer, and no `measure` step comes before it in the flow.

## Why it matters

No agent reads an update until it has cooled, because a fresh release is when a compromised one is most likely to be caught by others first. The engine passes the fixer the cooldown proof that a `measure` step makes, and without one it cannot call the fixer at all.

## What to do next

Add a `measure` step before the agent, and a `decide` between them that ends the run when the age is young or unknown.
