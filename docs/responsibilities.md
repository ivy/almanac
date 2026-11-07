# About who is responsible for what

An automated workflow fails in two opposite ways. It can do too much, and merge something nobody could vouch for. Or it can do too little, and leave work sitting until someone notices. Both come from the same mistake: nobody said where one party's job ends and the next one's starts.

This document explains the position behind [principle 12](core-principles.md#12-checks-decide-and-a-gap-in-them-belongs-to-the-repository), with dependency updates as the worked example. It is meant for anyone deciding how a workflow should behave, person or agent.

## The position

If the tests pass, the tests pass.

A pull request waiting for a person to read it is usually a person doing a job a check should do, and doing it worse. Reviewing a dependency bump by hand feels careful, but the reviewer rarely finds what a test suite would miss. They find it late, because they have a day job. Meanwhile the update waits, and a security fix can wait along with it.

So a passing check is the decision. When a repository cannot attest that a change is correct, that is a finding about the repository. It has a missing or weak workflow, and the answer is to fix the workflow, not to add a reviewer.

This moves the work rather than removing it. A check has to earn the trust this gives it, so writing a good one is now the important job.

## Say what each party owns

Before building a workflow, write down who owns each part of it. For Renovate pull requests:

| Party | Owns | Relies on |
|---|---|---|
| Renovate | Proposing updates, waiting out the cooldown, and merging once every check is green | The checks being meaningful |
| The repository | Checks that attest the code builds, passes its tests, and is safe to ship | Someone maintaining them |
| Almanac | Repairing a failed build, validating a security update, and reporting what it found | The repository's checks to judge its repairs |
| A person | Judgments no check can make, and the gaps that reach them | Being told, in a place they will see |

Each line constrains the others. Almanac never merges, by [principle 8](core-principles.md#8-almanac-reports-the-repository-decides). Renovate never decides an update is good, only that the checks say so. Nobody downstream re-checks what the row above already owns.

A new workflow should be able to fill in the same table. If a cell is empty, or two parties seem to own it, the design is not finished.

## What counts as an exception

Not every change can be settled by a check, and a shared default should say so honestly. An exception is justified by what a check cannot do, not by how anyone feels about a change.

- **The checks cannot observe the risk.** A release tool that only runs in a publish job, with the right to sign what it ships, never runs on a pull request. A green build says nothing about it, so a person keeps its larger updates until a check can exercise it.
- **The change is a judgment.** Deciding which fixes from an upstream project are worth carrying is not a test result.
- **The change is a planned migration.** Moving to a new operating system release is coordinated with what runs on it, so it is scheduled rather than proposed.
- **A merge would not stick.** Where another system overwrites the forge's copy of a repository, a merge there is undone by the next push.

Each of these names a fact about the system. "It is a major version" is not on the list. A major version is a claim that something may break, and that is what tests are for.

Write an exception down where it applies, with its reason, and keep the default in one shared place. A copied configuration drifts, and every copy looks like a decision somebody made.

## Green can still be wrong

Trusting checks means taking care over what they are checking. Two cases show the limits.

The first is a status that counts as a pass without testing anything. A cooldown proves a release is old, and proves nothing about whether it works. Configuration that treats it as a successful check lets a repository with no tests merge on a timer alone. Renovate refuses to automerge with no checks by default, so the default is the safe one, and a shared configuration must not override it. Almanac's own `almanac/trust` is the same trap: Renovate sees it as one more passing check, so Almanac never posts `success` on a commit nothing else has tested.

The second is a change that no longer does its job. Almanac's first live pass met a TypeScript update that broke a build, and an agent made the checks pass by downgrading TypeScript. Every check was green, on a pull request that no longer updated anything. The checks answered correctly, and the question had changed underneath them. So the workflow owns a promise the checks cannot make, that a repair keeps the update, and it enforces that in code.

The general rule is that a workflow is responsible for anything it changes after the checks were designed. If it can alter what is being tested, it has to prove it did not alter the point.

## When a person is needed

Asking a person is part of the design, and it has to be as deliberate as the rest. The question goes where the work is, on the pull request, naming and assigning who it is for and saying what was found. A person should be able to act on it without reconstructing what happened.

A question also has to be reachable. A workflow that waits for something that will never arrive has stalled, and it should say so. An update with no publish time can never finish a cooldown, so it asks once rather than waiting for ever.

The test for all of this is simple. Nothing should sit around, and nothing should merge that no one could vouch for. When something does sit, or does merge wrongly, the question is which row of the table failed, and that is what to fix.
