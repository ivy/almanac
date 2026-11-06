//! `File` and `Ask` as GitHub issues.
use crate::client::{Auth, GithubClient};
use almanac::prelude::*;
use serde_json::json;

const LABEL: &str = "almanac";

/// GitHub offers no idempotency key, so each issue carries its key in a marker and a retry
/// looks for that marker first.
pub struct GithubIssues {
    client: GithubClient,
}

impl GithubIssues {
    pub fn new(base_url: impl Into<String>, token: impl Into<String>) -> Self {
        Self::using(GithubClient::new(base_url, Auth::Token(token.into())))
    }

    pub fn using(client: GithubClient) -> Self {
        Self { client }
    }

    async fn already_filed(&self, target: &Slug, marker: &str) -> Result<bool, StepError> {
        let path = format!("/repos/{target}/issues?state=all&labels={LABEL}");
        let issues = self.client.get_all(&path).await?;
        let carries =
            |issue: &serde_json::Value| issue["body"].as_str().is_some_and(|b| b.contains(marker));
        Ok(issues.iter().any(carries))
    }

    async fn open(
        &self,
        key: &str,
        target: &Slug,
        title: &str,
        body: &Untrusted<String>,
    ) -> Result<(), StepError> {
        let marker = format!("<!-- almanac:{key} -->");
        if self.already_filed(target, &marker).await? {
            return Ok(());
        }
        let issue = json!({
            "title": title,
            "body": format!("{}\n\n{marker}\n", quoted(body)?),
            "labels": [LABEL],
        });
        let path = format!("/repos/{target}/issues");
        self.client.post(&path, &issue).await?;
        Ok(())
    }
}

/// Shows tainted text as inert code. The fence outruns any backticks in the text, so the text
/// cannot close it, and mentions and markup inside it do nothing.
fn quoted(body: &Untrusted<String>) -> Result<String, StepError> {
    let tagged = serde_json::to_value(body)?;
    let text = tagged["$untrusted"].as_str().unwrap_or_default();
    let longest = text
        .split(|c| c != '`')
        .map(str::len)
        .max()
        .unwrap_or_default();
    let fence = "`".repeat(longest.max(2) + 1);
    Ok(format!("{fence}text\n{text}\n{fence}"))
}

impl Tracker for GithubIssues {
    fn file<'a>(
        &'a self,
        key: &'a str,
        target: &'a Slug,
        title: &'a str,
        body: &'a Untrusted<String>,
    ) -> BoxFuture<'a, Result<(), StepError>> {
        Box::pin(self.open(key, target, title, body))
    }

    fn ask<'a>(
        &'a self,
        key: &'a str,
        target: &'a Slug,
        question: &'a str,
        body: &'a Untrusted<String>,
    ) -> BoxFuture<'a, Result<(), StepError>> {
        Box::pin(self.open(key, target, question, body))
    }
}
