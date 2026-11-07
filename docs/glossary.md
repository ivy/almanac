# Glossary

**agent.** A model session started for work that needs judgment, such as repairing a build. Every agent has a profile that fixes what it can do.

**almanac.** A self-contained binary of workflows, built with this library. `almanac-renovate` is one.

**`almanac/trust`.** The commit status Almanac posts. `pending` holds a merge, `success` releases it, and `failure` stops it and asks a person.

**attest.** Post a status on a commit.

**broker.** The one service GitHub's webhooks reach. It verifies each delivery and appends it to a log that almanacs read.

**closed set of effects.** The only things a workflow can ask for: `Attest`, `Await`, `File`, `Ask`, and `Push`. Nothing else exists, and a merge is deliberately not one.

**cooled.** Old enough to read. An update has cooled once its release is past the workflow's minimum age, which gives a malicious release time to be found first.

**`Cx`.** What `gather` receives. Every outside read and model call goes through it, so that it is recorded.

**dry run.** A run that records what it would do and applies nothing. Dry runs keep state of their own, so they never use up work a live run has to do.

**effect.** Something a workflow asks to happen, proposed by its policy and applied afterwards by the runtime.

**forge.** A code host. Today that means GitHub.

**gather.** The function under `#[workflow]`. It collects facts through `Cx`, and it can call agents.

**grant.** What an agent profile allows: writing files, network access, and secrets.

**`Halt`.** How `gather` stops a run early, either with a reason or with a failure.

**manifest.** What `synth` prints: each workflow's trigger, allowed effects, budget, agents, and required providers.

**observed.** Read from outside. Text in an observed type must be `Untrusted`.

**operator.** You: the one person an almanac serves. Almanac is not multi-tenant.

**policy.** The plain function that turns gathered facts into effects. It does no I/O, so it can be tested in a table and re-run on replay.

**profile.** An agent's capability level. `Quarantined` reads and can veto, `Fixer` edits a sandboxed checkout and returns a patch, and `Worker` takes clean input and returns typed output.

**provider.** Something a workflow depends on, such as a GitHub client, supplied in `providers.rs` and checked at startup.

**quarantined.** The agent profile with no tools, for reading hostile text safely.

**recorded call.** A read or model call made through `Cx`. Its result is stored with the run.

**replay.** Running a workflow again from its recorded calls, without calling anything outside.

**scan.** A function that lists current subjects, so a workflow can catch up on events it missed.

**taint, `Untrusted`.** The type for text an attacker could have written. It has no way to be read as a string, only narrowed to a closed set of answers.
