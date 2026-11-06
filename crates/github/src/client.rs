//! GitHub's REST API: authentication, pagination, and rate limits, for every provider here.
use almanac::diag::Error;
use base64::Engine;
use jsonwebtoken::{Algorithm, EncodingKey, Header};
use reqwest::{Method, Response, StatusCode};
use serde::Serialize;
use serde_json::{Value, json};
use std::sync::Mutex;
use std::time::Duration;
use time::OffsetDateTime;

const USER_AGENT: &str = "almanac";
const PAGE: usize = 100;
/// A receiver that was down for a while should not be flooded in one sweep.
const MOST_REDELIVERIES: usize = 20;
/// GitHub allows an App token at most ten minutes, and clocks drift.
const JWT_LIFETIME_SECONDS: i64 = 540;
const JWT_BACKDATE_SECONDS: i64 = 60;
/// A token this close to expiry is replaced before use.
const TOKEN_MARGIN_SECONDS: i64 = 120;

/// How hard to try before reporting a failure.
#[derive(Clone, Copy, Debug)]
pub struct Retry {
    pub attempts: u32,
    /// The first wait after a server error, doubled each time.
    pub backoff: Duration,
    /// A rate limit asking for longer than this is reported, not waited out.
    pub longest_wait: Duration,
}

impl Default for Retry {
    fn default() -> Self {
        Self {
            attempts: 5,
            backoff: Duration::from_millis(500),
            longest_wait: Duration::from_secs(60),
        }
    }
}

pub enum Auth {
    /// A personal token, for read-only use.
    Token(String),
    App {
        app_id: String,
        /// PEM, from the environment.
        private_key: String,
        installation_id: u64,
    },
}

struct Installation {
    token: String,
    expires_at: OffsetDateTime,
}

pub struct GithubClient {
    http: reqwest::Client,
    base_url: String,
    auth: Auth,
    read_only: bool,
    retry: Retry,
    installation: Mutex<Option<Installation>>,
}

#[derive(Serialize)]
struct Claims<'a> {
    iat: i64,
    exp: i64,
    iss: &'a str,
}

fn seconds(header: Option<&reqwest::header::HeaderValue>) -> Option<u64> {
    header?.to_str().ok()?.parse().ok()
}

/// How long a refusal asks us to wait, if it is a rate limit at all.
fn rate_limit_wait(response: &Response) -> Option<Duration> {
    let status = response.status();
    let headers = response.headers();
    let limited = status == StatusCode::TOO_MANY_REQUESTS
        || (status == StatusCode::FORBIDDEN
            && (headers.contains_key("retry-after")
                || seconds(headers.get("x-ratelimit-remaining")) == Some(0)));
    if !limited {
        return None;
    }
    if let Some(wait) = seconds(headers.get("retry-after")) {
        return Some(Duration::from_secs(wait));
    }
    let reset = seconds(headers.get("x-ratelimit-reset")).unwrap_or(0) as i64;
    let now = OffsetDateTime::now_utc().unix_timestamp();
    Some(Duration::from_secs((reset - now).max(1) as u64))
}

fn next_page(response: &Response, base_url: &str) -> Option<String> {
    let link = response.headers().get("link")?.to_str().ok()?;
    let next = link.split(',').find(|part| part.contains("rel=\"next\""))?;
    let url = next.split(['<', '>']).nth(1)?;
    url.starts_with(base_url).then(|| url.to_string())
}

impl GithubClient {
    pub fn new(base_url: impl Into<String>, auth: Auth) -> Self {
        Self {
            http: reqwest::Client::new(),
            base_url: base_url.into().trim_end_matches('/').to_string(),
            auth,
            read_only: false,
            retry: Retry::default(),
            installation: Mutex::default(),
        }
    }

    /// Refuses every write, whatever its caller believes. A dry run against real repositories
    /// should use this as well as `Mode::DryRun`.
    pub fn read_only(mut self) -> Self {
        self.read_only = true;
        self
    }

    pub fn retrying(mut self, retry: Retry) -> Self {
        self.retry = retry;
        self
    }

    fn jwt(&self, app_id: &str, private_key: &str) -> Result<String, Error> {
        let now = OffsetDateTime::now_utc().unix_timestamp();
        let claims = Claims {
            iat: now - JWT_BACKDATE_SECONDS,
            exp: now + JWT_LIFETIME_SECONDS,
            iss: app_id,
        };
        let failed = |e: jsonwebtoken::errors::Error| Error::ForgeUnreachable {
            action: "signing an App token".to_string(),
            reason: e.to_string(),
        };
        let key = EncodingKey::from_rsa_pem(private_key.as_bytes()).map_err(failed)?;
        jsonwebtoken::encode(&Header::new(Algorithm::RS256), &claims, &key).map_err(failed)
    }

    fn cached_installation_token(&self) -> Option<String> {
        let cached = self.installation.lock().unwrap();
        let installation = cached.as_ref()?;
        let left = installation.expires_at - OffsetDateTime::now_utc();
        (left.whole_seconds() > TOKEN_MARGIN_SECONDS).then(|| installation.token.clone())
    }

    /// `/app` endpoints take the App's own token. Everything else takes an installation's.
    async fn bearer(&self, path: &str) -> Result<String, Error> {
        let Auth::App {
            app_id,
            private_key,
            installation_id,
        } = &self.auth
        else {
            let Auth::Token(token) = &self.auth else {
                unreachable!("only two kinds of auth");
            };
            return Ok(token.clone());
        };
        let jwt = self.jwt(app_id, private_key)?;
        if path.starts_with("/app") {
            return Ok(jwt);
        }
        if let Some(token) = self.cached_installation_token() {
            return Ok(token);
        }
        let url = format!(
            "{}/app/installations/{installation_id}/access_tokens",
            self.base_url
        );
        let action = "getting an installation token";
        let response = self.send(Method::POST, &url, &jwt, None, action).await?;
        let body: Value = self.decoded(response, action).await?;
        let token = body["token"].as_str().unwrap_or_default().to_string();
        let expires_at = body["expires_at"]
            .as_str()
            .and_then(|t| {
                OffsetDateTime::parse(t, &time::format_description::well_known::Rfc3339).ok()
            })
            .unwrap_or_else(OffsetDateTime::now_utc);
        *self.installation.lock().unwrap() = Some(Installation {
            token: token.clone(),
            expires_at,
        });
        Ok(token)
    }

    async fn decoded(&self, response: Response, action: &str) -> Result<Value, Error> {
        response.json().await.map_err(|e| Error::ForgeUnreachable {
            action: action.to_string(),
            reason: e.to_string(),
        })
    }

    /// Sends with retries. A rate limit waits as asked, and a server error backs off.
    async fn send(
        &self,
        method: Method,
        url: &str,
        token: &str,
        body: Option<&Value>,
        action: &str,
    ) -> Result<Response, Error> {
        let mut backoff = self.retry.backoff;
        let mut last_failure = String::new();
        for attempt in 1..=self.retry.attempts {
            let mut request = self
                .http
                .request(method.clone(), url)
                .bearer_auth(token)
                .header("Accept", "application/vnd.github+json")
                .header("X-GitHub-Api-Version", "2022-11-28")
                .header("User-Agent", USER_AGENT);
            if let Some(body) = body {
                request = request.json(body);
            }
            let wait = match request.send().await {
                Err(e) => {
                    last_failure = e.to_string();
                    backoff
                }
                Ok(response) => {
                    if let Some(wait) = rate_limit_wait(&response) {
                        if wait > self.retry.longest_wait || attempt == self.retry.attempts {
                            return Err(Error::RateLimited {
                                action: action.to_string(),
                                wait_seconds: wait.as_secs(),
                            });
                        }
                        wait
                    } else if response.status().is_server_error() {
                        last_failure = format!("GitHub answered {}", response.status());
                        backoff
                    } else if response.status().is_success() {
                        return Ok(response);
                    } else {
                        return Err(Error::ForgeRefused {
                            action: action.to_string(),
                            status: response.status().as_u16(),
                        });
                    }
                }
            };
            backoff *= 2;
            if attempt < self.retry.attempts {
                tokio::time::sleep(wait).await;
            }
        }
        Err(Error::ForgeUnreachable {
            action: action.to_string(),
            reason: last_failure,
        })
    }

    async fn request(
        &self,
        method: Method,
        path: &str,
        body: Option<&Value>,
    ) -> Result<Response, Error> {
        let action = format!("{method} {path}");
        if self.read_only && method != Method::GET {
            return Err(Error::ReadOnly { action });
        }
        let token = self.bearer(path).await?;
        let url = format!("{}{path}", self.base_url);
        self.send(method, &url, &token, body, &action).await
    }

    pub async fn get(&self, path: &str) -> Result<Value, Error> {
        let response = self.request(Method::GET, path, None).await?;
        self.decoded(response, path).await
    }

    /// The body as text, for logs.
    pub async fn get_text(&self, path: &str) -> Result<String, Error> {
        let response = self.request(Method::GET, path, None).await?;
        response.text().await.map_err(|e| Error::ForgeUnreachable {
            action: path.to_string(),
            reason: e.to_string(),
        })
    }

    pub async fn post(&self, path: &str, body: &Value) -> Result<Value, Error> {
        let response = self.request(Method::POST, path, Some(body)).await?;
        self.decoded(response, path).await
    }

    /// For endpoints that answer with no body worth reading.
    pub async fn post_empty(&self, path: &str, body: &Value) -> Result<(), Error> {
        self.request(Method::POST, path, Some(body))
            .await
            .map(|_| ())
    }

    pub async fn patch(&self, path: &str, body: &Value) -> Result<Value, Error> {
        let response = self.request(Method::PATCH, path, Some(body)).await?;
        self.decoded(response, path).await
    }

    /// Every item of a list, following `Link` headers. `path` must not have a query of its own
    /// that sets `per_page`.
    pub async fn get_all(&self, path: &str) -> Result<Vec<Value>, Error> {
        let joiner = if path.contains('?') { '&' } else { '?' };
        let first = format!("{path}{joiner}per_page={PAGE}");
        let token = self.bearer(path).await?;
        let mut url = format!("{}{first}", self.base_url);
        let mut items = vec![];
        loop {
            let response = self.send(Method::GET, &url, &token, None, path).await?;
            let next = next_page(&response, &self.base_url);
            let page = self.decoded(response, path).await?;
            items.extend(page.as_array().cloned().unwrap_or_default());
            match next {
                Some(next) => url = next,
                None => return Ok(items),
            }
        }
    }

    /// This response is an object, so `get_all` does not apply.
    async fn installation_repositories(&self) -> Result<Vec<Value>, Error> {
        let mut all = vec![];
        for page in 1.. {
            let path = format!("/installation/repositories?per_page={PAGE}&page={page}");
            let found = self.get(&path).await?;
            let repositories = found["repositories"]
                .as_array()
                .cloned()
                .unwrap_or_default();
            let last = repositories.len() < PAGE;
            all.extend(repositories);
            if last {
                break;
            }
        }
        Ok(all)
    }

    /// Repositories with the given owner, leaving out archived ones.
    pub async fn repositories(&self, owner: &str) -> Result<Vec<Value>, Error> {
        let all = match self.auth {
            Auth::Token(_) => self.get_all("/user/repos?affiliation=owner").await?,
            Auth::App { .. } => self.installation_repositories().await?,
        };
        let wanted = |repo: &Value| {
            repo["owner"]["login"] == json!(owner) && repo["archived"] != json!(true)
        };
        Ok(all.into_iter().filter(wanted).collect())
    }

    /// Open pull requests in `repo` by `author`, as REST responses.
    pub async fn pulls_by(&self, repo: &str, author: &str) -> Result<Vec<Value>, Error> {
        let path = format!("/repos/{repo}/pulls?state=open");
        let by_author = |pull: &Value| pull["user"]["login"] == json!(author);
        let pulls = self.get_all(&path).await?;
        Ok(pulls.into_iter().filter(by_author).collect())
    }

    pub async fn pull(&self, repo: &str, number: u64) -> Result<Value, Error> {
        self.get(&format!("/repos/{repo}/pulls/{number}")).await
    }

    /// A pull request once GitHub has computed `mergeable`, which it does lazily. After `reads`
    /// tries it stays null, which a caller should treat as still waiting.
    pub async fn pull_settled(
        &self,
        repo: &str,
        number: u64,
        reads: u32,
        pause: Duration,
    ) -> Result<Value, Error> {
        let mut pull = self.pull(repo, number).await?;
        for _ in 1..reads {
            if !pull["mergeable"].is_null() {
                break;
            }
            tokio::time::sleep(pause).await;
            pull = self.pull(repo, number).await?;
        }
        Ok(pull)
    }

    pub async fn check_suites(&self, repo: &str, sha: &str) -> Result<Value, Error> {
        self.get(&format!("/repos/{repo}/commits/{sha}/check-suites"))
            .await
    }

    pub async fn check_runs(&self, repo: &str, sha: &str) -> Result<Value, Error> {
        self.get(&format!("/repos/{repo}/commits/{sha}/check-runs"))
            .await
    }

    pub async fn combined_status(&self, repo: &str, sha: &str) -> Result<Value, Error> {
        self.get(&format!("/repos/{repo}/commits/{sha}/status"))
            .await
    }

    pub async fn job_logs(&self, repo: &str, job: u64) -> Result<String, Error> {
        self.get_text(&format!("/repos/{repo}/actions/jobs/{job}/logs"))
            .await
    }

    /// The open pull request whose head is `branch` in the same repository, as a REST response.
    pub async fn pull_for_branch(&self, repo: &str, branch: &str) -> Result<Option<Value>, Error> {
        let owner = repo.split('/').next().unwrap_or_default();
        let path = format!("/repos/{repo}/pulls?state=open&head={owner}:{branch}");
        Ok(self.get_all(&path).await?.into_iter().next())
    }

    /// The paths a pull request changes.
    pub async fn pull_files(&self, repo: &str, number: u64) -> Result<Vec<String>, Error> {
        let files = self
            .get_all(&format!("/repos/{repo}/pulls/{number}/files"))
            .await?;
        let path = |f: &Value| f["filename"].as_str().map(str::to_string);
        Ok(files.iter().filter_map(path).collect())
    }

    /// A commit's source as a gzipped tar, with one top directory.
    pub async fn tarball(&self, repo: &str, sha: &str) -> Result<Vec<u8>, Error> {
        let path = format!("/repos/{repo}/tarball/{sha}");
        let response = self.request(Method::GET, &path, None).await?;
        let bytes = response.bytes().await;
        let bytes = bytes.map_err(|e| Error::ForgeUnreachable {
            action: path,
            reason: e.to_string(),
        })?;
        Ok(bytes.to_vec())
    }

    /// Messages of the commits on a pull request, oldest first.
    pub async fn pull_commit_messages(
        &self,
        repo: &str,
        number: u64,
    ) -> Result<Vec<String>, Error> {
        let commits = self
            .get_all(&format!("/repos/{repo}/pulls/{number}/commits"))
            .await?;
        let message = |c: &Value| c["commit"]["message"].as_str().map(str::to_string);
        Ok(commits.iter().filter_map(message).collect())
    }

    /// A file's text, or `None` if the repository does not have it.
    pub async fn file(&self, repo: &str, path: &str) -> Result<Option<String>, Error> {
        let found = match self.get(&format!("/repos/{repo}/contents/{path}")).await {
            Ok(found) => found,
            Err(Error::ForgeRefused { status: 404, .. }) => return Ok(None),
            Err(e) => return Err(e),
        };
        let text: String = found["content"]
            .as_str()
            .unwrap_or_default()
            .split_whitespace()
            .collect();
        let bytes = base64::engine::general_purpose::STANDARD
            .decode(text)
            .unwrap_or_default();
        Ok(Some(String::from_utf8_lossy(&bytes).into_owned()))
    }

    /// Status contexts a branch requires. A token that may not see protection reads as none.
    pub async fn required_checks(&self, repo: &str, branch: &str) -> Result<Vec<String>, Error> {
        let found = self
            .get(&format!("/repos/{repo}/branches/{branch}"))
            .await?;
        let contexts = found["protection"]["required_status_checks"]["contexts"].as_array();
        let names = contexts.into_iter().flatten().filter_map(Value::as_str);
        Ok(names.map(str::to_string).collect())
    }

    pub async fn deliveries(&self) -> Result<Vec<Value>, Error> {
        self.get_all("/app/hook/deliveries").await
    }

    /// Asks GitHub to resend recent deliveries that failed and have not since succeeded. GitHub
    /// never does this itself. Returns how many were asked for.
    pub async fn redeliver_failures(&self) -> Result<usize, Error> {
        let listed = self
            .get(&format!("/app/hook/deliveries?per_page={PAGE}"))
            .await?;
        let attempts = listed.as_array().cloned().unwrap_or_default();
        let succeeded = |d: &&Value| d["status_code"].as_u64().is_some_and(|c| c < 300);
        let delivered: Vec<&Value> = attempts
            .iter()
            .filter(succeeded)
            .map(|d| &d["guid"])
            .collect();
        let mut asked: Vec<&Value> = vec![];
        for attempt in &attempts {
            let guid = &attempt["guid"];
            if succeeded(&attempt) || delivered.contains(&guid) || asked.contains(&guid) {
                continue;
            }
            if let Some(id) = attempt["id"].as_u64() {
                self.redeliver(id).await?;
                asked.push(guid);
            }
            if asked.len() >= MOST_REDELIVERIES {
                break;
            }
        }
        Ok(asked.len())
    }

    pub async fn redeliver(&self, id: u64) -> Result<(), Error> {
        let path = format!("/app/hook/deliveries/{id}/attempts");
        self.post_empty(&path, &json!({})).await
    }
}
