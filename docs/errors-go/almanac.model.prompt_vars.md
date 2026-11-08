# An agent's prompt variables do not match its prompt

## What happened

An agent step fills prompt variables that the prompt does not declare, leaves one it declares unfilled, or fills one from a fact that is not clean.

## Why it matters

A prompt is the agent's instructions. A missing variable would fail every call, and a hostile value in the instructions would let text an attacker wrote instruct the agent.

## What to do next

Fill exactly the variables the prompt declares, each from a clean fact or field. Pass hostile text as an input, which the agent reads as data.
