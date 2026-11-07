//! The HTTP surface. Paths and shapes follow `specs/github`; `tests/shape.rs` checks them.
use crate::world::{Failure, Issue, Pull, Status, World, Write};
use axum::Router;
use axum::body::{Body, to_bytes};
use axum::extract::{Path, Query, Request, State};
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, patch, post};
use base64::Engine;
use serde_json::{Value, json};
use std::collections::HashMap;
use std::sync::{Arc, Mutex};

type Shared = Arc<Mutex<World>>;
type Params = Query<HashMap<String, String>>;

const DEFAULT_PAGE: usize = 30;
const MAX_BODY: usize = 16 * 1024 * 1024;

pub fn router(world: Shared) -> Router {
    Router::new()
        .route("/users/{owner}/repos", get(user_repos))
        .route("/user/repos", get(own_repos))
        .route("/installation/repositories", get(installation_repos))
        .route(
            "/app/installations/{id}/access_tokens",
            post(installation_token),
        )
        .route("/repos/{owner}/{repo}", get(repository))
        .route("/repos/{owner}/{repo}/pulls", get(pulls))
        .route("/repos/{owner}/{repo}/pulls/{number}", get(pull))
        .route(
            "/repos/{owner}/{repo}/pulls/{number}/commits",
            get(pull_commits),
        )
        .route(
            "/repos/{owner}/{repo}/pulls/{number}/files",
            get(pull_files),
        )
        .route("/repos/{owner}/{repo}/tarball/{sha}", get(tarball))
        .route("/repos/{owner}/{repo}/branches/{branch}", get(branch))
        .route("/repos/{owner}/{repo}/contents/{*path}", get(contents))
        .route(
            "/repos/{owner}/{repo}/commits/{sha}/check-suites",
            get(check_suites),
        )
        .route(
            "/repos/{owner}/{repo}/commits/{sha}/check-runs",
            get(check_runs),
        )
        .route(
            "/repos/{owner}/{repo}/commits/{sha}/status",
            get(combined_status),
        )
        .route(
            "/repos/{owner}/{repo}/actions/jobs/{id}/logs",
            get(job_logs),
        )
        .route("/repos/{owner}/{repo}/statuses/{sha}", post(post_status))
        .route("/repos/{owner}/{repo}/git/blobs", post(create_blob))
        .route("/repos/{owner}/{repo}/git/trees", post(create_tree))
        .route("/repos/{owner}/{repo}/git/commits", post(create_commit))
        .route("/repos/{owner}/{repo}/git/commits/{sha}", get(commit))
        .route(
            "/repos/{owner}/{repo}/git/refs/heads/{*branch}",
            patch(update_ref),
        )
        .route(
            "/repos/{owner}/{repo}/issues",
            get(issues).post(create_issue),
        )
        .route(
            "/repos/{owner}/{repo}/issues/{number}/comments",
            get(comments).post(create_comment),
        )
        .route(
            "/repos/{owner}/{repo}/issues/{number}/assignees",
            axum::routing::post(add_assignees),
        )
        .route("/app/hook/deliveries", get(deliveries))
        .route("/app/hook/deliveries/{id}/attempts", post(redeliver))
        .layer(middleware::from_fn_with_state(world.clone(), gatekeeper))
        .with_state(world)
}

/// Checks for a token, applies scripted failures, and records writes.
async fn gatekeeper(State(world): State<Shared>, request: Request, next: Next) -> Response {
    let path = request.uri().path().to_string();
    if !request.headers().contains_key(header::AUTHORIZATION) {
        let body = json!({"message": "Requires authentication"});
        return (StatusCode::UNAUTHORIZED, axum::Json(body)).into_response();
    }
    if let Some(failure) = take_failure(&world, &path) {
        return failed(failure);
    }
    let (parts, body) = request.into_parts();
    let bytes = to_bytes(body, MAX_BODY).await.unwrap_or_default();
    if parts.method != axum::http::Method::GET {
        let write = Write {
            method: parts.method.to_string(),
            path,
            body: serde_json::from_slice(&bytes).unwrap_or(Value::Null),
        };
        world.lock().unwrap().writes.push(write);
    }
    next.run(Request::from_parts(parts, Body::from(bytes)))
        .await
}

fn take_failure(world: &Shared, path: &str) -> Option<Failure> {
    let mut world = world.lock().unwrap();
    let at = world.failures.iter().position(|(p, _)| path.contains(p))?;
    world.failures.remove(at).map(|(_, failure)| failure)
}

fn failed(failure: Failure) -> Response {
    match failure {
        Failure::ServerError(code) => {
            let status = StatusCode::from_u16(code).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
            (status, axum::Json(json!({"message": "Server Error"}))).into_response()
        }
        Failure::RateLimited {
            retry_after_seconds,
        } => {
            let mut headers = HeaderMap::new();
            headers.insert(header::RETRY_AFTER, retry_after_seconds.into());
            headers.insert("x-ratelimit-remaining", HeaderValue::from_static("0"));
            let body = json!({"message": "API rate limit exceeded"});
            (StatusCode::TOO_MANY_REQUESTS, headers, axum::Json(body)).into_response()
        }
    }
}

fn missing() -> Response {
    (
        StatusCode::NOT_FOUND,
        axum::Json(json!({"message": "Not Found"})),
    )
        .into_response()
}

/// One page of `items`, with a `Link` header as GitHub writes it. `key` is `page`, or `cursor`
/// for endpoints that use one.
fn paged(
    world: &World,
    path: &str,
    key: &str,
    params: &HashMap<String, String>,
    items: Vec<Value>,
) -> Response {
    let per_page = params
        .get("per_page")
        .and_then(|p| p.parse().ok())
        .unwrap_or(DEFAULT_PAGE)
        .max(1);
    let page: usize = params.get(key).and_then(|p| p.parse().ok()).unwrap_or(1);
    let last = items.len().div_ceil(per_page).max(1);
    let shown: Vec<Value> = items
        .into_iter()
        .skip((page - 1) * per_page)
        .take(per_page)
        .collect();
    let url = |n: usize| format!("<{}{path}?per_page={per_page}&{key}={n}>", world.base_url);
    let mut links = vec![];
    if page < last {
        links.push(format!("{}; rel=\"next\"", url(page + 1)));
        links.push(format!("{}; rel=\"last\"", url(last)));
    }
    if page > 1 {
        links.push(format!("{}; rel=\"prev\"", url(page - 1)));
        links.push(format!("{}; rel=\"first\"", url(1)));
    }
    let mut headers = HeaderMap::new();
    if let (false, Ok(link)) = (links.is_empty(), HeaderValue::from_str(&links.join(", "))) {
        headers.insert(header::LINK, link);
    }
    (headers, axum::Json(Value::Array(shown))).into_response()
}

async fn user_repos(
    State(world): State<Shared>,
    Path(owner): Path<String>,
    Query(params): Params,
) -> Response {
    let world = world.lock().unwrap();
    let items = world
        .repos
        .values()
        .filter(|r| r.owner() == owner)
        .map(|r| world.repo_json(r))
        .collect();
    paged(
        &world,
        &format!("/users/{owner}/repos"),
        "page",
        &params,
        items,
    )
}

async fn own_repos(State(world): State<Shared>, Query(params): Params) -> Response {
    let world = world.lock().unwrap();
    let items = world.repos.values().map(|r| world.repo_json(r)).collect();
    paged(&world, "/user/repos", "page", &params, items)
}

async fn installation_repos(State(world): State<Shared>, Query(params): Params) -> Response {
    let world = world.lock().unwrap();
    let number = |key: &str, default: usize| {
        let found = params.get(key).and_then(|v| v.parse().ok());
        found.unwrap_or(default)
    };
    let per_page = number("per_page", DEFAULT_PAGE).max(1);
    let page = number("page", 1).max(1);
    let all: Vec<Value> = world.repos.values().map(|r| world.repo_json(r)).collect();
    let total = all.len();
    let repos: Vec<Value> = all
        .into_iter()
        .skip((page - 1) * per_page)
        .take(per_page)
        .collect();
    axum::Json(json!({"total_count": total, "repositories": repos})).into_response()
}

async fn installation_token() -> Response {
    let body = json!({"token": "ghs_simulated", "expires_at": "2099-01-01T00:00:00Z"});
    (StatusCode::CREATED, axum::Json(body)).into_response()
}

async fn commit(
    State(world): State<Shared>,
    Path((_, _, sha)): Path<(String, String, String)>,
) -> Response {
    let world = world.lock().unwrap();
    let Some(message) = world.commits.get(&sha) else {
        return missing();
    };
    let who = json!({"name": "almanac", "email": "a@b", "date": "2026-11-05T12:00:00Z"});
    axum::Json(json!({
        "sha": sha, "node_id": "C_1", "url": format!("/commits/{sha}"),
        "html_url": format!("/commit/{sha}"), "author": who, "committer": who,
        "message": message, "parents": [], "tree": {"sha": "t", "url": "/tree"},
        "verification": {"verified": false, "reason": "unsigned", "signature": null, "payload": null, "verified_at": null},
    }))
    .into_response()
}

async fn repository(
    State(world): State<Shared>,
    Path((owner, repo)): Path<(String, String)>,
) -> Response {
    let world = world.lock().unwrap();
    match world.repos.get(&format!("{owner}/{repo}")) {
        Some(r) => axum::Json(world.repo_json(r)).into_response(),
        None => missing(),
    }
}

async fn pulls(
    State(world): State<Shared>,
    Path((owner, repo)): Path<(String, String)>,
    Query(params): Params,
) -> Response {
    let world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let Some(r) = world.repos.get(&name) else {
        return missing();
    };
    let wanted = params.get("state").map_or("open", String::as_str);
    let head = params.get("head").and_then(|h| h.split_once(':'));
    let on_head = |p: &Pull| head.is_none_or(|(_, branch)| p.head_ref == branch);
    let items = world
        .pulls
        .iter()
        .filter(|((n, _), p)| *n == name && (wanted == "all" || p.state == wanted))
        .filter(|(_, p)| on_head(p))
        .map(|(_, p)| world.pull_simple_json(r, p))
        .collect();
    paged(
        &world,
        &format!("/repos/{name}/pulls"),
        "page",
        &params,
        items,
    )
}

async fn pull(
    State(world): State<Shared>,
    Path((owner, repo, number)): Path<(String, String, u64)>,
) -> Response {
    let mut world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let found = match (
        world.repos.get(&name),
        world.pulls.get(&(name.clone(), number)),
    ) {
        (Some(r), Some(p)) => axum::Json(world.pull_json(r, p)).into_response(),
        _ => missing(),
    };
    world.unknown_reads = world.unknown_reads.saturating_sub(1);
    found
}

async fn pull_commits(
    State(world): State<Shared>,
    Path((owner, repo, number)): Path<(String, String, u64)>,
    Query(params): Params,
) -> Response {
    let world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let Some(pull) = world.pulls.get(&(name.clone(), number)) else {
        return missing();
    };
    let who = json!({"name": "a", "email": "a@b", "date": "2026-11-05T12:00:00Z"});
    let items = pull
        .commits
        .iter()
        .map(|message| {
            json!({
                "sha": "0".repeat(40), "node_id": "C_1", "url": "/c", "html_url": "/c",
                "comments_url": "/c", "parents": [],
                "commit": {
                    "url": "/c", "author": who, "committer": who, "message": message,
                    "comment_count": 0, "tree": {"sha": "t", "url": "/t"},
                },
            })
        })
        .collect();
    let path = format!("/repos/{name}/pulls/{number}/commits");
    paged(&world, &path, "page", &params, items)
}

async fn pull_files(
    State(world): State<Shared>,
    Path((owner, repo, number)): Path<(String, String, u64)>,
    Query(params): Params,
) -> Response {
    let world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let Some(pull) = world.pulls.get(&(name.clone(), number)) else {
        return missing();
    };
    let file = |f: &String| {
        let mut item = json!({"filename": f});
        if let Some(diff) = pull.diffs.get(f) {
            item["patch"] = json!(diff);
        }
        item
    };
    let items = pull.files.iter().map(file).collect();
    let path = format!("/repos/{name}/pulls/{number}/files");
    paged(&world, &path, "page", &params, items)
}

async fn tarball(
    State(world): State<Shared>,
    Path((owner, repo, _)): Path<(String, String, String)>,
) -> Response {
    let world = world.lock().unwrap();
    match world.tarballs.get(&format!("{owner}/{repo}")) {
        Some(bytes) => bytes.clone().into_response(),
        None => missing(),
    }
}

async fn branch(
    State(world): State<Shared>,
    Path((owner, repo, branch)): Path<(String, String, String)>,
) -> Response {
    let world = world.lock().unwrap();
    let Some(r) = world.repos.get(&format!("{owner}/{repo}")) else {
        return missing();
    };
    axum::Json(json!({
        "name": branch,
        "protected": !r.required_checks.is_empty(),
        "protection": {"required_status_checks": {"contexts": r.required_checks}},
    }))
    .into_response()
}

async fn contents(
    State(world): State<Shared>,
    Path((owner, repo, path)): Path<(String, String, String)>,
) -> Response {
    let world = world.lock().unwrap();
    let found = world.repos.get(&format!("{owner}/{repo}"));
    match found.and_then(|r| r.renovate.as_ref()) {
        Some(text) if path == "renovate.json" => axum::Json(json!({
            "type": "file", "encoding": "base64", "name": path, "path": path, "size": text.len(),
            "sha": "0", "url": "/f", "git_url": null, "html_url": null, "download_url": null,
            "content": base64::engine::general_purpose::STANDARD.encode(text),
            "_links": {"self": "/f", "git": null, "html": null},
        }))
        .into_response(),
        _ => missing(),
    }
}

async fn check_suites(
    State(world): State<Shared>,
    Path((owner, repo, sha)): Path<(String, String, String)>,
) -> Response {
    let world = world.lock().unwrap();
    let suites: Vec<Value> = world
        .suites
        .get(&format!("{owner}/{repo}"))
        .into_iter()
        .flatten()
        .filter(|s| s.sha == sha)
        .map(|s| world.suite_json(s))
        .collect();
    axum::Json(json!({"total_count": suites.len(), "check_suites": suites})).into_response()
}

async fn check_runs(
    State(world): State<Shared>,
    Path((owner, repo, sha)): Path<(String, String, String)>,
) -> Response {
    let world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let runs: Vec<Value> = world
        .suites
        .get(&name)
        .into_iter()
        .flatten()
        .filter(|s| s.sha == sha)
        .map(|s| world.check_run_json(&name, s))
        .collect();
    axum::Json(json!({"total_count": runs.len(), "check_runs": runs})).into_response()
}

async fn combined_status(
    State(world): State<Shared>,
    Path((owner, repo, sha)): Path<(String, String, String)>,
) -> Response {
    let world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let Some(r) = world.repos.get(&name) else {
        return missing();
    };
    let statuses: Vec<&Status> = world
        .statuses
        .get(&name)
        .into_iter()
        .flatten()
        .filter(|s| s.sha == sha)
        .collect();
    let state = if statuses
        .iter()
        .any(|s| s.state == "failure" || s.state == "error")
    {
        "failure"
    } else if statuses.iter().all(|s| s.state == "success") && !statuses.is_empty() {
        "success"
    } else {
        "pending"
    };
    let shown: Vec<Value> = statuses.iter().map(|s| world.status_json(s)).collect();
    axum::Json(json!({
        "state": state, "sha": sha, "total_count": shown.len(), "statuses": shown,
        "repository": world.repo_json(r),
        "commit_url": format!("{}/repos/{name}/commits/{sha}", world.base_url),
        "url": format!("{}/repos/{name}/commits/{sha}/status", world.base_url),
    }))
    .into_response()
}

async fn job_logs(
    State(world): State<Shared>,
    Path((owner, repo, id)): Path<(String, String, u64)>,
) -> Response {
    let world = world.lock().unwrap();
    let suites = world.suites.get(&format!("{owner}/{repo}"));
    match suites.into_iter().flatten().find(|s| s.id == id) {
        Some(suite) => suite.logs.clone().into_response(),
        None => missing(),
    }
}

async fn post_status(
    State(world): State<Shared>,
    Path((owner, repo, sha)): Path<(String, String, String)>,
    axum::Json(body): axum::Json<Value>,
) -> Response {
    let mut world = world.lock().unwrap();
    let status = Status {
        sha,
        context: body["context"].as_str().unwrap_or("default").to_string(),
        state: body["state"].as_str().unwrap_or_default().to_string(),
        description: body["description"].as_str().map(str::to_string),
    };
    let json = world.status_json(&status);
    let statuses = world.statuses.entry(format!("{owner}/{repo}")).or_default();
    statuses.retain(|s| s.sha != status.sha || s.context != status.context);
    statuses.push(status);
    (StatusCode::CREATED, axum::Json(json)).into_response()
}

async fn create_blob(State(world): State<Shared>) -> Response {
    let sha = world.lock().unwrap().next_sha();
    let body = json!({"sha": sha, "url": format!("/blobs/{sha}")});
    (StatusCode::CREATED, axum::Json(body)).into_response()
}

async fn create_tree(State(world): State<Shared>) -> Response {
    let sha = world.lock().unwrap().next_sha();
    let body = json!({"sha": sha, "url": format!("/trees/{sha}"), "tree": [], "truncated": false});
    (StatusCode::CREATED, axum::Json(body)).into_response()
}

async fn create_commit(
    State(world): State<Shared>,
    axum::Json(request): axum::Json<Value>,
) -> Response {
    let sha = {
        let mut world = world.lock().unwrap();
        let sha = world.next_sha();
        let message = request["message"].as_str().unwrap_or_default().to_string();
        world.commits.insert(sha.clone(), message);
        sha
    };
    let who =
        json!({"name": "almanac", "email": "almanac@localhost", "date": "2026-11-05T12:00:00Z"});
    let body = json!({
        "sha": sha, "node_id": "C_1", "url": format!("/commits/{sha}"),
        "html_url": format!("/commit/{sha}"),
        "author": request.get("author").cloned().unwrap_or_else(|| who.clone()),
        "committer": request.get("committer").cloned().unwrap_or(who),
        "message": request["message"], "parents": [],
        "tree": {"sha": request["tree"], "url": "/tree"},
        "verification": {"verified": false, "reason": "unsigned", "signature": null, "payload": null, "verified_at": null},
    });
    (StatusCode::CREATED, axum::Json(body)).into_response()
}

async fn update_ref(
    State(world): State<Shared>,
    Path((owner, repo, branch)): Path<(String, String, String)>,
    axum::Json(body): axum::Json<Value>,
) -> Response {
    let mut world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let sha = body["sha"].as_str().unwrap_or_default().to_string();
    let key = (name.clone(), branch.clone());
    if !world.refs.contains_key(&key) {
        return (
            StatusCode::UNPROCESSABLE_ENTITY,
            axum::Json(json!({"message": "Reference does not exist"})),
        )
            .into_response();
    }
    world.refs.insert(key, sha.clone());
    let message = world.commits.get(&sha).cloned().unwrap_or_default();
    let moved = world
        .pulls
        .iter_mut()
        .filter(|((n, _), p)| *n == name && p.head_ref == branch);
    for (_, pull) in moved {
        pull.head_sha = sha.clone();
        pull.commits.push(message.clone());
    }
    axum::Json(json!({
        "ref": format!("refs/heads/{branch}"), "node_id": "REF_1", "url": "/ref",
        "object": {"type": "commit", "sha": sha, "url": "/commit"},
    }))
    .into_response()
}

async fn issues(
    State(world): State<Shared>,
    Path((owner, repo)): Path<(String, String)>,
    Query(params): Params,
) -> Response {
    let world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let label = params.get("labels");
    let items = world
        .issues
        .get(&name)
        .into_iter()
        .flatten()
        .filter(|i| label.is_none_or(|l| i.labels.contains(l)))
        .map(|i| world.issue_json(&name, i))
        .collect();
    paged(
        &world,
        &format!("/repos/{name}/issues"),
        "page",
        &params,
        items,
    )
}

async fn create_issue(
    State(world): State<Shared>,
    Path((owner, repo)): Path<(String, String)>,
    axum::Json(body): axum::Json<Value>,
) -> Response {
    let mut world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let taken = world.pulls.keys().filter(|(r, _)| *r == name).count();
    let issues = world.issues.entry(name.clone()).or_default();
    let labels = body["labels"].as_array().into_iter().flatten();
    let issue = Issue {
        number: (taken + issues.len() + 1) as u64,
        title: body["title"].as_str().unwrap_or_default().to_string(),
        body: body["body"].as_str().map(str::to_string),
        labels: labels
            .filter_map(|l| l.as_str().map(str::to_string))
            .collect(),
    };
    issues.push(issue.clone());
    let json = world.issue_json(&name, &issue);
    (StatusCode::CREATED, axum::Json(json)).into_response()
}

async fn comments(
    State(world): State<Shared>,
    Path((owner, repo, number)): Path<(String, String, u64)>,
    Query(params): Params,
) -> Response {
    let world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let thread = world.comments.get(&(name.clone(), number));
    let bodies = thread.into_iter().flatten();
    let items = bodies.map(|body| world.comment_json(&name, number, body));
    let path = format!("/repos/{name}/issues/{number}/comments");
    paged(&world, &path, "page", &params, items.collect())
}

async fn create_comment(
    State(world): State<Shared>,
    Path((owner, repo, number)): Path<(String, String, u64)>,
    axum::Json(body): axum::Json<Value>,
) -> Response {
    let mut world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let text = body["body"].as_str().unwrap_or_default().to_string();
    let thread = world.comments.entry((name.clone(), number)).or_default();
    thread.push(text.clone());
    let json = world.comment_json(&name, number, &text);
    (StatusCode::CREATED, axum::Json(json)).into_response()
}

async fn add_assignees(
    State(world): State<Shared>,
    Path((owner, repo, number)): Path<(String, String, u64)>,
    axum::Json(body): axum::Json<Value>,
) -> Response {
    let mut world = world.lock().unwrap();
    let name = format!("{owner}/{repo}");
    let added = body["assignees"].as_array().into_iter().flatten();
    let logins: Vec<String> = added
        .filter_map(|l| l.as_str().map(str::to_string))
        .collect();
    let assigned = world.assignees.entry((name.clone(), number)).or_default();
    assigned.extend(logins);
    let issue = Issue {
        number,
        title: String::new(),
        body: None,
        labels: vec![],
    };
    let json = world.issue_json(&name, &issue);
    (StatusCode::CREATED, axum::Json(json)).into_response()
}

async fn deliveries(State(world): State<Shared>, Query(params): Params) -> Response {
    let world = world.lock().unwrap();
    let items = world
        .deliveries
        .iter()
        .rev()
        .map(|d| world.delivery_json(d))
        .collect();
    paged(&world, "/app/hook/deliveries", "cursor", &params, items)
}

async fn redeliver(State(world): State<Shared>, Path(id): Path<u64>) -> Response {
    let found = {
        let world = world.lock().unwrap();
        world.deliveries.iter().find(|d| d.id == id).cloned()
    };
    let Some(delivery) = found else {
        return missing();
    };
    crate::webhooks::redeliver(world, delivery).await;
    (StatusCode::ACCEPTED, axum::Json(json!({}))).into_response()
}
