# Vision

Almanac is how one operator's software keeps itself maintained. It watches for things that happen to their projects, decides deterministically what deserves a response, and brings in an AI agent only where judgment or repair is needed.

## The problem

On 2026-11-05 the operator's GitHub account had 119 open pull requests, and 97 of them were dependency updates. All 10 open Renovate pull requests in `ivy/thicket` dated from October, and 5 of those had failing checks. Automerging after a cooldown clears the healthy updates and does nothing for the broken ones.

Vulnerability response has the same shape. When a certificate authority was compromised, tracing exposure across everything that trusted it took a hand-written pull request, and its overlap with an open Renovate pull request was left as a note for someone to act on.

Existing automation falls short in a specific way. Agent platforms start one agent per event, so a burst of webhooks or a chain reaction becomes a burst of spending, and attacker-written text reaches an agent before any deterministic check has run. Their behaviour lives in prompts and YAML, which cannot be type-checked, unit-tested, or reasoned about.

## The thesis

Behaviour belongs in code, and inference is the last resort rather than the first step. A workflow is a real program: deterministic up to the point of inference, testable without a model or a network, and reviewed in a pull request. The compiler enforces the rules that matter most, including that text an attacker could have written never becomes instructions, and that whatever reads hostile text holds as little power as possible.

## What Almanac is

An almanac is a self-contained binary: a small program with a `main()`, built from workflows written against the Almanac library. Changing one almanac deploys it separately and tests it in isolation. `almanac new` scaffolds one, and `almanac generate` adds to it.

Almanacs share one broker, which is the single place webhooks arrive, and one database. Each runs as its own daemon and starts rootless Podman sandboxes for its agents. Adding a workflow or a whole almanac never means registering another webhook.

Each almanac receives events, deduplicates and correlates them, gathers facts through recorded calls, applies a pure policy, and produces a small closed set of effects. Everything it observes and decides is recorded, so a run can be replayed without calling a model again.

## What Almanac is not

- **Not a merge bot.** Almanac gets a pull request into a mergeable state and reports what it found. The repository's rules and Renovate's automerge perform the merge.
- **Not a backlog.** It holds what it has seen and decided, never a queue of planned work. Planned work belongs in a tracker.
- **Not a general workflow engine.** It is not Temporal, Airflow, or Zapier. It handles events about software projects for one operator.
- **Not an agent framework.** It runs agents behind a narrow interface and treats their output as untrusted. It does not hold conversations or define personas.
- **Not a vulnerability database.** It consumes feeds and curates nothing.
- **Not a chat surface.** People are reached through a tracker or a forge. A conversational front end belongs to something else.
- **Not multi-tenant.** It serves one operator. Nothing is paid for twice to keep that door open.

## Relationship to other tools

Almanac starts as its own project, and it is built to stand alone. It is intended to sit upstream of a tracker, which holds the plan, and workers, which carry it out: Almanac is how things outside the plan get into it. That integration is deferred, so the interfaces that would carry it stay narrow. See [roadmap.md](roadmap.md).

## What success looks like

The operator stops thinking about dependency maintenance. Healthy updates merge after their cooldown with no Almanac involvement. Broken updates get repaired and merged by the same route, and security updates arrive validated. A person is asked only when something needs a judgment no check can make, and spending stays bounded when the outside world misbehaves.
