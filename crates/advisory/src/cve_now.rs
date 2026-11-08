//! cve.now's feed. There is no spec to generate from here, so these are hand-written types, read
//! field by field (R-39). The API takes uppercase IDs and redirects anything else.
use crate::{Advisories, Advisory, Affected, Range, Severity};
use almanac::prelude::*;
use reqwest::StatusCode;
use serde_json::Value;

const USER_AGENT: &str = "almanac";
const SEARCH_LIMIT: usize = 20;

pub struct CveNow {
    client: reqwest::Client,
    base_url: String,
}

fn failed(e: impl std::fmt::Display) -> StepError {
    StepError(e.to_string())
}

fn slug(value: &Value) -> Option<Slug> {
    value.as_str()?.parse().ok()
}

fn severity(entry: &Value) -> Severity {
    match entry["severity"]["cvss"].as_f64() {
        Some(score) if score >= 9.0 => Severity::Critical,
        Some(score) if score >= 7.0 => Severity::High,
        Some(score) if score >= 4.0 => Severity::Medium,
        Some(_) => Severity::Low,
        None => Severity::Unknown,
    }
}

fn range(value: &Value) -> Range {
    Range {
        introduced: slug(&value["introduced"]),
        fixed: slug(&value["fixed"]),
    }
}

/// Entries that name a vendor and product, and not a package, say nothing about a dependency.
fn affected(value: &Value) -> Option<Affected> {
    let ranges = value["ranges"].as_array()?;
    Some(Affected {
        ecosystem: slug(&value["ecosystem"])?,
        package: slug(&value["package"])?,
        ranges: ranges.iter().map(range).collect(),
    })
}

fn advisory(entry: &Value) -> Result<Advisory, StepError> {
    let id = slug(&entry["id"]).ok_or_else(|| failed("an entry has no usable ID"))?;
    let list = |key: &str| entry[key].as_array().cloned().unwrap_or_default();
    let summary = entry["summary"].as_str().unwrap_or_default().to_string();
    Ok(Advisory {
        id,
        aliases: list("aliases").iter().filter_map(slug).collect(),
        severity: severity(entry),
        kev: !entry["exploitation"]["kev"].is_null(),
        affected: list("affected").iter().filter_map(affected).collect(),
        summary: Untrusted::new(summary),
    })
}

/// An HTTP client that gives up on a peer that stops answering, so a stalled connection fails the
/// request instead of holding its run forever. There is no overall deadline, because a large
/// download that keeps moving is not stalled.
fn http_client() -> reqwest::Client {
    reqwest::Client::builder()
        .connect_timeout(std::time::Duration::from_secs(10))
        .read_timeout(std::time::Duration::from_secs(30))
        .build()
        .expect("a client with only timeouts set always builds")
}

impl CveNow {
    pub fn new(base_url: impl Into<String>) -> Self {
        Self {
            client: http_client(),
            base_url: base_url.into().trim_end_matches('/').to_string(),
        }
    }

    async fn get(&self, path: &str) -> Result<Option<Value>, StepError> {
        let url = format!("{}{path}", self.base_url);
        let response = self
            .client
            .get(url)
            .header("User-Agent", USER_AGENT)
            .send()
            .await
            .map_err(failed)?;
        if response.status() == StatusCode::NOT_FOUND {
            return Ok(None);
        }
        let response = response.error_for_status().map_err(failed)?;
        response.json().await.map(Some).map_err(failed)
    }

    async fn entry(&self, id: &str) -> Result<Option<Advisory>, StepError> {
        let path = format!("/api/v1/entries/{}", id.to_uppercase());
        let found = self.get(&path).await?;
        found.as_ref().map(advisory).transpose()
    }

    async fn search(&self, ecosystem: &Slug, package: &Slug) -> Result<Vec<Advisory>, StepError> {
        let name = package.to_string().replace('@', "%40").replace('/', "%2F");
        let path =
            format!("/api/v1/search?package={name}&ecosystem={ecosystem}&limit={SEARCH_LIMIT}");
        let found = self.get(&path).await?.unwrap_or_default();
        let entries = found["entries"].as_array().cloned().unwrap_or_default();
        let mut advisories = vec![];
        for id in entries.iter().filter_map(|e| e["id"].as_str()) {
            if let Some(advisory) = self.entry(id).await? {
                advisories.push(advisory);
            }
        }
        Ok(advisories)
    }
}

impl Advisories for CveNow {
    fn by_id<'a>(&'a self, id: &'a Slug) -> BoxFuture<'a, Result<Option<Advisory>, StepError>> {
        Box::pin(async move { self.entry(&id.to_string()).await })
    }

    fn by_package<'a>(
        &'a self,
        ecosystem: &'a Slug,
        package: &'a Slug,
    ) -> BoxFuture<'a, Result<Vec<Advisory>, StepError>> {
        Box::pin(self.search(ecosystem, package))
    }
}
