# A workflow has a step or rule no run can reach

## What happened

A rule comes after an otherwise rule in the same `decide`, or a step comes after a `decide` that ends with an otherwise rule.

## Why it matters

An otherwise rule always matches, so nothing after it runs. A reader of the workflow would believe the later step or rule can happen, and views would show an ending that never occurs.

## What to do next

Remove the unreachable step or rule, or move the otherwise rule to the end of the flow.
