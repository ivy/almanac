//! Everything this almanac talks to, set up once from the environment. A provider that is not
//! configured is left out, so `check` names what is missing.
use crate::update::{Ages, RegistryAges};
use crate::vetting::{RealVetting, Vetting};
use crate::workflows::build_failures::ScanTarget;
use advisory::CveNow;
use almanac::prelude::*;
use claude::{Checkouts, ClaudeConfig, ClaudeHarness};
use github::client::{Auth, GithubClient};
use github::forge::{CommitAuthor, GithubForge};
use github::tracker::GithubIssues;
use registry::{Endpoints, HttpRegistry, PackageRegistry};
use sandbox::{PodmanSandbox, Sandbox, SandboxSpec};
use std::env;
use std::sync::Arc;

const API: &str = "https://api.github.com";
const CVE_NOW: &str = "https://cve.now";
const DEFAULT_AUTHOR: &str = "ivy-s-renovate-bot[bot]";
const SANDBOX_IMAGE: &str = "localhost/almanac-fixer:latest";
/// Stops a runaway session. It is a ceiling, not a target.
const MAX_SESSION_USD: f64 = 6.0;

/// Reads commits for the harness.
struct GithubCheckouts(Arc<GithubClient>);

impl Checkouts for GithubCheckouts {
    fn tarball<'a>(
        &'a self,
        repo: &'a str,
        sha: &'a str,
    ) -> BoxFuture<'a, Result<Vec<u8>, StepError>> {
        Box::pin(async move { Ok(self.0.tarball(repo, sha).await?) })
    }
}

fn auth() -> Option<Auth> {
    if let (Ok(app_id), Ok(private_key), Ok(installation)) = (
        env::var("GITHUB_APP_ID"),
        env::var("GITHUB_APP_PRIVATE_KEY"),
        env::var("GITHUB_APP_INSTALLATION_ID"),
    ) {
        let installation_id = installation.parse().ok()?;
        return Some(Auth::App {
            app_id,
            private_key,
            installation_id,
        });
    }
    env::var("GITHUB_TOKEN").ok().map(Auth::Token)
}

/// Set for a pass over real repositories. Every client refuses writes and no model is called,
/// whatever mode a run is in.
fn read_only() -> bool {
    env::var("ALMANAC_READ_ONLY").is_ok_and(|v| v == "1")
}

fn client() -> Option<GithubClient> {
    let api = env::var("GITHUB_API_URL").unwrap_or_else(|_| API.to_string());
    let client = GithubClient::new(api, auth()?);
    Some(if read_only() {
        client.read_only()
    } else {
        client
    })
}

/// For verbs that only look: it refuses writes whatever else is set.
pub fn read_only_client() -> Option<GithubClient> {
    client().map(GithubClient::read_only)
}

/// Stands in for an agent when nothing may be spent, so a run records how far it got.
struct NoAgent;

impl Harness for NoAgent {
    fn run<'a>(
        &'a self,
        _: &'a Pass,
        _: &'a str,
        _: almanac::agent::Grant,
        _: serde_json::Value,
    ) -> BoxFuture<'a, Result<serde_json::Value, StepError>> {
        Box::pin(async { Err(StepError("read-only: an agent would run here".to_string())) })
    }
}

fn harness(checkouts: Arc<GithubClient>) -> Option<ClaudeHarness> {
    let mut config = ClaudeConfig::new(env::var("ANTHROPIC_API_KEY").ok()?);
    config.base_url = env::var("ANTHROPIC_BASE_URL").ok();
    config.max_budget_usd = Some(MAX_SESSION_USD);
    let sandbox: Arc<dyn Sandbox> = Arc::new(PodmanSandbox);
    let image = env::var("ALMANAC_SANDBOX_IMAGE").unwrap_or_else(|_| SANDBOX_IMAGE.to_string());
    let cache = |spec: SandboxSpec| SandboxSpec {
        env: vec![
            ("CARGO_HOME".to_string(), "/work/.cargo".to_string()),
            ("GOPATH".to_string(), "/work/.cache/gopath".to_string()),
            ("GOMODCACHE".to_string(), "/work/.cache/go".to_string()),
            ("GOCACHE".to_string(), "/work/.cache/gobuild".to_string()),
            ("GOTOOLCHAIN".to_string(), "local".to_string()),
            (
                "npm_config_cache".to_string(),
                "/work/.cache/npm".to_string(),
            ),
            ("npm_config_prefer_offline".to_string(), "true".to_string()),
            ("XDG_CACHE_HOME".to_string(), "/work/.cache".to_string()),
            (
                "COREPACK_HOME".to_string(),
                "/work/.cache/corepack".to_string(),
            ),
            (
                "COREPACK_ENABLE_DOWNLOAD_PROMPT".to_string(),
                "0".to_string(),
            ),
            (
                "npm_config_store_dir".to_string(),
                "/work/.cache/pnpm".to_string(),
            ),
            (
                "pnpm_config_store_dir".to_string(),
                "/work/.cache/pnpm".to_string(),
            ),
            (
                "PNPM_STORE_DIR".to_string(),
                "/work/.cache/pnpm".to_string(),
            ),
        ],
        ..spec
    };
    let fetching = cache(SandboxSpec::fetch(&image, vec![]));
    Some(
        ClaudeHarness::new(config)
            .with_sandbox(sandbox, cache(SandboxSpec::agent(&image)))
            .with_checkouts(Arc::new(GithubCheckouts(checkouts)))
            .with_fetching(fetching),
    )
}

/// Where registries are. `ALMANAC_REGISTRY_URL` points them all at one mirror.
pub fn registry_endpoints() -> Endpoints {
    match env::var("ALMANAC_REGISTRY_URL") {
        Ok(url) => Endpoints {
            npm: url.clone(),
            crates: url.clone(),
            pypi: url.clone(),
            go: url.clone(),
            github: url.clone(),
            docker: url,
        },
        Err(_) => Endpoints::default(),
    }
}

pub fn all(_config: &Config) -> Providers {
    let registry: Arc<dyn PackageRegistry> = Arc::new(HttpRegistry::new(registry_endpoints()));
    let ages: Arc<dyn Ages> = Arc::new(RegistryAges(registry.clone()));
    let vetting: Arc<dyn Vetting> = Arc::new(RealVetting {
        advisories: Arc::new(CveNow::new(
            env::var("ALMANAC_ADVISORIES_URL").unwrap_or_else(|_| CVE_NOW.to_string()),
        )),
        registry,
    });
    let mut providers = Providers::new()
        .provide::<dyn Ages>(ages)
        .provide::<dyn Vetting>(vetting);
    let bot = env::var("ALMANAC_BOT_LOGIN").unwrap_or_else(|_| CommitAuthor::default().name);
    providers = providers.acting_as(&[&bot]);
    if let Some(target) = env::var("ALMANAC_ALERTS_REPO")
        .ok()
        .and_then(|r| r.parse().ok())
    {
        providers = providers.alerting(target);
    }
    let owner = env::var("ALMANAC_OWNER").unwrap_or_default();
    let author = env::var("ALMANAC_RENOVATE_AUTHOR").unwrap_or_else(|_| DEFAULT_AUTHOR.to_string());
    providers = providers.provide(Arc::new(ScanTarget { owner, author }));
    let (Some(reader), Some(writer), Some(tracker)) = (client(), client(), client()) else {
        return providers;
    };
    let reader = Arc::new(reader);
    let sweep: Arc<dyn Sweep> = reader.clone();
    let forge: Arc<dyn Forge> = Arc::new(GithubForge::new(writer, CommitAuthor::default()));
    let tracker: Arc<dyn Tracker> = Arc::new(GithubIssues::using(tracker));
    providers = providers
        .provide::<GithubClient>(reader.clone())
        .provide::<dyn Forge>(forge)
        .provide::<dyn Tracker>(tracker)
        .provide::<dyn Sweep>(sweep);
    if read_only() {
        return providers.harness(NoAgent);
    }
    match harness(reader) {
        Some(harness) => providers.harness(harness),
        None => providers,
    }
}
