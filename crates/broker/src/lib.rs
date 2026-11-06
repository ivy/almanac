//! The one place webhooks arrive. It verifies a delivery, appends it to a shared log, and
//! acknowledges. It knows nothing about workflows, so adding one never touches it or GitHub.
use axum::Router;
use axum::body::Bytes;
use axum::extract::{DefaultBodyLimit, State};
use axum::http::{HeaderMap, StatusCode};
use axum::routing::{get, post};
use deliveries::Delivery;
use futures::future::BoxFuture;
use hmac::{Hmac, Mac};
use sha2::Sha256;
use sqlx::postgres::PgPool;
use std::sync::Arc;

/// GitHub caps payloads at 25 MB.
pub const MAX_BODY: usize = 25 * 1024 * 1024;
const MAX_ID: usize = 128;

/// Why a request was refused. It holds nothing from the request's body.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Refusal {
    Unsigned,
    BadSignature,
    Malformed,
}

impl Refusal {
    fn status(self) -> StatusCode {
        match self {
            Self::Unsigned | Self::BadSignature => StatusCode::UNAUTHORIZED,
            Self::Malformed => StatusCode::BAD_REQUEST,
        }
    }
}

/// A provider of webhooks: where they arrive and how to trust them. Another provider is another
/// implementation of this and a route, with no change to the log or to any almanac.
pub trait Source: Send + Sync + 'static {
    fn name(&self) -> &'static str;
    fn accept(&self, headers: &HeaderMap, body: &[u8]) -> Result<Delivery, Refusal>;
}

pub struct GithubSource {
    secret: String,
}

impl GithubSource {
    pub fn new(secret: impl Into<String>) -> Self {
        Self {
            secret: secret.into(),
        }
    }

    /// Constant time, because `verify_slice` compares without stopping early.
    fn verified(&self, signature: &str, body: &[u8]) -> bool {
        let Some(hex_digest) = signature.strip_prefix("sha256=") else {
            return false;
        };
        let Ok(digest) = hex::decode(hex_digest) else {
            return false;
        };
        let Ok(mut mac) = Hmac::<Sha256>::new_from_slice(self.secret.as_bytes()) else {
            return false;
        };
        mac.update(body);
        mac.verify_slice(&digest).is_ok()
    }
}

fn header<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    headers.get(name)?.to_str().ok()
}

/// An identifier from a header, restricted because it is stored and logged.
fn identifier(text: &str) -> Option<&str> {
    let valid = |c: char| c.is_ascii_alphanumeric() || c == '-' || c == '_';
    (!text.is_empty() && text.len() <= MAX_ID && text.chars().all(valid)).then_some(text)
}

impl Source for GithubSource {
    fn name(&self) -> &'static str {
        "github"
    }

    fn accept(&self, headers: &HeaderMap, body: &[u8]) -> Result<Delivery, Refusal> {
        let signature = header(headers, "x-hub-signature-256").ok_or(Refusal::Unsigned)?;
        if !self.verified(signature, body) {
            return Err(Refusal::BadSignature);
        }
        let event = header(headers, "x-github-event")
            .and_then(identifier)
            .ok_or(Refusal::Malformed)?;
        let id = header(headers, "x-github-delivery")
            .and_then(identifier)
            .ok_or(Refusal::Malformed)?;
        // All the broker reads of a payload: routing needs the action, and nothing else does.
        let payload: serde_json::Value =
            serde_json::from_slice(body).map_err(|_| Refusal::Malformed)?;
        let kind = match payload["action"].as_str().and_then(identifier) {
            Some(action) => format!("github.{event}.{action}"),
            None => format!("github.{event}"),
        };
        Ok(Delivery {
            source: "github".to_string(),
            delivery_id: id.to_string(),
            kind,
            body: body.to_vec(),
        })
    }
}

/// Where accepted deliveries go.
pub trait Log: Send + Sync + 'static {
    fn append<'a>(&'a self, delivery: &'a Delivery) -> BoxFuture<'a, Result<bool, String>>;
    fn ready(&self) -> BoxFuture<'_, bool>;
}

pub struct PostgresLog(pub PgPool);

impl Log for PostgresLog {
    fn append<'a>(&'a self, delivery: &'a Delivery) -> BoxFuture<'a, Result<bool, String>> {
        Box::pin(async move {
            deliveries::append(&self.0, delivery)
                .await
                .map_err(|e| e.to_string())
        })
    }

    fn ready(&self) -> BoxFuture<'_, bool> {
        Box::pin(async move { sqlx::query("SELECT 1").execute(&self.0).await.is_ok() })
    }
}

struct Broker<S, L> {
    source: S,
    log: L,
}

async fn receive<S: Source, L: Log>(
    State(broker): State<Arc<Broker<S, L>>>,
    headers: HeaderMap,
    body: Bytes,
) -> StatusCode {
    let source = broker.source.name();
    let delivery = match broker.source.accept(&headers, &body) {
        Ok(delivery) => delivery,
        Err(refusal) => {
            tracing::warn!(source, ?refusal, bytes = body.len(), "refused a delivery");
            return refusal.status();
        }
    };
    match broker.log.append(&delivery).await {
        Ok(appended) => {
            tracing::info!(source, id = %delivery.delivery_id, kind = %delivery.kind, appended);
            StatusCode::ACCEPTED
        }
        // GitHub records the failure, and a catch-up scan or redelivery brings the event back.
        Err(error) => {
            tracing::error!(source, %error, "could not append a delivery");
            StatusCode::SERVICE_UNAVAILABLE
        }
    }
}

async fn ready<S: Source, L: Log>(State(broker): State<Arc<Broker<S, L>>>) -> StatusCode {
    match broker.log.ready().await {
        true => StatusCode::OK,
        false => StatusCode::SERVICE_UNAVAILABLE,
    }
}

pub fn router<S: Source, L: Log>(source: S, log: L) -> Router {
    let route = format!("/webhooks/{}", source.name());
    let broker = Arc::new(Broker { source, log });
    Router::new()
        .route(&route, post(receive::<S, L>))
        .route("/healthz", get(|| async { StatusCode::OK }))
        .route("/readyz", get(ready::<S, L>))
        .layer(DefaultBodyLimit::max(MAX_BODY))
        .with_state(broker)
}
