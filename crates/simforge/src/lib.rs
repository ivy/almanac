//! A local GitHub for tests: the HTTP subset Almanac uses, a scenario API to script it, and
//! signed webhook delivery with failure injection. It keeps no state past the process and
//! checks only that a token is present.
mod api;
mod webhooks;
mod world;

pub use webhooks::{Fate, sign};
pub use world::{Failure, Pull, Repo, Suite, Write};

use std::sync::{Arc, Mutex};
use tokio::net::TcpListener;
use world::World;

/// Renovate's login in the operator's repositories, used as the default author.
pub const RENOVATE: &str = "ivy-s-renovate-bot[bot]";

#[derive(Clone)]
pub struct SimForge {
    pub(crate) world: Arc<Mutex<World>>,
    pub(crate) client: reqwest::Client,
    base_url: String,
}

impl SimForge {
    /// Starts on a free local port. The server stops when the test's runtime does.
    pub async fn start() -> Self {
        let world: Arc<Mutex<World>> = Arc::default();
        let listener = TcpListener::bind("127.0.0.1:0").await.expect("a free port");
        let base_url = format!("http://{}", listener.local_addr().expect("a bound port"));
        world.lock().unwrap().base_url = base_url.clone();
        let app = api::router(world.clone());
        tokio::spawn(async move { axum::serve(listener, app).await });
        Self {
            world,
            client: reqwest::Client::new(),
            base_url,
        }
    }

    pub fn base_url(&self) -> &str {
        &self.base_url
    }

    pub fn add_repo(&self, repo: Repo) -> &Self {
        self.world.lock().unwrap().add_repo(repo);
        self
    }

    /// Adds an open pull request and returns its number. Sends no webhook: use `opened`.
    pub fn add_pull(&self, repo: &str, pull: Pull) -> u64 {
        self.world.lock().unwrap().add_pull(repo, pull)
    }

    pub fn add_suite(&self, repo: &str, suite: Suite) -> u64 {
        self.world.lock().unwrap().add_suite(repo, suite)
    }

    pub fn pull(&self, repo: &str, number: u64) -> Pull {
        self.world.lock().unwrap().pull(repo, number).clone()
    }

    pub fn add_label(&self, repo: &str, number: u64, label: &str) {
        let mut world = self.world.lock().unwrap();
        world.pull_mut(repo, number).labels.push(label.to_string());
    }

    /// `mergeable` reads as null for the next `reads` reads, as GitHub computes it lazily.
    pub fn mergeable_after(&self, reads: u32) {
        self.world.lock().unwrap().unknown_reads = reads;
    }

    /// Moves a branch as someone else's push would.
    pub fn move_branch(&self, repo: &str, number: u64, sha: &str, message: &str) {
        let mut world = self.world.lock().unwrap();
        world.commits.insert(sha.to_string(), message.to_string());
        world.pull_mut(repo, number).head_sha = sha.to_string();
    }

    /// The next requests matching `path_contains` fail, in order.
    pub fn fail_requests(&self, path_contains: &str, failures: &[Failure]) {
        let mut world = self.world.lock().unwrap();
        for failure in failures {
            world
                .failures
                .push_back((path_contains.to_string(), failure.clone()));
        }
    }

    /// Every write received, oldest first.
    pub fn writes(&self) -> Vec<Write> {
        self.world.lock().unwrap().writes.clone()
    }
}
