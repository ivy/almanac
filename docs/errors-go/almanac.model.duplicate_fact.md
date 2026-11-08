# A workflow produces one fact twice

## What happened

Two steps produce a fact with the same name, or the model declares the same fact twice. A workflow has at most one `measure`, one `attempts` and one `check_patch` step, because their facts have fixed names.

## Why it matters

A run records each fact once and every reference reads it by name. Two producers would leave a reference unclear about which value it reads.

## What to do next

Give each fact its own name. To measure or check twice, split the work into two workflows.
