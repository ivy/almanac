//! The reads a workflow can make of GitHub. GitHub's JSON is hostile, so it stops here: each read
//! projects it down to validated fields, closed values, and `Tainted` text. Nothing else crosses.
use crate::value::{Script, Tainted};
use almanac::prelude::*;
use almanac_renovate::fleet::{Checks, checks_of, trust_pending};
use almanac_renovate::update::PullText;
use github::client::GithubClient;
use serde_json::json;

fn slug(args: &[Script], at: usize) -> Result<String, StepError> {
    match args.get(at) {
        Some(Script::Text(text)) => Ok(text.parse::<Slug>()?.to_string()),
        _ => Err(StepError(format!("argument {at} of a read must be a slug"))),
    }
}

fn sha(args: &[Script], at: usize) -> Result<String, StepError> {
    match args.get(at) {
        Some(Script::Text(text)) => Ok(text.parse::<Sha>()?.to_string()),
        _ => Err(StepError(format!("argument {at} of a read must be a sha"))),
    }
}

fn number(args: &[Script], at: usize) -> Result<u64, StepError> {
    match args.get(at) {
        Some(Script::Int(n)) if *n >= 0 => Ok(*n as u64),
        _ => Err(StepError(format!("argument {at} of a read must be a number"))),
    }
}

fn state_of(checks: Checks) -> &'static str {
    match checks {
        Checks::None => "none",
        Checks::Pending => "pending",
        Checks::Green => "green",
        Checks::Red => "red",
    }
}

/// `nil` when the branch has no open pull request. A login that is not a valid slug reads as an
/// author nobody can match.
async fn pull_for_branch(client: &GithubClient, args: &[Script]) -> Result<Script, StepError> {
    let listed = client
        .pull_for_branch(&slug(args, 0)?, &slug(args, 1)?)
        .await?;
    let Some(listed) = listed else {
        return Ok(Script::Nil);
    };
    let login = listed["user"]["login"].as_str().unwrap_or_default();
    let author = match login.parse::<Slug>() {
        Ok(login) => Script::text(login),
        Err(_) => Script::Nil,
    };
    Ok(Script::map([
        ("number", Script::Int(listed["number"].as_i64().unwrap_or_default())),
        ("author", author),
    ]))
}

async fn pull(client: &GithubClient, args: &[Script]) -> Result<Script, StepError> {
    let pull = client.pull(&slug(args, 0)?, number(args, 1)?).await?;
    let head: Sha = pull["head"]["sha"].as_str().unwrap_or_default().parse()?;
    let text = PullText {
        body: pull["body"].as_str().map(str::to_string),
    };
    let mergeable = match pull["mergeable"].as_bool() {
        Some(mergeable) => Script::Bool(mergeable),
        None => Script::Nil,
    };
    Ok(Script::map([
        ("head", Script::text(head)),
        ("mergeable", mergeable),
        ("text", Script::Tainted(Tainted::Pull(Untrusted::new(text)))),
    ]))
}

async fn checks(client: &GithubClient, args: &[Script]) -> Result<Script, StepError> {
    let (repo, head) = (slug(args, 0)?, sha(args, 1)?);
    let suites = client.check_suites(&repo, &head).await?;
    let statuses = client.combined_status(&repo, &head).await?;
    Ok(Script::map([
        ("state", Script::text(state_of(checks_of(&suites, &statuses)))),
        ("trust-pending", Script::Bool(trust_pending(&statuses))),
    ]))
}

async fn check_runs(client: &GithubClient, args: &[Script]) -> Result<Script, StepError> {
    let runs = client.check_runs(&slug(args, 0)?, &sha(args, 1)?).await?;
    let runs = runs["check_runs"].as_array().into_iter().flatten();
    let projected = runs.filter_map(|run| {
        let id = run["id"].as_i64()?;
        Some(Script::map([
            ("id", Script::Int(id)),
            ("failed", Script::Bool(run["conclusion"] == json!("failure"))),
        ]))
    });
    Ok(Script::List(projected.collect()))
}

/// `nil` when the log cannot be read: a repair can still go ahead on the other jobs' logs.
async fn job_logs(client: &GithubClient, args: &[Script]) -> Result<Script, StepError> {
    let log = client.job_logs(&slug(args, 0)?, number(args, 1)?).await;
    Ok(log.map_or(Script::Nil, |log| {
        Script::Tainted(Tainted::Text(Untrusted::new(log)))
    }))
}

pub async fn read(
    client: &GithubClient,
    operation: &str,
    args: &[Script],
) -> Result<Script, StepError> {
    match operation {
        "pull-for-branch" => pull_for_branch(client, args).await,
        "pull" => pull(client, args).await,
        "checks" => checks(client, args).await,
        "check-runs" => check_runs(client, args).await,
        "job-logs" => job_logs(client, args).await,
        other => Err(StepError(format!("github has no read `{other}`"))),
    }
}
