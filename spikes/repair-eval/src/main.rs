//! Tries a real repair on each pull request named in a list, and records what happened. Throwaway,
//! and read-only: its GitHub client refuses writes, and a patch is only ever written to a file.
//!
//! `repair-eval LIST OUT [--stub]`, where LIST is a JSON array of `{"repo", "number"}`. Results and
//! patches go under OUT, which must not be in the repository's tracked tree. With `--stub` no model
//! is called, to prove the pipeline first.
use almanac::agent::{Grant, Template};
use almanac::prelude::*;
use almanac_renovate::workflows::build_failures::FixerPrompt;
use claude::stub::{StubModel, Turn};
use claude::{CHECKOUT_FIELD, Checkouts, ClaudeConfig, ClaudeHarness};
use github::client::{Auth, GithubClient};
use sandbox::{PodmanSandbox, SandboxSpec};
use serde_json::{Value, json};
use std::sync::Arc;
use std::time::Duration;

const IMAGE: &str = "localhost/almanac-fixer-polyglot:eval";
const LOG_LIMIT: usize = 200 * 1024;
const FIXER: Grant = Grant {
    write: true,
    egress: false,
    secrets: false,
};
/// The repository's own checks, as far as they can run with no network.
const VERIFY: &str = "cd /work; status=0; \
    if [ -f pnpm-lock.yaml ]; then \
      pnpm install --offline --frozen-lockfile --ignore-scripts --store-dir /work/.cache/pnpm || status=1; \
      for script in typecheck lint test build; do \
        pnpm -r --if-present run \"$script\" || status=1; \
      done; \
    fi; \
    find . -name go.mod -not -path './node_modules/*' -not -path './.cache/*' | while read -r mod; do \
      (cd \"$(dirname \"$mod\")\" && go build ./... && go test ./...) || exit 1; \
    done || status=1; \
    exit $status";

struct Tarballs(Arc<GithubClient>);

impl Checkouts for Tarballs {
    fn tarball<'a>(
        &'a self,
        repo: &'a str,
        sha: &'a str,
    ) -> BoxFuture<'a, Result<Vec<u8>, StepError>> {
        Box::pin(async move { Ok(self.0.tarball(repo, sha).await?) })
    }
}

fn spec(base: SandboxSpec) -> SandboxSpec {
    SandboxSpec {
        memory_mb: 8192,
        work_mb: 8192,
        pids: 2048,
        env: [
            ("CARGO_HOME", "/work/.cargo"),
            ("GOPATH", "/work/.cache/gopath"),
            ("GOMODCACHE", "/work/.cache/go"),
            ("GOCACHE", "/work/.cache/gobuild"),
            ("XDG_CACHE_HOME", "/work/.cache"),
            ("COREPACK_HOME", "/work/.cache/corepack"),
            ("COREPACK_ENABLE_DOWNLOAD_PROMPT", "0"),
            ("GOTOOLCHAIN", "local"),
            ("npm_config_store_dir", "/work/.cache/pnpm"),
            ("pnpm_config_store_dir", "/work/.cache/pnpm"),
            ("PNPM_STORE_DIR", "/work/.cache/pnpm"),
        ]
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .to_vec(),
        ..base
    }
}

async fn logs(client: &GithubClient, repo: &str, sha: &str) -> Result<String, StepError> {
    let found = client.check_runs(repo, sha).await?;
    let runs = found["check_runs"].as_array().cloned().unwrap_or_default();
    let mut text = String::new();
    for run in &runs {
        if run["conclusion"] != json!("failure") {
            continue;
        }
        let name = run["name"].as_str().unwrap_or_default();
        text.push_str(&format!("=== {name}\n"));
        if let Some(id) = run["id"].as_u64()
            && let Ok(log) = client.job_logs(repo, id).await
        {
            let start = log.len().saturating_sub(LOG_LIMIT);
            let start = (start..log.len()).find(|i| log.is_char_boundary(*i));
            text.push_str(&log[start.unwrap_or(0)..]);
            text.push('\n');
        }
    }
    Ok(text)
}

async fn attempt(
    client: &GithubClient,
    harness: &ClaudeHarness,
    repo: &str,
    number: u64,
    out: &str,
) -> Result<Value, StepError> {
    let pull = client.pull(repo, number).await?;
    let sha = pull["head"]["sha"].as_str().unwrap_or_default().to_string();
    let head: Sha = sha.parse()?;
    let slug: Slug = repo.parse()?;
    let log_text = logs(client, repo, &sha).await?;
    let prompt = FixerPrompt { repo: slug }.render();
    let input = json!({
        "logs": {"$untrusted": log_text},
        CHECKOUT_FIELD: {"repo": repo, "sha": sha},
    });
    let session = harness.session(&prompt, FIXER, input).await?;
    let patch: Patch = serde_json::from_value(session.clone())?;
    let name = repo.replace('/', "-");
    std::fs::write(format!("{out}/patches/{name}-{number}.diff"), &patch.diff)
        .map_err(|e| StepError(e.to_string()))?;
    let verdict = DEFAULT_POLICY.check(&Untrusted::new(patch.clone()), &head);
    Ok(json!({
        "repo": repo,
        "number": number,
        "head": sha,
        "answer": session["answer"],
        "usage": session["usage"],
        "verified": session["verified"],
        "files": patch.changes.len(),
        "policy": match verdict {
            Ok(_) => "accepted".to_string(),
            Err(rejection) => format!("{rejection:?}"),
        },
    }))
}

/// Finishes at once, so a stub run proves checkout, fetch, and verify with no model.
fn stub_agent(_: &Value) -> Turn {
    Turn::Text(r#"{"summary": "stub"}"#.to_string())
}

#[tokio::main]
async fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let (list, out) = (&args[0], &args[1]);
    let stub = args.iter().any(|a| a == "--stub");
    std::fs::create_dir_all(format!("{out}/patches")).unwrap();
    let targets: Vec<Value> =
        serde_json::from_str(&std::fs::read_to_string(list).unwrap()).unwrap();
    let token = std::env::var("GITHUB_TOKEN").expect("GITHUB_TOKEN");
    let api = "https://api.github.com";
    let client = Arc::new(GithubClient::new(api, Auth::Token(token)).read_only());

    let model = if stub {
        Some(StubModel::answering(stub_agent).await)
    } else {
        None
    };
    let mut config = ClaudeConfig::new(if stub { "stub" } else { "" });
    config.base_url = model.as_ref().map(|m| m.url.clone());
    config.isolation_flags = vec![
        "--setting-sources=".to_string(),
        "--disable-slash-commands".to_string(),
    ];
    config.home = std::env::var("HOME").ok();
    config.model = Some("sonnet".to_string());
    config.timeout = Duration::from_secs(1500);
    config.max_turns = 80;
    config.token_budget = 6_000_000;
    config.max_budget_usd = Some(6.0);
    config.verify = Some(VERIFY.to_string());
    let harness = ClaudeHarness::new(config)
        .with_sandbox(Arc::new(PodmanSandbox), spec(SandboxSpec::agent(IMAGE)))
        .with_checkouts(Arc::new(Tarballs(client.clone())))
        .with_fetching(spec(SandboxSpec::fetch(IMAGE, vec![])));

    let mut results = vec![];
    for target in &targets {
        let repo = target["repo"].as_str().unwrap();
        let number = target["number"].as_u64().unwrap();
        eprintln!("attempting {number}");
        let started = std::time::Instant::now();
        let result = attempt(&client, &harness, repo, number, out).await;
        let mut row = match result {
            Ok(row) => row,
            Err(StepError(error)) => json!({"repo": repo, "number": number, "error": error}),
        };
        row["seconds"] = json!(started.elapsed().as_secs());
        results.push(row);
        let text = serde_json::to_string_pretty(&results).unwrap();
        std::fs::write(format!("{out}/results.json"), text).unwrap();
    }
}
