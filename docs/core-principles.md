# Core Principles

Decision filters. Each one rules out something that might plausibly be wanted, and a change that violates one needs that principle revisited first.

## 1. Deterministic up to the point of inference

Code does everything code can do. A model is called only for work that needs judgment or repair, after deduplication, correlation, and deterministic checks have run.

**Says no to:** an agent as the first step of a workflow, routing or sequencing decided by a prompt, a model doing arithmetic or date comparison, and using inference to save writing a parser.

## 2. Behaviour is code, and configuration only tightens

Workflows, policy, capabilities, and allowed effects are compiled into the binary. Runtime configuration sets limits and addresses, and can only lower what the binary declares.

**Says no to:** YAML workflow definitions, prompts as the definition of behaviour, a config key that names a capability or an effect, and secrets in the config file.

## 3. Hostile text is a type

Everything observed from outside is `Untrusted`. It leaves taint only by narrowing to a closed set of values, and it never reaches a prompt's instructions. Agent output counts as observed.

**Says no to:** a bare `String` in an event type, string-formatting observed text into a prompt, treating a model's summary as clean because an agent wrote it, and a public accessor on `Untrusted`.

## 4. A model can veto, never approve

A model's answer can stop something. Only deterministic evidence lets it through. A prompt-injected "looks safe" must gain an attacker nothing.

**Says no to:** merging or attesting success on a model's verdict alone, a classifier deciding safety, and skipping a deterministic check because a model was confident.

## 5. Whatever reads hostile text holds the least power

An agent that reads attacker-reachable content gets no secrets and no general network access, and its output is reviewed by deterministic policy before anything acts on it. Capabilities are part of an agent's type and are enforced beneath it by a sandbox.

**Says no to:** an agent pushing directly, a write-capable agent with credentials reading build logs, and treating a capability type as enforcement without a sandbox behind it.

## 6. One thing, one run

Delivery is at-least-once at best, so the guarantee is exactly-once effects. Events are deduplicated by delivery, correlated by key, and limited to one active run per key. Chains and fan-out are bounded by depth and budget.

**Says no to:** a run per event, unbounded retries, an agent started before the budget check, and a retry loop with no attempt cap.

## 7. Record everything a run depends on

Outside reads, model answers, and guard verdicts are recorded. A replay reuses them and re-runs only policy, so a decision can be explained and re-examined without spending again.

**Says no to:** a read or model call outside `Cx`, policy that performs I/O, and a run whose result depends on something unrecorded.

## 8. Almanac reports; the repository decides

Effects are a small closed set, and merging is not one of them. Almanac publishes statuses, proposes changes, pushes checked patches to a pull request's own branch, and closes tracker work on evidence code checked. Repository rules and Renovate perform the merge. A tracker is the operator's planning record, so closing an entry in it reports that the work is done, the way `Attest` reports that a check passed ([ADR 2](adrs/0002-resolve-effect.md)).

**Says no to:** a merge effect, closing a pull request, bypassing branch protection, and an effect added because one workflow found it convenient without a principle covering it.

## 9. Errors are an interface

A person or an agent should be able to act on an error without reading the source. Every error says what happened, why it matters, and what to do next, with a stable code and a machine-readable form.

**Says no to:** a bare `unwrap` on a path reachable from input, an error that names no next step, and messages that differ between human and machine output in substance.

## 10. Reuse before building, generate before hand-writing

Integrations stay cheap. Types come from published specs where they exist, projected down to the fields a workflow uses, and hand-written declarations are the escape hatch. Dependencies favour maturity over novelty.

**Says no to:** hand-writing what a spec defines, generating a whole API surface nobody uses, a young crate where a mature one does the job, and a framework where a function would do.

## 11. Everything testable without the outside world

Every behaviour runs against fakes and a simulated forge, with no live service, model, or network. Backfill, simulation, and catch-up share the production path.

**Says no to:** a test that needs a real GitHub account, a code path that exists only for tests, and a feature with no dry run.

## 12. Checks decide, and a gap in them belongs to the repository

If a repository's checks pass, the change is good enough to merge. If nothing can attest that it is, the fix is a better workflow, not a person reading it. Every workflow says what it promises, what it relies on, and what it hands to a person, and a person is asked only for a judgment no check can make. [responsibilities.md](responsibilities.md) works through this with Renovate.

**Says no to:** holding a change for review because it feels risky, a person doing what a check could do, counting a cooldown as proof of tests, and green checks on a change that no longer does what it was for.
