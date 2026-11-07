# A workflow needs a provider that was not set up

## What happened

A workflow declared a dependency, or may produce an effect, that nothing in the binary provides. This is checked at startup, before any event is read.

## Why it matters

Starting anyway would fail at the moment a real event needed it, or drop an effect.

## What to do next

If the almanac builds this provider from its environment, set what it reads: `contrib/` has an example for each almanac. Otherwise add the provider in `providers.rs`. For `Forge` and `Tracker`, which only a live run needs, you can also run in dry-run mode.
