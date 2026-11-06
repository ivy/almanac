# The store could not be reached or refused a write

## What happened

A read or write to the store failed.

## Why it matters

A delivery counts as seen only once it is stored, so nothing was lost: a redelivery or a catch-up scan will bring it back. Work may be delayed until the store returns.

## What to do next

Check that `DATABASE_URL` is correct and that PostgreSQL is running, then restart the almanac.
