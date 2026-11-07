//! The Renovate catalog: what `workflows/build_failures.luau` can name. `read_pull` and `read_logs`
//! are copies of the private functions in `almanacs/renovate/src/workflows/build_failures.rs`,
//! which this spike leaves unchanged. They decide what is tainted, so they stay in Rust.
use crate::catalog::{Catalog, EventSpec, Field, ReadSpec, ScanSpec, parse_event};
use crate::taint::Tainted;
use crate::value::Node;
use crate::workflow::{LoadError, LuaWorkflow, seconds};
use almanac::cooled::Cooled;
use almanac::prelude::*;
use almanac::providers::Requirement;
use almanac::workflow::Found;
use almanac_renovate::events::BuildChanged;
use almanac_renovate::fleet::{Checks, checks_of, trust_pending};
use almanac_renovate::update::{Ages, PullText};
use almanac_renovate::workflows::build_failures::{
    BuildFailures, FailureLogs, PullView, ScanTarget,
};
use futures::future::BoxFuture;
use github::client::GithubClient;
use serde_json::json;

const LOG_LIMIT: usize = 256 * 1024;
const SOURCE: &str = include_str!("../workflows/build_failures.luau");
const FIXER_PROMPT: &str = include_str!("../../../almanacs/renovate/prompts/fixer.md");

fn failed(error: impl ToString) -> Halt {
    Halt::Failed(StepError(error.to_string()))
}

fn clean<T: std::str::FromStr>(node: &Node, what: &str) -> Result<T, Halt> {
    let text = node
        .as_str()
        .ok_or_else(|| failed(format!("{what} must be a string")))?;
    text.parse()
        .map_err(|_| failed(format!("{what} is not valid")))
}

async fn read_pull(
    client: &GithubClient,
    author: &str,
    repo: &Slug,
    branch: &Slug,
    sha: &Sha,
) -> Result<PullView, StepError> {
    let name = repo.to_string();
    let listed = client.pull_for_branch(&name, &branch.to_string()).await?;
    // A branch name proves nothing: only Renovate's own pull requests are ours to touch.
    let listed = listed.filter(|pull| pull["user"]["login"] == json!(author));
    let Some(listed) = listed else {
        return Ok(PullView {
            found: false,
            number: 0,
            head: sha.clone(),
            conflicted: false,
            failed: false,
            running: false,
            trust_pending: false,
            text: Untrusted::new(PullText::default()),
        });
    };
    let number = listed["number"].as_u64().unwrap_or_default();
    let pull = client.pull(&name, number).await?;
    let head: Sha = pull["head"]["sha"].as_str().unwrap_or_default().parse()?;
    let sha = head.to_string();
    let suites = client.check_suites(&name, &sha).await?;
    let statuses = client.combined_status(&name, &sha).await?;
    let checks = checks_of(&suites, &statuses);
    Ok(PullView {
        found: true,
        number,
        head,
        conflicted: pull["mergeable"] == json!(false),
        failed: checks == Checks::Red,
        running: checks == Checks::Pending,
        trust_pending: trust_pending(&statuses),
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

/// `cx.pull(event)`. It records under the same name and type as the compiled workflow, so the two
/// can replay each other's runs.
fn pull<'a>(
    cx: &'a Cx,
    arguments: Vec<Node>,
    _: &'a mut Vec<Cooled>,
) -> BoxFuture<'a, Result<Node, Halt>> {
    Box::pin(async move {
        let event = arguments
            .first()
            .ok_or_else(|| failed("pull takes the event"))?;
        let repo: Slug = clean(event.field("repo"), "repo")?;
        let branch: Slug = clean(event.field("branch"), "branch")?;
        let sha: Sha = clean(event.field("sha"), "sha")?;
        let client = cx.get::<GithubClient>()?;
        let target = cx.get::<ScanTarget>()?;
        let read = read_pull(&client, &target.author, &repo, &branch, &sha);
        let pull = cx.observe("pull", read).await?;
        Ok(Node::map([
            ("found", Node::Bool(pull.found)),
            ("number", Node::Num(pull.number as f64)),
            ("head", Node::text(&pull.head)),
            ("conflicted", Node::Bool(pull.conflicted)),
            ("failed", Node::Bool(pull.failed)),
            ("running", Node::Bool(pull.running)),
            ("trust_pending", Node::Bool(pull.trust_pending)),
            ("text", Node::Tainted(Tainted::Pull(pull.text))),
        ]))
    })
}

/// `cx.logs(repo, sha)`.
fn logs<'a>(
    cx: &'a Cx,
    arguments: Vec<Node>,
    _: &'a mut Vec<Cooled>,
) -> BoxFuture<'a, Result<Node, Halt>> {
    Box::pin(async move {
        let [repo, sha] = arguments.as_slice() else {
            return Err(failed("logs takes a repository and a commit"));
        };
        let repo: Slug = clean(repo, "repo")?;
        let sha: Sha = clean(sha, "sha")?;
        let client = cx.get::<GithubClient>()?;
        let logs = cx.observe("logs", read_logs(&client, &repo, &sha)).await?;
        Ok(Node::map([(
            "text",
            Node::Tainted(Tainted::Text(logs.text)),
        )]))
    })
}

/// `cx.cooled(min_age, update)`. The proof stays with the driver, and a script gets a ticket.
fn cooled<'a>(
    cx: &'a Cx,
    arguments: Vec<Node>,
    proofs: &'a mut Vec<Cooled>,
) -> BoxFuture<'a, Result<Node, Halt>> {
    Box::pin(async move {
        let min_age = seconds(arguments.first())?;
        let Some(Node::Tainted(Tainted::Update(update))) = arguments.get(1) else {
            return Err(failed(
                "cooled takes a minimum age and what `update_of` mapped",
            ));
        };
        let ages = cx.get::<dyn Ages>()?;
        Ok(match cx.cooled(min_age, ages.newest(update)).await? {
            Some(proof) => {
                proofs.push(proof);
                Node::Proof(proofs.len() - 1)
            }
            None => Node::Nil,
        })
    })
}

/// Listing pull requests needs the same reads and no judgment, so it is the compiled scan.
fn scan_build_failures(scanner: &Scanner) -> BoxFuture<'_, Result<Vec<Found>, StepError>> {
    Registered::scan(&BuildFailures, scanner)
}

pub fn catalog() -> Catalog {
    let mut catalog = Catalog::default();
    catalog.events.insert(
        "BuildChanged",
        EventSpec {
            kind: <BuildChanged as Event>::KIND,
            also: <BuildChanged as Event>::ALSO,
            fields: &[
                ("repo", Field::Slug),
                ("branch", Field::Slug),
                ("sha", Field::Clean),
                ("failed", Field::Clean),
                ("finished", Field::Clean),
                ("patched", Field::Clean),
                ("bookkeeping", Field::Clean),
            ],
            parse: parse_event::<BuildChanged>,
        },
    );
    catalog.reads.insert(
        "pull",
        ReadSpec {
            requires: || {
                vec![
                    Requirement::of::<GithubClient>(),
                    Requirement::of::<ScanTarget>(),
                ]
            },
            run: pull,
        },
    );
    catalog.reads.insert(
        "logs",
        ReadSpec {
            requires: || vec![Requirement::of::<GithubClient>()],
            run: logs,
        },
    );
    catalog.reads.insert(
        "cooled",
        ReadSpec {
            requires: || vec![Requirement::of::<dyn Ages>()],
            run: cooled,
        },
    );
    catalog.scans.insert(
        "build_failures",
        ScanSpec {
            event: "BuildChanged",
            requires: || Registered::requirements(&BuildFailures),
            run: scan_build_failures,
        },
    );
    catalog.prompts.insert("fixer.md", FIXER_PROMPT);
    catalog
}

/// The ported workflow. Its source is embedded, so like a compiled workflow it changes only when
/// the binary does.
pub fn build_failures() -> Result<LuaWorkflow, LoadError> {
    LuaWorkflow::load(SOURCE, "build_failures.luau", &catalog())
}
