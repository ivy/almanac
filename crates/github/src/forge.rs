//! `Forge` on GitHub: statuses, and pushing a checked patch to a pull request's own branch.
use crate::client::GithubClient;
use almanac::diag::Error;
use almanac::prelude::*;
use serde_json::{Value, json};

/// The author of every commit Almanac makes. Renovate's `gitIgnoredAuthors` matches on this,
/// so it must not vary, or automerge stops on every patched branch.
#[derive(Clone, Debug)]
pub struct CommitAuthor {
    pub name: String,
    pub email: String,
}

impl Default for CommitAuthor {
    fn default() -> Self {
        Self {
            name: "almanac[bot]".to_string(),
            email: "almanac[bot]@users.noreply.github.com".to_string(),
        }
    }
}

pub struct GithubForge {
    client: GithubClient,
    author: CommitAuthor,
}

/// How a retry finds a commit it already made.
fn trailer(key: &str) -> String {
    format!("Almanac-Key: {key}")
}

fn refused(push: &Push, reason: impl Into<String>) -> Error {
    Error::PushRefused {
        branch: push.branch.to_string(),
        reason: reason.into(),
    }
}

/// Reads a checked patch at the provider boundary. Its text goes only to GitHub's API.
fn opened(patch: &Untrusted<Patch>) -> Result<Patch, StepError> {
    let tagged = serde_json::to_value(patch)?;
    Ok(serde_json::from_value(tagged["$untrusted"].clone())?)
}

impl GithubForge {
    pub fn new(client: GithubClient, author: CommitAuthor) -> Self {
        Self { client, author }
    }

    async fn committed_already(&self, push: &Push, key: &str, head: &str) -> Result<bool, Error> {
        let path = format!("/repos/{}/git/commits/{head}", push.repo);
        let commit = self.client.get(&path).await;
        // A commit we cannot read is not one we can claim, so it counts as someone else's.
        let message = commit.map(|c| c["message"].as_str().unwrap_or_default().to_string());
        Ok(message.is_ok_and(|m| m.contains(&trailer(key))))
    }

    /// Verifies the target, or reports that this push already happened.
    async fn verify(&self, push: &Push, key: &str) -> Result<bool, Error> {
        let pull = self.client.pull(&push.repo.to_string(), push.pull).await?;
        let repo = push.repo.to_string();
        if pull["head"]["ref"] != json!(push.branch.to_string()) {
            return Err(refused(push, "it is not this pull request's branch"));
        }
        if pull["head"]["repo"]["full_name"] != json!(repo) {
            return Err(refused(push, "the pull request is from a fork"));
        }
        if pull["state"] != json!("open") {
            return Err(refused(push, "the pull request is not open"));
        }
        let head = pull["head"]["sha"].as_str().unwrap_or_default();
        if head == push.base.to_string() {
            return Ok(false);
        }
        if self.committed_already(push, key, head).await? {
            return Ok(true);
        }
        Err(refused(
            push,
            "the branch moved after the patch was checked",
        ))
    }

    async fn tree_entry(&self, repo: &str, change: &Change) -> Result<Value, StepError> {
        Ok(match change {
            Change::Delete { path } => {
                json!({"path": path, "mode": "100644", "type": "blob", "sha": null})
            }
            Change::Write {
                path,
                content,
                executable,
            } => {
                let blob = json!({"content": content, "encoding": "base64"});
                let created = self
                    .client
                    .post(&format!("/repos/{repo}/git/blobs"), &blob)
                    .await?;
                let mode = if *executable { "100755" } else { "100644" };
                json!({"path": path, "mode": mode, "type": "blob", "sha": created["sha"]})
            }
            Change::Link { .. } => {
                return Err(StepError("a checked patch cannot hold a link".to_string()));
            }
        })
    }

    async fn commit(&self, key: &str, push: &Push) -> Result<(), StepError> {
        if self.verify(push, key).await? {
            return Ok(());
        }
        let repo = push.repo.to_string();
        let patch = opened(&push.patch)?;
        let mut entries = vec![];
        for change in &patch.changes {
            entries.push(self.tree_entry(&repo, change).await?);
        }
        let base = push.base.to_string();
        let tree = json!({"base_tree": base, "tree": entries});
        let tree = self
            .client
            .post(&format!("/repos/{repo}/git/trees"), &tree)
            .await?;
        let who = json!({"name": self.author.name, "email": self.author.email});
        let commit = json!({
            "message": format!("{}\n\n{}", push.message, trailer(key)),
            "tree": tree["sha"],
            "parents": [base],
            "author": who,
            "committer": who,
        });
        let commit = self
            .client
            .post(&format!("/repos/{repo}/git/commits"), &commit)
            .await?;
        // Set before the branch moves, so the new commit is never unheld.
        if let Some(context) = &push.hold {
            let status = json!({"state": "pending", "context": context});
            let sha = commit["sha"].as_str().unwrap_or_default();
            self.client
                .post(&format!("/repos/{repo}/statuses/{sha}"), &status)
                .await?;
        }
        // Never forced: if the branch moved in the meantime, GitHub refuses this.
        let update = json!({"sha": commit["sha"], "force": false});
        let path = format!("/repos/{repo}/git/refs/heads/{}", push.branch);
        self.client.patch(&path, &update).await?;
        Ok(())
    }
}

fn state_name(state: AttestState) -> &'static str {
    match state {
        AttestState::Pending => "pending",
        AttestState::Success => "success",
        AttestState::Failure => "failure",
    }
}

impl Forge for GithubForge {
    fn attest<'a>(
        &'a self,
        _key: &'a str,
        repo: &'a Slug,
        sha: &'a Sha,
        context: &'a str,
        state: AttestState,
    ) -> BoxFuture<'a, Result<(), StepError>> {
        Box::pin(async move {
            let status = json!({"state": state_name(state), "context": context});
            let path = format!("/repos/{repo}/statuses/{sha}");
            self.client.post(&path, &status).await?;
            Ok(())
        })
    }

    fn push<'a>(&'a self, key: &'a str, push: &'a Push) -> BoxFuture<'a, Result<(), StepError>> {
        Box::pin(self.commit(key, push))
    }
}

impl Sweep for GithubClient {
    fn sweep(&self) -> BoxFuture<'_, Result<usize, StepError>> {
        Box::pin(async move { Ok(self.redeliver_failures().await?) })
    }
}
