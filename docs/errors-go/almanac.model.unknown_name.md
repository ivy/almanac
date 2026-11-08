# A workflow names something the vocabulary does not have

## What happened

The checker found a name in a workflow model, such as an event, observer, predicate, map, agent, scan or patch policy, that is not registered in the almanac's vocabulary. A classifier's `is` value that is not one of its declared answers is reported the same way.

## Why it matters

A model can only combine parts of the vocabulary, which is Go compiled into the binary and reviewed like any other code. A name that resolves to nothing would leave a run with no way to read, test or act, so the almanac refuses the workflow before any event arrives.

## What to do next

Check the name against the vocabulary: names are exact and case-sensitive, and are written with their namespace, such as `renovate.not_actionable`. If the part does not exist yet, add it to the vocabulary in Go, with a label a person can read.
