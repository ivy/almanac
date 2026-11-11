//! Repairs a Renovate pull request whose CI failed, once the update has cooled.
use crate::events::{BuildChanged, PATCH_TRAILER, suite_payload};
use crate::fleet::{
    ATTEMPT_CAP, Checks, Cooldown, TRUST_CONTEXT, checks_of, cooldown_of, scanned_repositories,
    trust_pending,
};
use crate::update::{Ages, PullText, update_of};
use almanac::prelude::*;
use github::client::GithubClient;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::time::Duration;

/// How old a release must be before anything reads it.
pub const MIN_RELEASE_AGE: Duration = Duration::from_secs(72 * 3600);
const RENOVATE_BRANCH: &str = "renovate/";
const LOG_LIMIT: usize = 256 * 1024;

#[prompt("fixer.md")]
pub struct FixerPrompt {
    pub repo: Slug,
}

pub const FIXER: AgentSpec<Fixer, FixerPrompt> = AgentSpec::fixer("fixer");

observed! {
    /// What GitHub says now. A payload is only a hint that something changed (R-36).
    pub struct PullView {
        pub found: bool,
        pub number: u64,
        pub head: Sha,
        pub conflicted: bool,
        pub failed: bool,
        pub running: bool,
        // A repair of ours is held, and its checks have passed.
        pub repair_passed: bool,
        pub text: Untrusted<PullText>,
    }
}

observed! {
    pub struct FailureLogs {
        pub text: Untrusted<String>,
    }
}

observed! {
    /// What the update itself added, as `(path, line)`.
    pub struct UpdatedLines {
        pub lines: Untrusted<Vec<(String, String)>>,
    }
}

/// A commit for the harness to check out. It is taken out before the model sees the input.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Checkout {
    pub repo: Slug,
    pub sha: Sha,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct FixerInput {
    pub logs: Untrusted<String>,
    #[serde(rename = "$checkout")]
    pub checkout: Checkout,
}

/// Where a run ended up. `policy` turns this into effects.
pub enum Facts {
    /// Nothing to do: the pull request is gone, conflicted, or not failing.
    Nothing,
    Cooling,
    /// Nothing dates this update, so its cooldown can never pass.
    Undated {
        repo: Slug,
        number: u64,
        asked: bool,
    },
    /// CI passed on a patched commit, so the hold can come off.
    Passed {
        repo: Slug,
        head: Sha,
    },
    /// The attempt cap is reached.
    Exhausted {
        repo: Slug,
        number: u64,
        head: Sha,
        asked: bool,
    },
    Repaired {
        repo: Slug,
        number: u64,
        branch: Slug,
        head: Sha,
        patch: Untrusted<Patch>,
        updated: Untrusted<Vec<(String, String)>>,
    },
}

/// CI failing on a Renovate branch, or finishing on a commit Almanac patched.
pub fn needs_attention(event: &BuildChanged) -> bool {
    let renovate = event.branch.to_string().starts_with(RENOVATE_BRANCH);
    renovate && !event.bookkeeping && (event.failed || (event.patched && event.finished))
}

async fn read_pull(
    client: &GithubClient,
    author: &str,
    event: &BuildChanged,
) -> Result<PullView, StepError> {
    let repo = event.repo.to_string();
    let branch = event.branch.to_string();
    let listed = client.pull_for_branch(&repo, &branch).await?;
    // A branch name proves nothing: only Renovate's own pull requests are ours to touch.
    let listed = listed.filter(|pull| pull["user"]["login"] == json!(author));
    let Some(listed) = listed else {
        return Ok(PullView {
            found: false,
            number: 0,
            head: event.sha.clone(),
            conflicted: false,
            failed: false,
            running: false,
            repair_passed: false,
            text: Untrusted::new(PullText::default()),
        });
    };
    let number = listed["number"].as_u64().unwrap_or_default();
    let pull = client.pull(&repo, number).await?;
    let head: Sha = pull["head"]["sha"].as_str().unwrap_or_default().parse()?;
    let sha = head.to_string();
    let suites = client.check_suites(&repo, &sha).await?;
    let statuses = client.combined_status(&repo, &sha).await?;
    let checks = checks_of(&suites, &statuses);
    let cleared = trust_pending(&statuses) && checks == Checks::Green;
    let messages = match cleared {
        true => client.pull_commit_messages(&repo, number).await?,
        false => vec![],
    };
    Ok(PullView {
        found: true,
        number,
        head,
        conflicted: pull["mergeable"] == json!(false),
        failed: checks == Checks::Red,
        running: checks == Checks::Pending,
        repair_passed: messages.iter().any(|m| m.contains(PATCH_TRAILER)),
        text: Untrusted::new(PullText {
            body: pull["body"].as_str().map(str::to_string),
        }),
    })
}

/// The tail of each failed job's log, where a build's error usually is.
async fn read_logs(
    client: &GithubClient,
    repo: &Slug,
    sha: &Sha,
) -> Result<FailureLogs, StepError> {
    let repo = repo.to_string();
    let runs = client.check_runs(&repo, &sha.to_string()).await?;
    let mut text = String::new();
    for run in runs["check_runs"].as_array().into_iter().flatten() {
        if run["conclusion"] != json!("failure") {
            continue;
        }
        let Some(id) = run["id"].as_u64() else {
            continue;
        };
        if let Ok(log) = client.job_logs(&repo, id).await {
            let start = log.len().saturating_sub(LOG_LIMIT);
            let start = (start..log.len())
                .find(|i| log.is_char_boundary(*i))
                .unwrap_or(0);
            text.push_str(&log[start..]);
            text.push('\n');
        }
    }
    Ok(FailureLogs {
        text: Untrusted::new(text),
    })
}

async fn read_updated(
    client: &GithubClient,
    repo: &Slug,
    number: u64,
) -> Result<UpdatedLines, StepError> {
    let lines = client.pull_added_lines(&repo.to_string(), number).await?;
    Ok(UpdatedLines {
        lines: Untrusted::new(lines),
    })
}

/// A repair can regenerate a lockfile, so only what declares a version has to survive.
fn is_lockfile(path: &str) -> bool {
    let name = path.rsplit('/').next().unwrap_or(path);
    name == "go.sum"
        || name.ends_with(".lock")
        || name.contains("-lock.")
        || name.contains(".lock.")
}

/// Whether a patch takes back what the update changed. Green checks prove nothing then, because
/// the pull request no longer updates anything.
/// `updated` is the lines the pull request added, by path, as `pull_added_lines` reads them.
pub fn undoes_the_update(patch: &Patch, updated: &[(String, String)]) -> bool {
    let declared = updated.iter().filter(|(path, _)| !is_lockfile(path));
    declared
        .into_iter()
        .any(|(path, line)| !patch.keeps(path, line))
}

/// Pull requests whose CI is red now, for backfill and catch-up.
#[scan]
pub async fn scan(scanner: &Scanner) -> Result<Vec<Scanned<BuildChanged>>, StepError> {
    let client = scanner.get::<GithubClient>()?;
    let target = scanner.get::<ScanTarget>()?;
    let ages = scanner.get::<dyn Ages>()?;
    let mut found = vec![];
    for repo in scanned_repositories(scanner, &client, &target.owner).await? {
        let name = repo.to_string();
        for pull in client.pulls_by(&name, &target.author).await? {
            if let Some(event) = needing_a_run(&client, ages.as_ref(), &repo, &pull).await? {
                found.push(event);
            }
        }
    }
    Ok(found)
}

/// A pull request that needs a run, if any. The delivery ID carries what a later scan must
/// notice: a pull request waiting out its cooldown stays a duplicate and costs nothing, and gets
/// a new ID the moment it cools. A hold with no run behind it does the same, so a status left
/// `pending` by a run that died is cleared.
async fn needing_a_run(
    client: &GithubClient,
    ages: &dyn Ages,
    repo: &Slug,
    pull: &Value,
) -> Result<Option<Scanned<BuildChanged>>, StepError> {
    let name = repo.to_string();
    let sha_text = pull["head"]["sha"].as_str().unwrap_or_default();
    let (Ok(sha), Ok(branch)) = (
        sha_text.parse::<Sha>(),
        pull["head"]["ref"]
            .as_str()
            .unwrap_or_default()
            .parse::<Slug>(),
    ) else {
        return Ok(None);
    };
    let suites = client.check_suites(&name, sha_text).await?;
    let statuses = client.combined_status(&name, sha_text).await?;
    let checks = checks_of(&suites, &statuses);
    let failed = checks == Checks::Red;
    let awaiting = trust_pending(&statuses) && checks == Checks::Green;
    if !failed && !awaiting {
        return Ok(None);
    }
    let number = pull["number"].as_u64().unwrap_or_default();
    let messages = client.pull_commit_messages(&name, number).await?;
    let patched = messages.iter().any(|m| m.contains(PATCH_TRAILER));
    // Only a hold on a repair is this workflow's to clear. Any other was placed on purpose, such
    // as `security_fixes` holding a fix that is too young.
    let held = awaiting && patched;
    if !failed && !held {
        return Ok(None);
    }
    let cooled = cooldown_of(ages, pull).await == Cooldown::Passed;
    let payload = suite_payload(repo, &branch, &sha, failed, patched);
    let id = format!("scan:{repo}#{number}@{sha}");
    Ok(Some(
        Scanned::new(id, payload).at_state(format!("cooled={cooled},held={held}")),
    ))
}

/// Which pull requests a scan looks at.
#[derive(Clone, Debug)]
pub struct ScanTarget {
    pub owner: String,
    pub author: String,
}

#[workflow(
    on = BuildChanged,
    key = "{repo}:{branch}",
    intent = "Repair a Renovate pull request whose build failed, after its cooldown.",
    may = [Attest, Await, Ask, Push],
    runs_per_hour = 20,
    subjects = [repo(repo)],
    scan = scan,
    when = needs_attention,
    decide = policy,
)]
pub async fn build_failures(cx: &Cx, event: &BuildChanged) -> Result<Facts, Halt> {
    let client = cx.get::<GithubClient>()?;
    let target = cx.get::<ScanTarget>()?;
    let pull = cx
        .observe("pull", read_pull(&client, &target.author, event))
        .await?;
    if !pull.found || pull.conflicted || pull.running {
        return Ok(Facts::Nothing);
    }
    let (repo, head) = (event.repo.clone(), pull.head.clone());
    if !pull.failed {
        return Ok(match pull.repair_passed {
            true => Facts::Passed { repo, head },
            false => Facts::Nothing,
        });
    }
    let attempts = cx.attempts().await?;
    if attempts.used >= ATTEMPT_CAP {
        let asked = attempts.asked;
        let number = pull.number;
        return Ok(Facts::Exhausted {
            repo,
            number,
            head,
            asked,
        });
    }
    let update = pull.text.map(update_of);
    let ages = cx.get::<dyn Ages>()?;
    let cooled = match cx.age(MIN_RELEASE_AGE, ages.newest(&update)).await? {
        Age::Passed(cooled) => cooled,
        Age::Young => return Ok(Facts::Cooling),
        Age::Unknown => {
            let (number, asked) = (pull.number, attempts.asked);
            return Ok(Facts::Undated {
                repo,
                number,
                asked,
            });
        }
    };
    cx.hold(TRUST_CONTEXT, &repo, &head).await?;
    let logs = cx.observe("logs", read_logs(&client, &repo, &head)).await?;
    let updated = cx
        .observe("updated", read_updated(&client, &repo, pull.number))
        .await?;
    let input = FixerInput {
        logs: logs.text,
        checkout: Checkout {
            repo: repo.clone(),
            sha: head.clone(),
        },
    };
    let prompt = FixerPrompt { repo: repo.clone() };
    let patch = cx.agent(FIXER).repair(prompt, &input, &cooled).await?;
    Ok(Facts::Repaired {
        repo,
        number: pull.number,
        branch: event.branch.clone(),
        head,
        patch,
        updated: updated.lines,
    })
}

/// What an agent said when it finished, for whoever is asked to take over.
pub fn closing_words(patch: &Patch) -> String {
    match patch.answer.get("summary").unwrap_or(&patch.answer) {
        Value::String(text) => text.clone(),
        other => other.to_string(),
    }
}

fn stop_and_ask(
    repo: &Slug,
    pull: u64,
    head: &Sha,
    question: Title,
    body: Untrusted<String>,
) -> Vec<Effect> {
    vec![
        Effect::Attest {
            context: TRUST_CONTEXT,
            state: AttestState::Failure,
            repo: repo.clone(),
            sha: head.clone(),
        },
        Effect::Ask {
            question,
            target: repo.clone(),
            pull: Some(pull),
            body,
        },
    ]
}

pub fn policy(facts: &Facts) -> Vec<Effect> {
    match facts {
        Facts::Nothing | Facts::Exhausted { asked: true, .. } => vec![],
        Facts::Cooling => vec![Effect::Await {
            until: "the update's cooldown to pass",
        }],
        Facts::Undated { asked: true, .. } => vec![Effect::Await {
            until: "a person to look at this update",
        }],
        Facts::Undated { repo, number, .. } => vec![Effect::Ask {
            question: title!("This update's build failed, and nothing says when it was published"),
            target: repo.clone(),
            pull: Some(*number),
            body: Untrusted::new(
                "No agent reads an update before its cooldown passes, and this one has no \
                 publish time to measure from. It needs a person."
                    .to_string(),
            ),
        }],
        Facts::Passed { repo, head } => vec![Effect::Attest {
            context: TRUST_CONTEXT,
            state: AttestState::Success,
            repo: repo.clone(),
            sha: head.clone(),
        }],
        Facts::Exhausted {
            repo, number, head, ..
        } => stop_and_ask(
            repo,
            *number,
            head,
            title!("Repairs did not fix this build"),
            Untrusted::new(String::new()),
        ),
        Facts::Repaired {
            repo,
            number,
            branch,
            head,
            patch,
            updated,
        } => match DEFAULT_POLICY.check(patch, head) {
            Ok(_)
                if patch
                    .zip(updated)
                    .narrow(|(patch, updated)| undoes_the_update(patch, updated)) =>
            {
                stop_and_ask(
                    repo,
                    *number,
                    head,
                    title!("A repair took back the update"),
                    patch.map(|p| p.diff.clone()),
                )
            }
            Ok(patch) => vec![Effect::Push {
                repo: repo.clone(),
                pull: *number,
                branch: branch.clone(),
                message: "fix(deps): repair the build after this update",
                hold: Some(TRUST_CONTEXT),
                patch,
            }],
            Err(Rejection::Empty) => stop_and_ask(
                repo,
                *number,
                head,
                title!("A repair ended with no change to push"),
                patch.map(closing_words),
            ),
            Err(_) => stop_and_ask(
                repo,
                *number,
                head,
                title!("A repair was refused by the patch policy"),
                patch.map(|p| p.diff.clone()),
            ),
        },
    }
}
