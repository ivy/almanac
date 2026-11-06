# A write was refused by a read-only client

## What happened

Code tried to write through a client created with `read_only`.

## Why it matters

Read-only clients exist so a dry run cannot touch a real repository, whatever its mode says.

## What to do next

If this happened during a dry run, it is a bug: report it. To write, create a client that is not read-only.
