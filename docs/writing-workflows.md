# Write a workflow for real pull requests

This takes an almanac from [Your first workflow](first-workflow.md) to one that reacts to GitHub events, reads current state, and posts a status. The example holds a pull request whose author wrote "do not merge".

## Add the GitHub crate

Almanac is not published to crates.io, so depend on your clone by path, as `almanac new` did:

```toml
[dependencies]
almanac = { path = "/path/to/almanac/crates/almanac" }
github = { path = "/path/to/almanac/crates/github" }
serde_json = "1"
```

## Use a GitHub event as the trigger

The `github` crate generates event types from GitHub's API description: `PullRequestOpened`, `PullRequestLabeled`, `PullRequestSynchronize`, `CheckSuiteCompleted`, and `Status`. Their kinds, such as `github.pull_request.opened`, are what `simulate` takes.

An event says something changed, and it can be stale by the time a run starts. Read what is true now, through `cx.observe`, so the result is recorded and a replay can reuse it:

```rust
use almanac::prelude::*;
use github::PullRequestOpened;
use github::client::GithubClient;
use serde_json::json;

observed! {
    pub struct PullState {
        pub head: Sha,
        pub draft: bool,
        pub body: Untrusted<String>,
    }
}

async fn read_pull(
    client: &GithubClient,
    repo: &Slug,
    number: u64,
) -> Result<PullState, StepError> {
    let pull = client.pull(&repo.to_string(), number).await?;
    Ok(PullState {
        head: pull["head"]["sha"].as_str().unwrap_or_default().parse()?,
        draft: pull["draft"] == json!(true),
        body: Untrusted::new(pull["body"].as_str().unwrap_or_default().to_string()),
    })
}

clean! {
    pub struct Facts {
        pub repo: Slug,
        pub head: Sha,
        pub held: bool,
    }
}

fn says_do_not_merge(body: &String) -> bool {
    body.contains("do not merge")
}

#[workflow(
    on = PullRequestOpened,
    key = "{repo}:{head_branch}",
    intent = "Hold a pull request whose author says not to merge it.",
    may = [Attest],
    runs_per_hour = 20,
    decide = policy,
)]
pub async fn hold_marked(cx: &Cx, event: &PullRequestOpened) -> Result<Facts, Halt> {
    let client = cx.get::<GithubClient>()?;
    let pull = cx
        .observe("pull", read_pull(&client, &event.repo, event.number))
        .await?;
    Ok(Facts {
        repo: event.repo.clone(),
        head: pull.head,
        held: pull.draft || pull.body.narrow(says_do_not_merge),
    })
}

pub fn policy(facts: &Facts) -> Vec<Effect> {
    let state = match facts.held {
        true => AttestState::Pending,
        false => AttestState::Success,
    };
    vec![Effect::Attest {
        context: "demo/held",
        state,
        repo: facts.repo.clone(),
        sha: facts.head.clone(),
    }]
}
```

Everything from outside is `Untrusted` until it is validated, so the body only reaches `Facts` as the `bool` that `narrow` returns.

## Supply what it needs

`synth` now lists `github::client::GithubClient` under `requires`, because the macro found it in the body. Provide it in `src/providers.rs`:

```rust
use almanac::prelude::*;
use github::client::{Auth, GithubClient};
use github::forge::{CommitAuthor, GithubForge};
use std::sync::Arc;

const API: &str = "https://api.github.com";

fn client() -> Option<GithubClient> {
    let token = std::env::var("GITHUB_TOKEN").ok()?;
    Some(GithubClient::new(API, Auth::Token(token)))
}

pub fn all(_config: &Config) -> Providers {
    let (Some(reader), Some(writer)) = (client(), client()) else {
        return Providers::new();
    };
    let forge: Arc<dyn Forge> = Arc::new(GithubForge::new(writer, CommitAuthor::default()));
    Providers::new()
        .provide::<GithubClient>(Arc::new(reader))
        .provide::<dyn Forge>(forge)
}
```

`cargo run -- check` confirms it. Without the token it stops with `almanac::provider::missing`, naming what is absent. To ask a person, also provide a `dyn Tracker`, such as `github::tracker::GithubIssues`.

## Test it

Test `policy` as a plain function, as in the tutorial. To test the whole workflow, `crates/simforge` is a local GitHub and `almanac::testing` holds fakes, including a scripted agent. `almanacs/renovate/tests/build_failures.rs` shows both, and needs no network.

## Add an agent

Declare the prompt and the agent, then call it from `gather`. A prompt is a Markdown file in `prompts/`, checked against its struct at compile time. `almanacs/renovate/src/workflows/build_failures.rs` is the complete example, including the sandbox a `Fixer` needs, and `almanacs/renovate/src/providers.rs` shows how to build the harness.

## Catch up on missed events

Webhooks can be lost, so a workflow that must not miss anything declares `scan = ...`, a `#[scan]` function that lists current subjects and returns the workflow's own event type. A stable delivery ID makes scanning twice harmless.

## Run it

Your almanac runs like `almanac-renovate`: as `your-almanac run` under `contrib/systemd/almanac@.service`, with the same `DATABASE_URL` as the broker. The broker needs no change, because it passes every GitHub delivery to the shared log, and each almanac ignores kinds it has no workflow for. [Run Almanac on your repositories](deploying.md) covers the broker and the service.

## React to something other than these events

Another GitHub event means adding it to `crates/github/projection.toml` and running `make generate`, in your clone of Almanac. A source other than GitHub needs changes to Almanac, because the broker verifies only GitHub's signatures today. `simulate` and `backfill` still work for your own event types, as in the tutorial, and a `#[scan]` can produce them from any API.
