# A required environment variable is not set

## What happened

A verb needs a value that comes only from the environment, and it is unset.

## Why it matters

Secrets and deployment settings live in the environment and never in the config file, so there is nowhere else to find it.

## What to do next

Set the variable the error names and run the verb again. Keep secrets out of the config file and shell history.
