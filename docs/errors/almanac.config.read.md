# The config file could not be read

## What happened

The file named by `ALMANAC_CONFIG`, or `almanac.toml` in the working directory, exists but cannot be opened.

## Why it matters

An almanac refuses to start on a config it cannot read, because running with defaults could run with wider limits than you intended.

## What to do next

Check the path and its permissions. To run with no config file, remove it or point `ALMANAC_CONFIG` at a path that does not exist.
