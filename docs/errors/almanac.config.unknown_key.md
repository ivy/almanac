# The config file has a key Almanac does not know

## What happened

A key in the config file matches nothing Almanac reads.

## Why it matters

Config can only tighten limits. Behaviour lives in the binary, so there is no key for a capability, an effect, or a workflow. A misspelled limit would otherwise be ignored and leave you with a wider limit than you meant.

## What to do next

Correct the key or remove it. To change behaviour, change the workflow's code.
