# Almanac

Almanac keeps your repositories maintained. When a Renovate pull request breaks the build, it reads the failure, repairs it in a sandbox, and pushes the fix to the pull request, so the update can merge with nobody looking at it. When it cannot help, it says why and asks you on the pull request.

It is a Rust library for writing small, self-contained programs like that one, called almanacs. Behaviour is code you can test, and a model is called only where judgment or repair is needed.

> [!NOTE]
> Early. `almanacs/renovate` runs live on its author's repositories, and there are no releases yet. You build from a clone. It works with GitHub and Claude today.

## What it does

`@types/node` 26 broke a build in [`ivy/thicket#159`](https://github.com/ivy/thicket/pull/159). Nobody was going to look at it. Almanac waited for the release to be a few days old, held the pull request, and had an agent fix one line in a sandbox with no network:

```diff
-      body: new Blob([bytes]),
+      body: new Blob([new Uint8Array(bytes)]),
```

Plain code checked the patch, Almanac pushed it, CI passed, and the pull request was ready to merge. A repair like this costs tens of cents. [One broken pull request](docs/one-repair.md) follows it step by step.

## Is it for you?

Almanac is for one person or a small team with a lot of repositories. You are "the operator" throughout these docs. It needs GitHub, and Renovate if you want the repairs, plus a host with PostgreSQL and Podman, and an Anthropic key or token. It is not a merge bot, a hosted service, or a general workflow engine.

## Start here

- **See what it would do to your pull requests.** It needs a GitHub token, and changes nothing. [See what Almanac would do](docs/try-it-read-only.md).
- **Learn by building one.** It takes about 20 minutes, with nothing to sign up for. [Your first workflow](docs/first-workflow.md).
- **Run it for real.** [Run Almanac on your repositories](docs/deploying.md).

## What a workflow looks like

```rust
#[workflow(
    on = PullRequestChanged,
    key = "{repo}:{sha}",
    intent = "Hold a draft pull request, and let it through once it is ready.",
    may = [Attest],
    runs_per_hour = 10,
    decide = policy,
)]
pub async fn hold_drafts(_cx: &Cx, event: &PullRequestChanged) -> Result<Facts, Halt> {
    Ok(Facts {
        repo: event.repo.clone(),
        sha: event.sha.clone(),
        draft: event.draft,
    })
}

pub fn policy(facts: &Facts) -> Vec<Effect> {
    let state = match facts.draft {
        true => AttestState::Pending,
        false => AttestState::Success,
    };
    vec![Effect::Attest {
        context: "demo/ready",
        state,
        repo: facts.repo.clone(),
        sha: facts.sha.clone(),
    }]
}
```

The function gathers facts, and `policy` decides from them. A workflow can only do what `may` lists, and text an attacker could have written will not compile into a decision.

## Documentation

**Do something**

- [Your first workflow](docs/first-workflow.md)
- [See what Almanac would do to your pull requests](docs/try-it-read-only.md)
- [Run Almanac on your repositories](docs/deploying.md)
- [Write a workflow for real pull requests](docs/writing-workflows.md)
- [Stop Almanac, or find out what it did](docs/operating.md)

**Look something up**

- [Commands](docs/commands.md)
- [Configuration and limits](docs/configuration.md)
- [Authoring API](docs/authoring-reference.md)
- [Report states](docs/report-states.md)
- [Glossary](docs/glossary.md)

**Understand it**

- [One broken pull request, start to finish](docs/one-repair.md)
- [About who is responsible for what](docs/responsibilities.md)
- [About how an almanac works](docs/how-it-works.md)
- [About keeping agents safe](docs/keeping-agents-safe.md)
- [Vision](docs/vision.md) and [core principles](docs/core-principles.md)

**Work on Almanac**

[AGENTS.md](AGENTS.md) is the repository guide, and `make check` is the gate. It needs Podman and the `claude` CLI, and calls no model: the CLI runs against a local stub. [Requirements](docs/prd.md), [what proves each one](docs/coverage.md), the [roadmap](docs/roadmap.md), and [decisions](docs/adrs/) are maintainer documents.

## License

[ISC](LICENSE.md)
