# About keeping agents safe

A dependency update is written by strangers. Its release notes, its changelog, the error messages in a failed build, and the code itself can all contain text aimed at whatever reads them. An agent reading a build log is reading attacker-controlled input, and "ignore your instructions" is a real attack.

So Almanac assumes an agent will be fooled, and arranges things so that being fooled gains nothing.

## Text an attacker wrote is a different type

Anything read from outside is `Untrusted`. It has no way to be read as a string. It cannot be formatted into a prompt or a title, and code that tries does not compile. It can only be narrowed to an answer from a closed set, such as a `bool`. Agent output counts as outside: a model that read hostile text can repeat it.

This moves the rule from reviewers remembering to the compiler refusing. [Your first workflow](first-workflow.md) shows it happening.

## Whatever reads hostile text holds the least

An agent's profile fixes what it can do. One that reads hostile text gets no secrets and no general network, and its tools are only calls into a sandbox. The agent loop runs outside the sandbox, with an empty working directory and an environment built from nothing.

A sandbox enforces this underneath the types: code an agent runs cannot reach the network or the host's credentials. Dependencies are fetched first, by a separate step with no agent in it.

## A model can veto, never approve

A model's answer can stop something. Only deterministic evidence lets it through. A prompt-injected "this looks safe" gains an attacker nothing, because safe is not a decision a model makes here.

## A patch is checked by code

An agent never pushes. It returns a patch, and plain code decides. A patch cannot touch CI configuration, Renovate configuration, or tool versions, because a pushed patch runs in CI, which may hold secrets. GitHub adds a second guard: Almanac's App has no Workflows permission, so a push changing `.github/workflows` is refused anyway.

## Nothing reads an update too soon

Most malicious releases are found within days. So no agent reads an update until it has cooled, and code that skips the check does not compile, because the agent call requires a proof only the check can produce.

## A reviewer must see all of it

A veto only means something if the reviewer read everything. So a diff too large to show in full never reaches a reviewer, and neither does one with a changed file that cannot be shown, such as a binary. Both ask a person, because a reviewer that read part of an update has not reviewed it. One stray byte can make a source file look binary, and binary test fixtures are where the xz backdoor hid.

## What this does not cover

- A patch can still change a test, or a package script CI runs. Limit what branch-triggered workflows can reach.
- A `narrow` function reads the raw text and is trusted code, so it is reviewed like any other.
- The `claude` process runs on the host and parses hostile text, so a bug in it is the remaining route from hostile text to the host.

[ADR 1](adrs/0001-agent-sandbox.md) has the evidence behind the sandbox design.
