# The config file is not valid TOML

## What happened

The config file exists but could not be parsed, or a value has the wrong type.

## Why it matters

An almanac refuses to start on a config it cannot understand, because guessing could widen a limit.

## What to do next

Fix the line the error points at. The keys an almanac accepts are `listen`, `max_chain_depth`, `runs_per_hour_cap`, `overall_runs_per_hour_cap`, and `concurrency`.
