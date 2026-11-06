//! Each registry's API, read over HTTP.
use crate::{Ecosystem, Package, PackageRegistry};
use almanac::prelude::*;
use reqwest::StatusCode;
use serde_json::Value;
use time::OffsetDateTime;
use time::format_description::well_known::Rfc3339;

const USER_AGENT: &str = "almanac";
const MAX_ARTIFACT_BYTES: u64 = 256 * 1024 * 1024;

/// Where each registry lives. Tests point these at a local server.
#[derive(Clone, Debug)]
pub struct Endpoints {
    pub npm: String,
    pub crates: String,
    pub pypi: String,
    pub go: String,
    pub github: String,
    pub docker: String,
}

impl Default for Endpoints {
    fn default() -> Self {
        Self {
            npm: "https://registry.npmjs.org".to_string(),
            crates: "https://crates.io".to_string(),
            pypi: "https://pypi.org".to_string(),
            go: "https://proxy.golang.org".to_string(),
            github: "https://api.github.com".to_string(),
            docker: "https://hub.docker.com".to_string(),
        }
    }
}

pub struct HttpRegistry {
    client: reqwest::Client,
    endpoints: Endpoints,
}

fn failed(e: impl std::fmt::Display) -> StepError {
    StepError(e.to_string())
}

fn timestamp(value: &Value) -> Published {
    value
        .as_str()
        .and_then(|text| OffsetDateTime::parse(text, &Rfc3339).ok())
        .map_or(Published::Unknown, Published::At)
}

/// Go escapes capitals in module paths, as `!` and the lowercase letter.
fn go_escaped(module: &str) -> String {
    let mut out = String::new();
    for c in module.chars() {
        if c.is_ascii_uppercase() {
            out.push('!');
            out.push(c.to_ascii_lowercase());
        } else {
            out.push(c);
        }
    }
    out
}

impl HttpRegistry {
    pub fn new(endpoints: Endpoints) -> Self {
        Self {
            client: reqwest::Client::new(),
            endpoints,
        }
    }

    /// `None` when the registry does not know the release.
    async fn json(&self, url: &str) -> Result<Option<Value>, StepError> {
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

    async fn download(&self, url: &str) -> Result<Vec<u8>, StepError> {
        let response = self
            .client
            .get(url)
            .header("User-Agent", USER_AGENT)
            .send()
            .await
            .and_then(reqwest::Response::error_for_status)
            .map_err(failed)?;
        if response.content_length().unwrap_or(0) > MAX_ARTIFACT_BYTES {
            return Err(StepError(format!("{url} is too large to fetch")));
        }
        let bytes = response.bytes().await.map_err(failed)?;
        if bytes.len() as u64 > MAX_ARTIFACT_BYTES {
            return Err(StepError(format!("{url} is too large to fetch")));
        }
        Ok(bytes.to_vec())
    }

    async fn published_at(&self, p: &Package) -> Result<Published, StepError> {
        let e = &self.endpoints;
        let (name, version) = (p.name.to_string(), p.version.to_string());
        let (url, pick): (String, fn(&Value, &str) -> Published) = match p.ecosystem {
            Ecosystem::Npm => (
                format!("{}/{}", e.npm, name.replace('/', "%2f")),
                |v, version| timestamp(&v["time"][version]),
            ),
            Ecosystem::Crates => (
                format!("{}/api/v1/crates/{name}/{version}", e.crates),
                |v, _| timestamp(&v["version"]["created_at"]),
            ),
            Ecosystem::PyPi => (format!("{}/pypi/{name}/{version}/json", e.pypi), |v, _| {
                let newest = v["urls"].as_array().into_iter().flatten();
                let times = newest.map(|u| timestamp(&u["upload_time_iso_8601"]));
                latest(times)
            }),
            Ecosystem::Go => (
                format!("{}/{}/@v/{version}.info", e.go, go_escaped(&name)),
                |v, _| timestamp(&v["Time"]),
            ),
            Ecosystem::GithubRelease => (
                format!("{}/repos/{name}/releases/tags/{version}", e.github),
                |v, _| timestamp(&v["published_at"]),
            ),
            Ecosystem::Docker => {
                let path = if name.contains('/') {
                    name.clone()
                } else {
                    format!("library/{name}")
                };
                (
                    format!("{}/v2/repositories/{path}/tags/{version}", e.docker),
                    |v, _| timestamp(&v["tag_last_pushed"]),
                )
            }
            Ecosystem::Unsupported => return Ok(Published::Unknown),
        };
        let found = self.json(&url).await?;
        Ok(found.map_or(Published::Unknown, |v| pick(&v, &version)))
    }

    async fn artifact_of(&self, p: &Package) -> Result<Vec<u8>, StepError> {
        let e = &self.endpoints;
        let (name, version) = (p.name.to_string(), p.version.to_string());
        let unknown = || StepError(format!("no artifact for {name}@{version}"));
        let url = match p.ecosystem {
            Ecosystem::Npm => {
                let index = format!("{}/{}", e.npm, name.replace('/', "%2f"));
                let found = self.json(&index).await?.ok_or_else(unknown)?;
                let tarball = found["versions"][&version]["dist"]["tarball"].as_str();
                tarball.ok_or_else(unknown)?.to_string()
            }
            Ecosystem::Crates => format!("{}/api/v1/crates/{name}/{version}/download", e.crates),
            Ecosystem::PyPi => {
                let index = format!("{}/pypi/{name}/{version}/json", e.pypi);
                let found = self.json(&index).await?.ok_or_else(unknown)?;
                let urls = found["urls"].as_array().cloned().unwrap_or_default();
                let sdist = urls.iter().find(|u| u["packagetype"] == "sdist");
                let url = sdist.and_then(|u| u["url"].as_str()).ok_or_else(unknown)?;
                url.to_string()
            }
            Ecosystem::Go => format!("{}/{}/@v/{version}.zip", e.go, go_escaped(&name)),
            Ecosystem::GithubRelease => {
                let index = format!("{}/repos/{name}/releases/tags/{version}", e.github);
                let found = self.json(&index).await?.ok_or_else(unknown)?;
                let url = found["tarball_url"].as_str().ok_or_else(unknown)?;
                url.to_string()
            }
            Ecosystem::Docker | Ecosystem::Unsupported => return Err(unknown()),
        };
        self.download(&url).await
    }
}

/// The newest of several upload times. One unknown time makes the release unknown, because the
/// file that matters might be the one without a time.
fn latest(times: impl Iterator<Item = Published>) -> Published {
    let mut newest = None;
    for time in times {
        let Published::At(at) = time else {
            return Published::Unknown;
        };
        newest = newest.max(Some(at));
    }
    newest.map_or(Published::Unknown, Published::At)
}

impl PackageRegistry for HttpRegistry {
    fn published<'a>(
        &'a self,
        package: &'a Package,
    ) -> BoxFuture<'a, Result<Published, StepError>> {
        Box::pin(self.published_at(package))
    }

    fn artifact<'a>(&'a self, package: &'a Package) -> BoxFuture<'a, Result<Vec<u8>, StepError>> {
        Box::pin(self.artifact_of(package))
    }
}
