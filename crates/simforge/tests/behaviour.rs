use almanac::runtime::{Envelope, Ingest, Runtime};
use axum::Router;
use axum::body::Bytes;
use axum::http::{HeaderMap, StatusCode};
use axum::routing::post;
use serde_json::Value;
use simforge::{Failure, Fate, Pull, Repo, SimForge, Suite, sign};
use std::sync::{Arc, Mutex};

const REPO: &str = "owner/repo";

#[derive(Clone, Debug)]
struct Received {
    event: String,
    guid: String,
    signature: String,
    body: Bytes,
}

async fn receiver() -> (String, Arc<Mutex<Vec<Received>>>) {
    let received: Arc<Mutex<Vec<Received>>> = Arc::default();
    let log = received.clone();
    let app = Router::new().route(
        "/hook",
        post(move |headers: HeaderMap, body: Bytes| {
            let log = log.clone();
            async move {
                let text = |name: &str| headers[name].to_str().unwrap().to_string();
                log.lock().unwrap().push(Received {
                    event: text("x-github-event"),
                    guid: text("x-github-delivery"),
                    signature: text("x-hub-signature-256"),
                    body,
                });
                StatusCode::OK
            }
        }),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/hook", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, app).await });
    (url, received)
}

async fn scripted() -> (SimForge, u64) {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public(REPO));
    let number = sim.add_pull(REPO, Pull::renovate("renovate/lodash-4.x", "Update lodash"));
    (sim, number)
}

fn get(sim: &SimForge, path: &str) -> reqwest::RequestBuilder {
    reqwest::Client::new()
        .get(format!("{}{path}", sim.base_url()))
        .bearer_auth("token")
}

#[test]
fn signatures_match_githubs_documented_example() {
    let signature = sign("It's a Secret to Everybody", b"Hello, World!");
    assert_eq!(
        signature,
        "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
    );
}

#[tokio::test]
async fn a_webhook_is_signed_over_its_exact_body() {
    let (sim, number) = scripted().await;
    let (url, received) = receiver().await;
    sim.deliver_to(&url, "secret");
    let guid = sim.pull_request(REPO, number, "opened").await;
    let received = received.lock().unwrap();
    assert_eq!(received[0].event, "pull_request");
    assert_eq!(received[0].guid, guid);
    assert_eq!(received[0].signature, sign("secret", &received[0].body));
}

#[tokio::test]
async fn a_request_without_a_token_is_refused() {
    let (sim, _) = scripted().await;
    let url = format!("{}/repos/{REPO}", sim.base_url());
    let response = reqwest::get(url).await.unwrap();
    assert_eq!(response.status(), 401);
}

#[tokio::test]
async fn pagination_follows_link_headers_to_every_item() {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public(REPO));
    for i in 0..5 {
        sim.add_pull(REPO, Pull::renovate(&format!("renovate/p{i}"), "Update"));
    }
    let mut url = format!("{}/repos/{REPO}/pulls?per_page=2", sim.base_url());
    let mut seen = vec![];
    loop {
        let response = reqwest::Client::new()
            .get(&url)
            .bearer_auth("t")
            .send()
            .await
            .unwrap();
        let link = response
            .headers()
            .get("link")
            .map(|l| l.to_str().unwrap().to_string());
        let page: Vec<Value> = response.json().await.unwrap();
        seen.extend(page.iter().map(|p| p["number"].as_u64().unwrap()));
        let next = link.and_then(|l| {
            l.split(", ")
                .find(|part| part.ends_with("rel=\"next\""))
                .map(|part| part[1..part.find('>').unwrap()].to_string())
        });
        match next {
            Some(next) => url = next,
            None => break,
        }
    }
    assert_eq!(seen, [1, 2, 3, 4, 5]);
}

#[tokio::test]
async fn a_scenario_can_lose_duplicate_and_reorder_deliveries() {
    let (sim, number) = scripted().await;
    let (url, received) = receiver().await;
    sim.deliver_to(&url, "s");
    sim.next_deliveries(&[Fate::Lost, Fate::Duplicated, Fate::Held, Fate::Held]);

    let lost = sim.pull_request(REPO, number, "opened").await;
    let duplicated = sim.pull_request(REPO, number, "synchronize").await;
    let first = sim.pull_request(REPO, number, "edited").await;
    let second = sim.pull_request(REPO, number, "labeled").await;
    sim.release_held().await;

    let guids: Vec<_> = received
        .lock()
        .unwrap()
        .iter()
        .map(|r| r.guid.clone())
        .collect();
    assert!(!guids.contains(&lost));
    assert_eq!(guids, [duplicated.clone(), duplicated, second, first]);
}

#[tokio::test]
async fn a_lost_delivery_is_listed_failed_and_redelivered_with_its_id() {
    let (sim, number) = scripted().await;
    let (url, received) = receiver().await;
    sim.deliver_to(&url, "s");
    sim.next_deliveries(&[Fate::Lost]);
    let guid = sim.pull_request(REPO, number, "opened").await;

    let listed: Vec<Value> = get(&sim, "/app/hook/deliveries")
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert_eq!(listed[0]["guid"], guid.as_str());
    assert_eq!(listed[0]["status_code"], 502);

    let id = listed[0]["id"].as_u64().unwrap();
    let attempt = reqwest::Client::new()
        .post(format!(
            "{}/app/hook/deliveries/{id}/attempts",
            sim.base_url()
        ))
        .bearer_auth("t")
        .send()
        .await
        .unwrap();
    assert_eq!(attempt.status(), 202);
    assert_eq!(received.lock().unwrap()[0].guid, guid);
}

#[tokio::test]
async fn scripted_failures_answer_as_github_does_then_clear() {
    let (sim, _) = scripted().await;
    let limited = Failure::RateLimited {
        retry_after_seconds: 7,
    };
    sim.fail_requests("/pulls", &[limited, Failure::ServerError(502)]);
    let path = format!("/repos/{REPO}/pulls");
    let first = get(&sim, &path).send().await.unwrap();
    assert_eq!(first.status(), 429);
    assert_eq!(first.headers()["retry-after"], "7");
    assert_eq!(first.headers()["x-ratelimit-remaining"], "0");
    assert_eq!(get(&sim, &path).send().await.unwrap().status(), 502);
    assert_eq!(get(&sim, &path).send().await.unwrap().status(), 200);
}

#[tokio::test]
async fn every_write_is_recorded_and_a_push_moves_the_pull_request() {
    let (sim, number) = scripted().await;
    let before = sim.pull(REPO, number).head_sha;
    let client = reqwest::Client::new();
    let url = |path: &str| format!("{}/repos/{REPO}{path}", sim.base_url());
    let status = serde_json::json!({"state": "pending", "context": "almanac/trust"});
    client
        .post(url(&format!("/statuses/{before}")))
        .bearer_auth("t")
        .json(&status)
        .send()
        .await
        .unwrap();
    let moved = "a".repeat(40);
    client
        .patch(url("/git/refs/heads/renovate/lodash-4.x"))
        .bearer_auth("t")
        .json(&serde_json::json!({"sha": moved}))
        .send()
        .await
        .unwrap();
    let writes = sim.writes();
    assert_eq!(writes.len(), 2);
    assert_eq!(writes[0].body["context"], "almanac/trust");
    assert_eq!(sim.pull(REPO, number).head_sha, moved);
}

#[tokio::test]
async fn failure_logs_are_readable_through_the_check_run() {
    let (sim, number) = scripted().await;
    let sha = sim.pull(REPO, number).head_sha;
    let suite = Suite::finished(&sha, "renovate/lodash-4.x", "failure", "error: boom");
    sim.add_suite(REPO, suite);
    let runs: Value = get(&sim, &format!("/repos/{REPO}/commits/{sha}/check-runs"))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert_eq!(runs["check_runs"][0]["conclusion"], "failure");
    let id = runs["check_runs"][0]["id"].as_u64().unwrap();
    let logs = get(&sim, &format!("/repos/{REPO}/actions/jobs/{id}/logs"))
        .send()
        .await
        .unwrap()
        .text()
        .await
        .unwrap();
    assert_eq!(logs, "error: boom");
}

#[tokio::test]
async fn a_simulated_pull_request_event_parses_as_the_real_type() {
    use almanac::prelude::*;

    #[workflow(on = github::PullRequestOpened, key = "{repo}", intent = "x", may = [Await], runs_per_hour = 9, decide = policy)]
    async fn accepts(_cx: &Cx, _event: &github::PullRequestOpened) -> Result<(), Halt> {
        Ok(())
    }
    fn policy(_: &()) -> Vec<Effect> {
        vec![]
    }

    let (sim, number) = scripted().await;
    let (url, received) = receiver().await;
    sim.deliver_to(&url, "s");
    sim.pull_request(REPO, number, "opened").await;
    let body = received.lock().unwrap()[0].body.clone();
    let runtime = Runtime::new(vec![Box::new(Accepts)], Providers::new());
    let ingest = runtime
        .ingest(Envelope {
            source: "sim".into(),
            delivery_id: "d".into(),
            depth: 0,
            origin: almanac::runtime::Origin::Simulated,
            kind: "github.pull_request.opened".into(),
            payload: serde_json::from_slice(&body).unwrap(),
        })
        .await
        .unwrap();
    assert_eq!(ingest, Ingest::Queued);
}
