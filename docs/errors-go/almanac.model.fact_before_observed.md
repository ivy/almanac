# A workflow reads a fact before any step produces it

## What happened

A condition, effect, agent input or other reference names a fact that no earlier step in the flow produces. `when`, `key` and `subjects` are worked out before a run starts, so they can read only the event.

## Why it matters

A run walks its flow in order, and a fact exists only once the step that produces it has run. A reference to a fact that does not exist yet would read nothing, and a decision made on nothing is a decision no one reviewed.

## What to do next

Move the step that produces the fact before the step that reads it, or read the fact later in the flow. To filter or key on something only a read can tell, read it in the flow and end the run in a `decide` instead.
