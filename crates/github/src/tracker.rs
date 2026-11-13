//! `File` as a GitHub issue. `Ask` reaches a person on the pull request it is about, with a comment
//! and an assignment, or as an issue when it is about no pull request. `Resolve` closes an issue
//! almanac filed, with a comment holding the evidence.
use crate::client::{Auth, GithubClient};
use almanac::prelude::*;
use serde_json::json;

const LABEL: &str = "almanac";

/// GitHub offers no idempotency key, so each issue carries its key in a marker and a retry
/// looks for that marker first.
pub struct GithubIssues {
    client: GithubClient,
    person: Option<String>,
}

impl GithubIssues {
    pub fn new(base_url: impl Into<String>, token: impl Into<String>) -> Self {
        Self::using(GithubClient::new(base_url, Auth::Token(token.into())))
    }

    pub fn using(client: GithubClient) -> Self {
        Self {
            client,
            person: None,
        }
    }

    /// Who a question goes to, as a GitHub login. They are mentioned and assigned.
    pub fn asking(mut self, person: impl Into<String>) -> Self {
        self.person = Some(person.into());
        self
    }

    fn mention(&self) -> String {
        let person = self.person.as_deref();
        person.map_or_else(String::new, |login| format!("@{login} "))
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
        text: String,
        assignees: &[&str],
    ) -> Result<(), StepError> {
        let marker = format!("<!-- almanac:{key} -->");
        if self.already_filed(target, &marker).await? {
            return Ok(());
        }
        let issue = json!({
            "title": title,
            "body": format!("{text}\n\n{marker}\n"),
            "labels": [LABEL],
            "assignees": assignees,
        });
        let path = format!("/repos/{target}/issues");
        self.client.post(&path, &issue).await?;
        Ok(())
    }

    /// Assigning comes first and is safe to repeat, so a retry after a crash between the two
    /// still assigns, and the marker keeps it to one comment.
    async fn ask_on_pull(
        &self,
        key: &str,
        target: &Slug,
        pull: u64,
        question: &str,
        body: &Untrusted<String>,
    ) -> Result<(), StepError> {
        let thread = format!("/repos/{target}/issues/{pull}");
        if let Some(person) = &self.person {
            let assignees = json!({"assignees": [person]});
            let path = format!("{thread}/assignees");
            self.client.post(&path, &assignees).await?;
        }
        let path = format!("{thread}/comments");
        let marker = format!("<!-- almanac:{key} -->");
        let comments = self.client.get_all(&path).await?;
        let carries =
            |c: &serde_json::Value| c["body"].as_str().is_some_and(|b| b.contains(&marker));
        if comments.iter().any(carries) {
            return Ok(());
        }
        let text = format!(
            "{}{question}\n\n{}\n\n{marker}\n",
            self.mention(),
            quoted(body)?
        );
        self.client.post(&path, &json!({"body": text})).await?;
        Ok(())
    }

    async fn ask_by_issue(
        &self,
        key: &str,
        target: &Slug,
        question: &str,
        body: &Untrusted<String>,
    ) -> Result<(), StepError> {
        let text = format!("{}{question}\n\n{}", self.mention(), quoted(body)?);
        let assignees: Vec<&str> = self.person.iter().map(String::as_str).collect();
        self.open(key, target, question, text, &assignees).await
    }

    /// The issues `item` names. Almanac closes only issues it filed, so a named issue without the
    /// label is refused rather than closed.
    async fn resolving(
        &self,
        target: &Slug,
        item: &Resolving,
    ) -> Result<Vec<serde_json::Value>, StepError> {
        match item {
            Resolving::Named(name) => {
                let Ok(number) = name.to_string().parse::<u64>() else {
                    return Err(StepError(format!(
                        "`{name}` is not an issue number, so there is no issue in {target} to close; name a GitHub issue by its number"
                    )));
                };
                let issue = self
                    .client
                    .get(&format!("/repos/{target}/issues/{number}"))
                    .await?;
                let labelled = issue["labels"]
                    .as_array()
                    .is_some_and(|labels| labels.iter().any(|l| l["name"].as_str() == Some(LABEL)));
                if !labelled {
                    return Err(StepError(format!(
                        "{target}#{number} has no `{LABEL}` label, so almanac did not file it and will not close it; close it by hand if it is done"
                    )));
                }
                Ok(vec![issue])
            }
            Resolving::Filed(keys) if keys.is_empty() => Ok(vec![]),
            Resolving::Filed(keys) => {
                let path = format!("/repos/{target}/issues?state=all&labels={LABEL}");
                let issues = self.client.get_all(&path).await?;
                let markers: Vec<String> = keys
                    .iter()
                    .map(|k| format!("<!-- almanac:{k} -->"))
                    .collect();
                let carries = |issue: &serde_json::Value| {
                    let body = issue["body"].as_str().unwrap_or_default();
                    markers.iter().any(|m| body.contains(m.as_str()))
                };
                Ok(issues.into_iter().filter(carries).collect())
            }
        }
    }

    /// Comments before closing, and the marker keeps it to one comment, so a retry after a crash
    /// between the two still closes.
    async fn close(
        &self,
        key: &str,
        target: &Slug,
        issue: &serde_json::Value,
        evidence: &str,
    ) -> Result<(), StepError> {
        if issue["state"].as_str() == Some("closed") {
            return Ok(());
        }
        let Some(number) = issue["number"].as_u64() else {
            return Err(StepError(format!(
                "an issue in {target} came back with no number, so it cannot be closed; this is a bug in almanac or a change in GitHub's API"
            )));
        };
        let thread = format!("/repos/{target}/issues/{number}");
        let path = format!("{thread}/comments");
        let marker = format!("<!-- almanac:{key} -->");
        let comments = self.client.get_all(&path).await?;
        let carries =
            |c: &serde_json::Value| c["body"].as_str().is_some_and(|b| b.contains(&marker));
        if !comments.iter().any(carries) {
            let shown = quoted(&Untrusted::new(evidence.to_string()))?;
            let text = format!("Closing on this evidence:\n\n{shown}\n\n{marker}\n");
            self.client.post(&path, &json!({"body": text})).await?;
        }
        let closed = json!({"state": "closed", "state_reason": "completed"});
        self.client.patch(&thread, &closed).await?;
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
    if text.is_empty() {
        return Ok(String::new());
    }
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
        Box::pin(async move { self.open(key, target, title, quoted(body)?, &[]).await })
    }

    fn ask<'a>(
        &'a self,
        key: &'a str,
        target: &'a Slug,
        pull: Option<u64>,
        question: &'a str,
        body: &'a Untrusted<String>,
    ) -> BoxFuture<'a, Result<(), StepError>> {
        Box::pin(async move {
            match pull {
                Some(pull) => self.ask_on_pull(key, target, pull, question, body).await,
                None => self.ask_by_issue(key, target, question, body).await,
            }
        })
    }

    fn resolve<'a>(
        &'a self,
        key: &'a str,
        target: &'a Slug,
        item: &'a Resolving,
        evidence: &'a str,
    ) -> BoxFuture<'a, Result<(), StepError>> {
        Box::pin(async move {
            for issue in self.resolving(target, item).await? {
                self.close(key, target, &issue, evidence).await?;
            }
            Ok(())
        })
    }
}
