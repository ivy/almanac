# A workflow's flow can run off its end

## What happened

The last step of the flow is not a `decide` whose last rule is an otherwise rule, or a `decide` has no rules.

## Why it matters

Every run must end at a rule a person can read, so that every way a run can end can be listed and reviewed. A flow that runs off its end would finish having decided nothing anyone named.

## What to do next

End the flow with a `decide` whose last rule is an otherwise rule, and give every `decide` at least one rule.
