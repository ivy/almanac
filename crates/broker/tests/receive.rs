use axum::http::HeaderMap;
use broker::{GithubSource, Log, MAX_BODY, Refusal, Source, router};
use deliveries::Delivery;
use futures::future::BoxFuture;
use hmac::{Hmac, Mac};
use sha2::Sha256;
use std::io::Write;
use std::sync::{Arc, Mutex};

const SECRET: &str = "it's a secret";

fn sign(body: &[u8]) -> String {
    let mut mac = Hmac::<Sha256>::new_from_slice(SECRET.as_bytes()).unwrap();
    mac.update(body);
    format!("sha256={}", hex::encode(mac.finalize().into_bytes()))
}

#[derive(Clone, Default)]
struct MemoryLog(Arc<Mutex<Vec<Delivery>>>);

impl Log for MemoryLog {
    fn append<'a>(&'a self, delivery: &'a Delivery) -> BoxFuture<'a, Result<bool, String>> {
        Box::pin(async move {
            let mut log = self.0.lock().unwrap();
            if log.iter().any(|d| d.delivery_id == delivery.delivery_id) {
                return Ok(false);
            }
            log.push(delivery.clone());
            Ok(true)
        })
    }
    fn ready(&self) -> BoxFuture<'_, bool> {
        Box::pin(async { true })
    }
}

async fn serving() -> (String, MemoryLog) {
    let log = MemoryLog::default();
    let app = router(GithubSource::new(SECRET), log.clone());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, app).await });
    (url, log)
}

async fn post(url: &str, body: &[u8], signature: Option<String>, id: &str) -> reqwest::StatusCode {
    let mut request = reqwest::Client::new()
        .post(format!("{url}/webhooks/github"))
        .header("x-github-event", "pull_request")
        .header("x-github-delivery", id)
        .body(body.to_vec());
    if let Some(signature) = signature {
        request = request.header("x-hub-signature-256", signature);
    }
    request.send().await.unwrap().status()
}

const BODY: &[u8] = br#"{"action": "opened", "number": 1}"#;

#[tokio::test]
async fn a_signed_delivery_is_logged_with_its_routing_kind_and_exact_body() {
    let (url, log) = serving().await;
    assert_eq!(post(&url, BODY, Some(sign(BODY)), "d1").await, 202);
    let logged = log.0.lock().unwrap();
    assert_eq!(logged[0].kind, "github.pull_request.opened");
    assert_eq!(logged[0].source, "github");
    assert_eq!(logged[0].body, BODY);
}

#[tokio::test]
async fn a_forged_webhook_gets_401_and_nothing_is_persisted() {
    let (url, log) = serving().await;
    let forged = format!("sha256={}", "0".repeat(64));
    assert_eq!(post(&url, BODY, Some(forged), "d1").await, 401);
    assert_eq!(post(&url, BODY, None, "d2").await, 401);
    assert_eq!(
        post(&url, BODY, Some("garbage".to_string()), "d3").await,
        401
    );
    assert!(log.0.lock().unwrap().is_empty());
}

#[tokio::test]
async fn a_tampered_body_fails_verification() {
    let (url, log) = serving().await;
    let tampered = br#"{"action": "closed", "number": 1}"#;
    assert_eq!(post(&url, tampered, Some(sign(BODY)), "d1").await, 401);
    assert!(log.0.lock().unwrap().is_empty());
}

#[tokio::test]
async fn an_oversized_payload_is_refused() {
    let (url, log) = serving().await;
    let body = vec![b' '; MAX_BODY + 1];
    assert_eq!(post(&url, &body, Some(sign(&body)), "d1").await, 413);
    assert!(log.0.lock().unwrap().is_empty());
}

#[tokio::test]
async fn a_redelivered_duplicate_is_acknowledged_and_not_appended() {
    let (url, log) = serving().await;
    assert_eq!(post(&url, BODY, Some(sign(BODY)), "d1").await, 202);
    assert_eq!(post(&url, BODY, Some(sign(BODY)), "d1").await, 202);
    assert_eq!(log.0.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn a_delivery_id_that_could_poison_a_log_is_refused() {
    let (url, log) = serving().await;
    assert_eq!(post(&url, BODY, Some(sign(BODY)), "bad id").await, 400);
    assert!(log.0.lock().unwrap().is_empty());
}

#[tokio::test]
async fn health_needs_nothing_and_readiness_asks_the_log() {
    let (url, _) = serving().await;
    let status = |path: &'static str| {
        let url = format!("{url}{path}");
        async move { reqwest::get(url).await.unwrap().status() }
    };
    assert_eq!(status("/healthz").await, 200);
    assert_eq!(status("/readyz").await, 200);
}

#[test]
fn an_event_with_no_action_routes_by_event_alone() {
    let body = br#"{"state": "failure"}"#;
    let mut headers = HeaderMap::new();
    headers.insert("x-github-event", "status".parse().unwrap());
    headers.insert("x-github-delivery", "d1".parse().unwrap());
    headers.insert("x-hub-signature-256", sign(body).parse().unwrap());
    let delivery = GithubSource::new(SECRET).accept(&headers, body).unwrap();
    assert_eq!(delivery.kind, "github.status");
}

#[test]
fn a_hostile_action_cannot_shape_the_kind() {
    let body = br#"{"action": "opened.evil kind"}"#;
    let mut headers = HeaderMap::new();
    headers.insert("x-github-event", "pull_request".parse().unwrap());
    headers.insert("x-github-delivery", "d1".parse().unwrap());
    headers.insert("x-hub-signature-256", sign(body).parse().unwrap());
    let delivery = GithubSource::new(SECRET).accept(&headers, body).unwrap();
    assert_eq!(delivery.kind, "github.pull_request");
}

#[test]
fn a_signed_body_that_is_not_json_is_refused() {
    let body = b"not json";
    let mut headers = HeaderMap::new();
    headers.insert("x-github-event", "status".parse().unwrap());
    headers.insert("x-github-delivery", "d1".parse().unwrap());
    headers.insert("x-hub-signature-256", sign(body).parse().unwrap());
    let refused = GithubSource::new(SECRET).accept(&headers, body);
    assert_eq!(refused, Err(Refusal::Malformed));
}

#[derive(Clone, Default)]
struct Captured(Arc<Mutex<Vec<u8>>>);

impl Write for Captured {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        self.0.lock().unwrap().extend_from_slice(bytes);
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[test]
fn a_signature_failure_is_logged_without_the_payload() {
    let captured = Captured::default();
    let writer = captured.clone();
    let subscriber = tracing_subscriber::fmt()
        .with_writer(move || writer.clone())
        .finish();
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap();
    tracing::subscriber::with_default(subscriber, || {
        runtime.block_on(async {
            let app = router(GithubSource::new(SECRET), MemoryLog::default());
            let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
            let url = format!("http://{}", listener.local_addr().unwrap());
            let serving = axum::serve(listener, app);
            let hostile = b"HOSTILE PAYLOAD TEXT";
            let forged = Some(format!("sha256={}", "0".repeat(64)));
            tokio::select! {
                _ = serving => {}
                status = post(&url, hostile, forged, "d1") => assert_eq!(status, 401),
            }
        });
    });
    let logged = String::from_utf8(captured.0.lock().unwrap().clone()).unwrap();
    assert!(logged.contains("refused a delivery"), "{logged}");
    assert!(!logged.contains("HOSTILE"), "{logged}");
}

#[test]
fn the_broker_depends_on_no_workflow_or_agent_code() {
    let manifest = concat!(env!("CARGO_MANIFEST_DIR"), "/../../Cargo.toml");
    let out = std::process::Command::new(env!("CARGO"))
        .args([
            "metadata",
            "--format-version=1",
            "--manifest-path",
            manifest,
        ])
        .output()
        .unwrap();
    let metadata: serde_json::Value = serde_json::from_slice(&out.stdout).unwrap();
    let nodes = metadata["resolve"]["nodes"].as_array().unwrap();
    let broker = nodes
        .iter()
        .find(|n| n["id"].as_str().unwrap().contains("crates/broker"))
        .unwrap();
    let runtime_deps: Vec<&str> = broker["deps"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|d| {
            let kinds = d["dep_kinds"].as_array().unwrap();
            kinds.iter().any(|k| k["kind"].is_null())
        })
        .map(|d| d["name"].as_str().unwrap())
        .collect();
    for forbidden in [
        "almanac",
        "almanac_macros",
        "claude",
        "sandbox",
        "github",
        "registry",
    ] {
        assert!(!runtime_deps.contains(&forbidden), "{runtime_deps:?}");
    }
    assert!(runtime_deps.contains(&"deliveries"), "{runtime_deps:?}");
}
