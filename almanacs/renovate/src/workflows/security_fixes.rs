//! Validates a Renovate security pull request. These skip the cooldown, so they get the most
//! scrutiny. The model can veto, never approve: it is asked only once the deterministic checks
//! pass, and a `NoConcerns` cannot rescue a failed one.
use crate::events::SecurityPull;
use crate::fleet::{TRUST_CONTEXT, scanned_repositories, trust_of_statuses};
use crate::update::{Ages, PullText, update_of};
use crate::vetting::Vetting;
use crate::workflows::build_failures::{MIN_RELEASE_AGE, ScanTarget};
use almanac::prelude::*;
use github::client::GithubClient;
use serde_json::{Value, json};

const RENOVATE_BRANCH: &str = "renovate/";
/// Renovate's default branch topic for vulnerability fixes.
const VULNERABILITY_SUFFIX: &str = "-vulnerability";

#[prompt("reviewer.md")]
pub struct ReviewerPrompt {
    pub repo: Slug,
}

pub const REVIEWER: AgentSpec<Quarantined, ReviewerPrompt> = AgentSpec::quarantined("reviewer");

clean! {
    pub enum Verdict { NoConcerns, Concerns }
}

output! {
    /// Only `verdict` can leave taint, because it is an enum.
    pub struct Review {
        pub verdict: Verdict,
        pub notes: String,
    }
}

observed! {
    pub struct SecurityView {
        pub found: bool,
        pub head: Sha,
        pub text: Untrusted<PullText>,
    }
}

pub struct Facts {
    pub repo: Slug,
    pub head: Sha,
    pub found: bool,
    pub names_the_fix: bool,
    pub readable: bool,
    pub install_script_added: bool,
    pub cooled: bool,
    pub asked: bool,
    pub review: Option<Untrusted<Review>>,
}

/// A security pull request, by its label or by Renovate's default branch name, because the label
/// is often not configured.
pub fn is_security(event: &SecurityPull) -> bool {
    let branch = event.branch.to_string();
    branch.starts_with(RENOVATE_BRANCH) && (event.labeled || branch.ends_with(VULNERABILITY_SUFFIX))
}

async fn read_pull(
    client: &GithubClient,
    target: &ScanTarget,
    event: &SecurityPull,
) -> Result<SecurityView, StepError> {
    let pull = client.pull(&event.repo.to_string(), event.number).await?;
    let open = pull["state"] == json!("open");
    let by_renovate = pull["user"]["login"] == json!(target.author);
    let head = pull["head"]["sha"].as_str().unwrap_or_default().parse();
    Ok(SecurityView {
        found: open && by_renovate,
        head: head.unwrap_or_else(|_| event.sha.clone()),
        text: Untrusted::new(PullText {
            body: pull["body"].as_str().map(str::to_string),
        }),
    })
}

fn opened_payload(repo: &Slug, branch: &Slug, sha: &Sha, number: u64, pull: &Value) -> Value {
    json!({
        "number": number,
        "repository": {"full_name": repo},
        "pull_request": {"head": {"ref": branch, "sha": sha}, "labels": pull["labels"]},
    })
}

/// Security pull requests with no final verdict, for backfill and catch-up. A fix that was too
/// young gets a new delivery ID once it has cooled.
#[scan]
pub async fn scan(scanner: &Scanner) -> Result<Vec<Scanned<SecurityPull>>, StepError> {
    let client = scanner.get::<GithubClient>()?;
    let target = scanner.get::<ScanTarget>()?;
    let ages = scanner.get::<dyn Ages>()?;
    let mut found = vec![];
    for repo in scanned_repositories(scanner, &client, &target.owner).await? {
        let name = repo.to_string();
        for pull in client.pulls_by(&name, &target.author).await? {
            let branch = pull["head"]["ref"]
                .as_str()
                .unwrap_or_default()
                .parse::<Slug>();
            let sha = pull["head"]["sha"]
                .as_str()
                .unwrap_or_default()
                .parse::<Sha>();
            let (Ok(branch), Ok(sha)) = (branch, sha) else {
                continue;
            };
            let number = pull["number"].as_u64().unwrap_or_default();
            let payload = opened_payload(&repo, &branch, &sha, number, &pull);
            let event = SecurityPull::from_json(&payload)?;
            if !is_security(&event) {
                continue;
            }
            let statuses = client.combined_status(&name, &sha.to_string()).await?;
            if trust_of_statuses(&statuses).is_some_and(|t| t.is_final()) {
                continue;
            }
            let cooled = crate::fleet::cooldown_of(ages.as_ref(), &pull).await;
            let id = format!("scan:security:{repo}#{number}@{sha}");
            found.push(Scanned::new(id, payload).at_state(format!("{cooled:?}")));
        }
    }
    Ok(found)
}

#[workflow(
    on = SecurityPull,
    key = "{repo}:{branch}",
    intent = "Validate a Renovate security update, and hold it until it is proved.",
    may = [Attest, Ask],
    runs_per_hour = 20,
    subjects = [repo(repo)],
    scan = scan,
    when = is_security,
    decide = policy,
)]
pub async fn security_fixes(cx: &Cx, event: &SecurityPull) -> Result<Facts, Halt> {
    // Before anything else: a security update skips the cooldown, and an absent status holds
    // nothing.
    cx.hold(TRUST_CONTEXT, &event.repo, &event.sha).await?;
    let client = cx.get::<GithubClient>()?;
    let target = cx.get::<ScanTarget>()?;
    let pull = cx
        .observe("pull", read_pull(&client, &target, event))
        .await?;
    let mut facts = Facts {
        repo: event.repo.clone(),
        head: pull.head.clone(),
        found: pull.found,
        names_the_fix: false,
        readable: false,
        install_script_added: false,
        cooled: false,
        asked: cx.attempts().await?.asked,
        review: None,
    };
    if !pull.found {
        return Ok(facts);
    }
    let update = pull.text.map(update_of);
    let vetting = cx.get::<dyn Vetting>()?;
    let ages = cx.get::<dyn Ages>()?;
    facts.names_the_fix = cx
        .record_fact("advisory", vetting.names_the_fix(&update))
        .await?;
    facts.cooled = cx
        .cooled(MIN_RELEASE_AGE, ages.newest(&update))
        .await?
        .is_some();
    let diff = cx.observe("diff", vetting.diff(&update)).await?;
    facts.readable = diff.readable;
    facts.install_script_added = diff.install_script_added;
    let checks_pass = facts.names_the_fix && facts.readable && !facts.install_script_added;
    if checks_pass && facts.cooled {
        let prompt = ReviewerPrompt {
            repo: event.repo.clone(),
        };
        facts.review = Some(cx.agent(REVIEWER).read(prompt, &diff.diff).await?);
    }
    Ok(facts)
}

fn attest(facts: &Facts, state: AttestState) -> Effect {
    Effect::Attest {
        context: TRUST_CONTEXT,
        state,
        repo: facts.repo.clone(),
        sha: facts.head.clone(),
    }
}

fn ask(facts: &Facts, question: Title, body: Untrusted<String>) -> Vec<Effect> {
    if facts.asked {
        return vec![];
    }
    vec![Effect::Ask {
        question,
        target: facts.repo.clone(),
        body,
    }]
}

fn refuse(facts: &Facts, question: Title, body: Untrusted<String>) -> Vec<Effect> {
    let mut effects = vec![attest(facts, AttestState::Failure)];
    effects.extend(ask(facts, question, body));
    effects
}

/// Deterministic checks first. The model comes last and can only say no.
pub fn policy(facts: &Facts) -> Vec<Effect> {
    let empty = || Untrusted::new(String::new());
    if !facts.found {
        return vec![];
    }
    if !facts.names_the_fix {
        return refuse(
            facts,
            title!("This update is not the fix an advisory names"),
            empty(),
        );
    }
    if !facts.readable {
        return refuse(
            facts,
            title!("This security update's package could not be read"),
            empty(),
        );
    }
    if facts.install_script_added {
        return refuse(
            facts,
            title!("This security update adds an install script"),
            empty(),
        );
    }
    if !facts.cooled {
        // Stays pending: nothing is wrong yet, and the next scan looks again once it has aged.
        return ask(
            facts,
            title!("This fix is younger than the cooldown"),
            empty(),
        );
    }
    let Some(review) = &facts.review else {
        return vec![];
    };
    match review.narrow(|r| r.verdict) {
        Verdict::NoConcerns => vec![attest(facts, AttestState::Success)],
        Verdict::Concerns => refuse(
            facts,
            title!("A reviewer raised concerns about this security update"),
            review.map(|r| r.notes.clone()),
        ),
    }
}
