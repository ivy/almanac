# A workflow pushes something no patch policy checked

## What happened

A push effect names a fact that is not the output of a `check_patch` step, or the flow has no `check_patch` step before the push.

## Why it matters

An agent's patch is hostile until the patch policy has checked it: no edits to CI, no symbolic links, no binaries, and size limits. Only a checked patch may reach a branch.

## What to do next

Add a `check_patch` step after the agent that makes the patch, end the run in a `decide` when the patch is empty or refused, and push the fact the step produces.
