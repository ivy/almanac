//! Where every Renovate pull request stands. State is derived from what GitHub says now and is
//! never stored, so it cannot go stale. Pull request titles are hostile text, so a report names a
//! pull request by number and validated branch only.
use crate::events::PATCH_TRAILER;
use crate::providers::{read_only_client, registry_endpoints};
use crate::update::{Ages, PullText, RegistryAges, update_of};
use crate::workflows::build_failures::MIN_RELEASE_AGE;
use almanac::diag::Error;
use almanac::prelude::*;
use almanac::{Said, Verb};
use futures::future::BoxFuture;
use github::client::GithubClient;
use registry::HttpRegistry;
use serde::Serialize;
use serde_json::{Value, json};
use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::Duration;

/// Repairs tried on one pull request before a person is asked.
pub const ATTEMPT_CAP: u32 = 3;
pub const TRUST_CONTEXT: &str = "almanac/trust";
/// Renovate's login in the operator's repositories, unless the environment names another.
const DEFAULT_AUTHOR: &str = "ivy-s-renovate-bot[bot]";
const CONFIG_FILES: [&str; 6] = [
    "renovate.json",
    "renovate.json5",
    ".renovaterc.json",
    ".github/renovate.json",
    ".github/renovate.json5",
    ".renovaterc",
];
const VULNERABILITY_SUFFIX: &str = "-vulnerability";
const UNLABELED: &str = "set vulnerabilityAlerts.labels, so security updates are labelled";
const MERGEABLE_READS: u32 = 4;
const MERGEABLE_PAUSE: Duration = Duration::from_secs(2);

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum State {
    Mergeable,
    InProgress,
    Waiting,
    NeedsPerson,
    BlockedByConfig,
}

/// One pull request's standing: exactly one state, and always a reason.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct Verdict {
    pub state: State,
    pub reason: String,
    /// Something worth knowing that does not change the state.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Conflict {
    None,
    Yes,
    /// GitHub computes this lazily.
    Unknown,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Checks {
    /// Nothing ran, so nothing can fail.
    None,
    Pending,
    Green,
    Red,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Trust {
    Pending,
    Success,
    Failure,
}

impl Trust {
    /// Whether a verdict was reached.
    pub fn is_final(self) -> bool {
        self != Self::Pending
    }
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Cooldown {
    Passed,
    Young,
    /// No publish time could be read. Never treated as passed.
    Unknown,
}

/// What can be learned about a repository's Renovate setup. `None` means it could not be read.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct RepoConfig {
    pub automerge: Option<bool>,
    pub platform_automerge: Option<bool>,
    pub git_ignored_authors: Option<bool>,
    pub trust_required: bool,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct PullFacts {
    pub conflict: Conflict,
    pub checks: Checks,
    pub trust: Option<Trust>,
    pub security: bool,
    /// Patches already pushed to this branch.
    pub attempts: u32,
    pub cooldown: Cooldown,
    pub config: RepoConfig,
}

fn verdict(state: State, reason: impl Into<String>) -> Verdict {
    Verdict {
        state,
        reason: reason.into(),
        note: None,
    }
}

/// A status holds a merge only if the repository makes it, because platform automerge ignores
/// any check that is not required.
fn trust_would_hold(c: &RepoConfig) -> bool {
    c.trust_required || c.platform_automerge == Some(false)
}

/// The whole policy for where a pull request stands. Earlier rules win.
pub fn derive(f: &PullFacts) -> Verdict {
    let patched = f.attempts > 0;
    match f.conflict {
        Conflict::Unknown => return verdict(State::Waiting, "GitHub to compute mergeability"),
        Conflict::Yes if patched => {
            return verdict(
                State::NeedsPerson,
                "a conflict on a branch carrying an Almanac patch",
            );
        }
        Conflict::Yes => return verdict(State::Waiting, "Renovate to rebase it"),
        Conflict::None => {}
    }
    if f.trust == Some(Trust::Failure) {
        return verdict(State::NeedsPerson, "it failed trust checks");
    }
    if f.security && f.trust != Some(Trust::Success) && !trust_would_hold(&f.config) {
        return verdict(
            State::BlockedByConfig,
            "set platformAutomerge to false, or require the almanac/trust check",
        );
    }
    match f.checks {
        Checks::Red if f.attempts >= ATTEMPT_CAP => {
            return verdict(State::NeedsPerson, "repairs did not fix the build");
        }
        Checks::Red => {
            return match f.cooldown {
                Cooldown::Passed => verdict(State::InProgress, "a repair is due"),
                Cooldown::Young => verdict(State::Waiting, "the update's cooldown to pass"),
                Cooldown::Unknown => verdict(
                    State::NeedsPerson,
                    "nothing says when this update was published",
                ),
            };
        }
        Checks::Pending => return verdict(State::Waiting, "CI to finish"),
        Checks::Green | Checks::None => {}
    }
    if f.trust == Some(Trust::Pending) && f.checks == Checks::None {
        return verdict(State::NeedsPerson, "nothing tests this repository");
    }
    if f.trust == Some(Trust::Pending) {
        return verdict(State::InProgress, "Almanac is validating it");
    }
    if patched && f.config.git_ignored_authors == Some(false) {
        return verdict(
            State::BlockedByConfig,
            "add Almanac's author to gitIgnoredAuthors, or Renovate will not merge a patched branch",
        );
    }
    let note = if f.checks == Checks::None {
        Some("no checks ran on this pull request".to_string())
    } else if f.config.automerge == Some(false) {
        Some("automerge is off, so this will not merge itself".to_string())
    } else {
        None
    };
    Verdict {
        note,
        ..verdict(State::Mergeable, "nothing is in its way")
    }
}

fn conflict_of(pull: &Value) -> Conflict {
    match pull["mergeable"].as_bool() {
        Some(true) => Conflict::None,
        Some(false) => Conflict::Yes,
        None => Conflict::Unknown,
    }
}

fn is_failed(conclusion: &str) -> bool {
    matches!(
        conclusion,
        "failure" | "timed_out" | "action_required" | "startup_failure" | "cancelled"
    )
}

/// Combines check suites with commit statuses, leaving out Almanac's own.
pub fn checks_of(suites: &Value, statuses: &Value) -> Checks {
    let suites = suites["check_suites"]
        .as_array()
        .cloned()
        .unwrap_or_default();
    // Installed Apps leave a suite `queued` forever when they have nothing to report.
    let suites: Vec<Value> = suites
        .into_iter()
        .filter(|s| s["latest_check_runs_count"] != json!(0))
        .collect();
    let statuses = statuses["statuses"].as_array().cloned().unwrap_or_default();
    // Neither of these tests anything. A cooldown or Almanac's own word is not a check.
    let tests_something = |s: &&Value| {
        let context = s["context"].as_str().unwrap_or_default();
        context != TRUST_CONTEXT && !context.starts_with("renovate/")
    };
    let statuses: Vec<&Value> = statuses.iter().filter(tests_something).collect();
    let suite_failed = |s: &Value| s["conclusion"].as_str().is_some_and(is_failed);
    let status_failed = |s: &&Value| s["state"] == json!("failure") || s["state"] == json!("error");
    if suites.iter().any(suite_failed) || statuses.iter().any(status_failed) {
        return Checks::Red;
    }
    let suite_open = |s: &Value| s["status"] != json!("completed");
    if suites.iter().any(suite_open) || statuses.iter().any(|s| s["state"] == json!("pending")) {
        return Checks::Pending;
    }
    if suites.is_empty() && statuses.is_empty() {
        return Checks::None;
    }
    Checks::Green
}

/// What Almanac last said about this commit, read from its combined status.
pub fn trust_of_statuses(statuses: &Value) -> Option<Trust> {
    trust_of(statuses)
}

/// Whether Almanac is holding this commit.
pub fn trust_pending(statuses: &Value) -> bool {
    trust_of(statuses) == Some(Trust::Pending)
}

fn trust_of(statuses: &Value) -> Option<Trust> {
    let all = statuses["statuses"].as_array()?;
    let found = all.iter().find(|s| s["context"] == json!(TRUST_CONTEXT))?;
    match found["state"].as_str() {
        Some("success") => Some(Trust::Success),
        Some("pending") => Some(Trust::Pending),
        _ => Some(Trust::Failure),
    }
}

impl RepoConfig {
    /// This config, with what it leaves unset taken from `shared`.
    fn or(self, shared: Self) -> Self {
        Self {
            automerge: self.automerge.or(shared.automerge),
            platform_automerge: self.platform_automerge.or(shared.platform_automerge),
            git_ignored_authors: self.git_ignored_authors.or(shared.git_ignored_authors),
            trust_required: self.trust_required,
        }
    }
}

pub fn config_of(text: Option<&str>, trust_required: bool) -> RepoConfig {
    let parsed = text.and_then(|t| json5::from_str::<Value>(t).ok());
    let flag = |key: &str| parsed.as_ref().and_then(|c| c[key].as_bool());
    RepoConfig {
        automerge: flag("automerge"),
        platform_automerge: flag("platformAutomerge"),
        git_ignored_authors: parsed
            .as_ref()
            .and_then(|c| c["gitIgnoredAuthors"].as_array())
            .map(|authors| !authors.is_empty()),
        trust_required,
    }
}

async fn read_config(client: &GithubClient, repo: &str, branch: &str) -> Result<RepoConfig, Error> {
    let mut text = None;
    for file in CONFIG_FILES {
        text = client.file(repo, file).await?;
        if text.is_some() {
            break;
        }
    }
    let required = client.required_checks(repo, branch).await?;
    let trust_required = required.iter().any(|c| c == TRUST_CONTEXT);
    let own = config_of(text.as_deref(), trust_required);
    let mut shared = RepoConfig::default();
    for preset in presets_of(text.as_deref()) {
        let preset = client.file(&preset, "default.json").await?;
        shared = shared.or(config_of(preset.as_deref(), trust_required));
    }
    Ok(own.or(shared))
}

/// The repositories a Renovate config extends with `github>owner/repo`, the form whose settings
/// live in that repository's `default.json`.
pub fn presets_of(text: Option<&str>) -> Vec<String> {
    let parsed = text.and_then(|t| json5::from_str::<Value>(t).ok());
    let extends = parsed.map(|c| c["extends"].clone()).unwrap_or_default();
    let names = extends.as_array().cloned().unwrap_or_default();
    let repo = |name: &Value| {
        let repo = name.as_str()?.strip_prefix("github>")?;
        (!repo.contains(['/', ':']) || repo.matches('/').count() == 1)
            .then(|| repo.split(['#', ':']).next().unwrap_or(repo).to_string())
    };
    names.iter().filter_map(repo).collect()
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct PullReport {
    pub number: u64,
    pub branch: String,
    #[serde(flatten)]
    pub verdict: Verdict,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct RepoReport {
    pub repo: String,
    pub pulls: Vec<PullReport>,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Report {
    pub repositories_scanned: usize,
    pub totals: BTreeMap<State, usize>,
    /// Open Renovate pull requests whose branch is outside what Almanac accepts, so a workflow
    /// would never see them. Each is a gap to look at.
    pub unreadable: Vec<String>,
    pub repositories: Vec<RepoReport>,
}

/// The same test a workflow applies, so a report and a run cannot disagree.
pub async fn cooldown_of(ages: &dyn Ages, pull: &Value) -> Cooldown {
    let text = Untrusted::new(PullText {
        body: pull["body"].as_str().map(str::to_string),
    });
    let update = text.map(update_of);
    match ages.newest(&update).await {
        Ok(published @ Published::At(_)) => {
            let now = time::OffsetDateTime::now_utc();
            match Cooled::check(published, MIN_RELEASE_AGE, now) {
                Some(_) => Cooldown::Passed,
                None => Cooldown::Young,
            }
        }
        _ => Cooldown::Unknown,
    }
}

async fn observe(
    client: &GithubClient,
    ages: &dyn Ages,
    repo: &str,
    number: u64,
    config: RepoConfig,
) -> Result<(PullFacts, bool, String), Error> {
    let pull = client
        .pull_settled(repo, number, MERGEABLE_READS, MERGEABLE_PAUSE)
        .await?;
    let sha = pull["head"]["sha"].as_str().unwrap_or_default();
    let suites = client.check_suites(repo, sha).await?;
    let statuses = client.combined_status(repo, sha).await?;
    let messages = client.pull_commit_messages(repo, number).await?;
    let attempts = messages
        .iter()
        .filter(|m| m.contains(PATCH_TRAILER))
        .count();
    let labels = pull["labels"].as_array().cloned().unwrap_or_default();
    let labeled = labels.iter().any(|l| l["name"] == json!("security"));
    let branch_text = pull["head"]["ref"].as_str().unwrap_or_default();
    let facts = PullFacts {
        conflict: conflict_of(&pull),
        checks: checks_of(&suites, &statuses),
        trust: trust_of(&statuses),
        security: labeled || branch_text.ends_with(VULNERABILITY_SUFFIX),
        attempts: attempts as u32,
        cooldown: cooldown_of(ages, &pull).await,
        config,
    };
    Ok((facts, labeled, branch_text.to_string()))
}

/// Every open Renovate pull request across `owner`'s repositories, by repository.
pub async fn report(
    client: &GithubClient,
    ages: &dyn Ages,
    owner: &str,
    author: &str,
) -> Result<Report, Error> {
    let repositories = client.repositories(owner).await?;
    let mut out = Report {
        repositories_scanned: repositories.len(),
        ..Report::default()
    };
    for repository in &repositories {
        let repo = repository["full_name"].as_str().unwrap_or_default();
        let pulls = client.pulls_by(repo, author).await?;
        if pulls.is_empty() {
            continue;
        }
        let default_branch = repository["default_branch"].as_str().unwrap_or("main");
        let config = read_config(client, repo, default_branch).await?;
        let mut reports = vec![];
        for pull in &pulls {
            let number = pull["number"].as_u64().unwrap_or_default();
            let (facts, labeled, branch) = observe(client, ages, repo, number, config).await?;
            if branch.parse::<Slug>().is_err() {
                out.unreadable.push(format!("{repo}#{number}"));
                continue;
            }
            let mut verdict = derive(&facts);
            if facts.security && !labeled {
                verdict.note.get_or_insert(UNLABELED.to_string());
            }
            *out.totals.entry(verdict.state).or_default() += 1;
            reports.push(PullReport {
                number,
                branch,
                verdict,
            });
        }
        out.repositories.push(RepoReport {
            repo: repo.to_string(),
            pulls: reports,
        });
    }
    Ok(out)
}

fn state_name(state: State) -> String {
    let text = serde_json::to_value(state).unwrap_or_default();
    text.as_str().unwrap_or_default().to_string()
}

/// A report as people read it.
pub fn render(report: &Report) -> String {
    let mut out = String::new();
    for repo in &report.repositories {
        out.push_str(&format!("{}\n", repo.repo));
        for pull in &repo.pulls {
            let v = &pull.verdict;
            let state = state_name(v.state);
            out.push_str(&format!(
                "  #{:<5} {:<18} {}  {}\n",
                pull.number, state, pull.branch, v.reason
            ));
            if let Some(note) = &v.note {
                out.push_str(&format!("         note: {note}\n"));
            }
        }
    }
    for gap in &report.unreadable {
        out.push_str(&format!("unreadable: {gap}\n"));
    }
    let totals: Vec<String> = report
        .totals
        .iter()
        .map(|(state, count)| format!("{count} {}", state_name(*state)))
        .collect();
    out.push_str(&format!(
        "{} repositories scanned: {}\n",
        report.repositories_scanned,
        totals.join(", ")
    ));
    out
}

fn required(name: &str, purpose: &str) -> Result<String, Error> {
    std::env::var(name).map_err(|_| Error::EnvMissing {
        name: name.to_string(),
        purpose: purpose.to_string(),
    })
}

/// Always read-only: a report has no reason to write.
/// The repositories a scan looks at, which is every one `client` can see unless a backfill named
/// one.
pub async fn scanned_repositories(
    scanner: &Scanner,
    client: &GithubClient,
    owner: &str,
) -> Result<Vec<Slug>, StepError> {
    let listed = client.repositories(owner).await?;
    let names = listed
        .iter()
        .map(|repository| repository["full_name"].as_str().unwrap_or_default());
    let visible = names
        .map(str::parse::<Slug>)
        .collect::<Result<Vec<_>, _>>()?;
    scanner.narrow(visible)
}

fn report_verb<'a>(_args: &'a [String], _config: &'a Config) -> BoxFuture<'a, Result<Said, Error>> {
    Box::pin(async move {
        let owner = required(
            "ALMANAC_OWNER",
            "the account whose repositories to report on",
        )?;
        let author = std::env::var("ALMANAC_RENOVATE_AUTHOR");
        let author = author.unwrap_or_else(|_| DEFAULT_AUTHOR.to_string());
        let client = read_only_client().ok_or_else(|| Error::EnvMissing {
            name: "GITHUB_TOKEN".to_string(),
            purpose: "a token that can read the repositories, or the GITHUB_APP_ settings"
                .to_string(),
        })?;
        let registry = HttpRegistry::new(registry_endpoints());
        let ages = RegistryAges(Arc::new(registry));
        let found = report(&client, &ages, &owner, &author).await?;
        Ok(Said {
            human: render(&found),
            json: json!(found),
        })
    })
}

pub const VERBS: &[Verb] = &[Verb {
    name: "report",
    run: report_verb,
}];
