# A workflow uses hostile text where only clean values may go

## What happened

A reference to a hostile value, such as text read from a pull request or an agent's answer, appears in a key, a subject, a hold, an effect's repository, sha or number, a prompt variable, or a clean predicate. A reference that looks inside a hostile value, such as `review.notes`, is refused the same way.

## Why it matters

Hostile text may have been written by an attacker. It reaches decisions only as a yes-or-no answer from a narrowing predicate or a value from a closed set, and reaches people only as a body shown through a map. Anywhere else it could steer what the almanac does.

## What to do next

Test the value with a narrowing predicate, show it in a body through a map, or give it to an agent as input. If the decision needs a fact about the text, add a narrowing predicate or classifier to the vocabulary that answers it.
