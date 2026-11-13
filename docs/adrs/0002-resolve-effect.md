# 2. Closing tracker work is an effect

## Status

Proposed, pending review on [ivy/almanac#6](https://github.com/ivy/almanac/issues/6).

## Context

Work a workflow files stays open until a person notices it is done. Two workflows have hit this. A backlog reconciler sees a merged pull request that says `Closes <task>`, and a host check files work when a check fails and later sees the check pass again. Both have evidence code can check, and both leave an entry open that a person then closes by hand. The effect module said nothing in it closes, and principle 8 refuses an effect one workflow finds convenient, so this records the principle that covers it ([ivy/almanac#6](https://github.com/ivy/almanac/issues/6)).

## Decision

`Effect::Resolve { target, item, evidence }` closes tracker work, and `Tracker::resolve(key, target, item, evidence)` applies it.

A tracker is the operator's planning record, not the repository's. Closing an entry in it reports that the work is done, which is closer to `Attest` than to a merge: the repository's code and rules are untouched. Principle 4 holds because `evidence` is a `Title`, which holds only a literal and `Clean` values. A model's answer can stop a close by failing a check, and it cannot write the evidence for one.

`item` names the entry in one of two ways:

- `Item::Named(Slug)` is the tracker's own name for it, such as an issue number or a task's UUID. The reconciler reads one from a commit message.
- `Item::Filed { key }` is whatever this workflow filed under one of its own keys. The host check knows the key it filed under and has never seen the tracker's name for the entry. The runtime looks up the keys its `File` effects were applied under (`Store::filed`) and hands those to the tracker, which already stores them to stay idempotent. A workflow can only reach its own filed work this way.

The GitHub tracker comments the evidence and then closes the issue as completed. It closes only issues labelled `almanac`, so a named issue almanac did not file is refused with an error.

## Options rejected

- **Evidence as `Untrusted<String>`, like `File`'s body.** It would let a model's text be the reason a close happened, which principle 4 rules out.
- **Passing the workflow's key to `Tracker::file`, so trackers can find work by it.** Every tracker would need to store a second key, and `file` changes for every caller. The outbox already records which run filed what.
- **Expanding `Item::Filed` when the run is recorded.** The outbox would hold the expanded keys, but planning would need the store. Expanding when the effect is applied keeps planning pure, and the tracker looks the entries up at that point anyway.
- **A default `Tracker::resolve` that returns an error.** A tracker that cannot close would only find out when a workflow first tries to. A required method makes every tracker decide.

## Consequences

Every `Tracker` implements `resolve`. A workflow that may `Resolve` needs a tracker at startup, as `File` and `Ask` do. Closing a pull request is still not an effect.
