//! `File` and `Ask` as GitHub issues.
use almanac::prelude::*;
use serde::Deserialize;
use serde_json::json;

const LABEL: &str = "almanac";
const PAGE: usize = 100;
const USER_AGENT: &str = "almanac";

/// GitHub offers no idempotency key, so each issue carries its key in a marker and a retry
/// looks for that marker first.
pub struct GithubIssues {
    client: reqwest::Client,
    base_url: String,
    token: String,
}

#[derive(Deserialize)]
struct Issue {
    body: Option<String>,
}

impl GithubIssues {
    pub fn new(base_url: impl Into<String>, token: impl Into<String>) -> Self {
        Self {
            client: reqwest::Client::new(),
            base_url: base_url.into().trim_end_matches('/').to_string(),
            token: token.into(),
        }
    }

    fn request(&self, method: reqwest::Method, url: String) -> reqwest::RequestBuilder {
        self.client
            .request(method, url)
            .bearer_auth(&self.token)
            .header("Accept", "application/vnd.github+json")
            .header("User-Agent", USER_AGENT)
    }

    async fn already_filed(&self, target: &Slug, marker: &str) -> Result<bool, StepError> {
        for page in 1.. {
            let url = format!(
                "{}/repos/{target}/issues?state=all&labels={LABEL}&per_page={PAGE}&page={page}",
                self.base_url
            );
            let response = self.request(reqwest::Method::GET, url).send().await;
            let issues: Vec<Issue> = checked(response).await?.json().await.map_err(failed)?;
            if issues
                .iter()
                .any(|i| i.body.as_deref().is_some_and(|b| b.contains(marker)))
            {
                return Ok(true);
            }
            if issues.len() < PAGE {
                break;
            }
        }
        Ok(false)
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
        let url = format!("{}/repos/{target}/issues", self.base_url);
        let issue = json!({
            "title": title,
            "body": format!("{}\n\n{marker}\n", quoted(body)?),
            "labels": [LABEL],
        });
        let response = self
            .request(reqwest::Method::POST, url)
            .json(&issue)
            .send()
            .await;
        checked(response).await.map(|_| ())
    }
}

fn failed(e: reqwest::Error) -> StepError {
    StepError(e.to_string())
}

async fn checked(
    response: Result<reqwest::Response, reqwest::Error>,
) -> Result<reqwest::Response, StepError> {
    let response = response.map_err(failed)?;
    let status = response.status();
    if status.is_success() {
        return Ok(response);
    }
    Err(StepError(format!("GitHub answered {status}")))
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
