//! Everything this almanac talks to, set up once from the environment. A provider that is not
//! configured is left out, so `check` names what is missing.
use crate::update::{Ages, RegistryAges};
use crate::workflows::build_failures::ScanTarget;
use almanac::prelude::*;
use claude::{Checkouts, ClaudeConfig, ClaudeHarness};
use github::client::{Auth, GithubClient};
use github::forge::{CommitAuthor, GithubForge};
use github::tracker::GithubIssues;
use registry::{Endpoints, HttpRegistry};
use sandbox::{PodmanSandbox, Sandbox, SandboxSpec};
use std::env;
use std::sync::Arc;

const API: &str = "https://api.github.com";
const DEFAULT_AUTHOR: &str = "ivy-s-renovate-bot[bot]";
const SANDBOX_IMAGE: &str = "localhost/almanac-fixer:latest";

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

fn client() -> Option<GithubClient> {
    let api = env::var("GITHUB_API_URL").unwrap_or_else(|_| API.to_string());
    Some(GithubClient::new(api, auth()?))
}

fn harness(checkouts: Arc<GithubClient>) -> Option<ClaudeHarness> {
    let mut config = ClaudeConfig::new(env::var("ANTHROPIC_API_KEY").ok()?);
    config.base_url = env::var("ANTHROPIC_BASE_URL").ok();
    let sandbox: Arc<dyn Sandbox> = Arc::new(PodmanSandbox);
    let image = env::var("ALMANAC_SANDBOX_IMAGE").unwrap_or_else(|_| SANDBOX_IMAGE.to_string());
    let cache = |spec: SandboxSpec| SandboxSpec {
        env: vec![
            ("CARGO_HOME".to_string(), "/work/.cargo".to_string()),
            ("GOMODCACHE".to_string(), "/work/.cache/go".to_string()),
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

pub fn all(_config: &Config) -> Providers {
    let ages: Arc<dyn Ages> = Arc::new(RegistryAges(Arc::new(HttpRegistry::new(
        Endpoints::default(),
    ))));
    let mut providers = Providers::new().provide::<dyn Ages>(ages);
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
    match harness(reader) {
        Some(harness) => providers.harness(harness),
        None => providers,
    }
}
