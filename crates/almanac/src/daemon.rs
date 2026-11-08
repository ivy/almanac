//! An almanac as a long-lived service: consume, process, stay up, and stop cleanly.
use crate::consumer::Consumer;
use crate::diag::Error;
use crate::runtime::Runtime;
use axum::Router;
use axum::http::StatusCode;
use axum::routing::get;
use std::future::Future;
use std::net::SocketAddr;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;
use tokio::sync::watch;

const FIRST_BACKOFF: Duration = Duration::from_secs(1);
const LONGEST_BACKOFF: Duration = Duration::from_secs(60);
/// How often to ask whether a scheduled scan is due.
const TICK: Duration = Duration::from_secs(30);
/// How long an in-flight run gets to finish after a stop is asked for. A run abandoned here is
/// requeued on restart, so it is repeated and not lost.
pub const GRACE: Duration = Duration::from_secs(30);

/// Whether the daemon is reaching its store.
#[derive(Clone)]
pub struct Health(Arc<AtomicBool>);

impl Default for Health {
    fn default() -> Self {
        Self(Arc::new(AtomicBool::new(true)))
    }
}

impl Health {
    pub fn set(&self, healthy: bool) {
        self.0.store(healthy, Ordering::Relaxed);
    }

    pub fn is_healthy(&self) -> bool {
        self.0.load(Ordering::Relaxed)
    }
}

/// Answers on localhost for a process supervisor or a person.
pub async fn serve_health(address: SocketAddr, health: Health) -> Result<(), std::io::Error> {
    let app = Router::new().route(
        "/healthz",
        get(move || {
            let health = health.clone();
            async move {
                match health.is_healthy() {
                    true => StatusCode::OK,
                    false => StatusCode::SERVICE_UNAVAILABLE,
                }
            }
        }),
    );
    let listener = tokio::net::TcpListener::bind(address).await?;
    axum::serve(listener, app).await
}

/// Retries `attempt` until it works or a stop is asked for, waiting longer each time, so an
/// outage neither crashes the process nor hammers what is down.
pub async fn with_backoff<T, E, F, Fut>(
    what: &str,
    mut stopped: watch::Receiver<bool>,
    mut attempt: F,
) -> Option<T>
where
    E: std::fmt::Display,
    F: FnMut() -> Fut,
    Fut: Future<Output = Result<T, E>>,
{
    let mut wait = FIRST_BACKOFF;
    loop {
        match attempt().await {
            Ok(value) => return Some(value),
            Err(error) => tracing::warn!(%error, ?wait, "{what} failed, retrying"),
        }
        tokio::select! {
            () = tokio::time::sleep(wait) => {}
            _ = stopped.wait_for(|stop| *stop) => return None,
        }
        wait = (wait * 2).min(LONGEST_BACKOFF);
    }
}

/// Follows the log until `stop` finishes. No run starts after that, and an in-flight run gets
/// `grace` to finish.
pub async fn run(
    runtime: &Runtime,
    consumer: &Consumer,
    health: &Health,
    grace: Duration,
    stop: impl Future<Output = ()>,
) -> Result<(), Error> {
    let (stopping, stopped) = watch::channel(false);
    let work = async {
        let mut wait = FIRST_BACKOFF;
        loop {
            let mut wake = stopped.clone();
            let until_stopped = async move {
                let _ = wake.wait_for(|stop| *stop).await;
            };
            match consumer.follow(runtime, until_stopped).await {
                Ok(()) => return,
                Err(error) => {
                    health.set(false);
                    tracing::warn!(%error, ?wait, "lost the store, retrying");
                }
            }
            let mut wake = stopped.clone();
            tokio::select! {
                () = tokio::time::sleep(wait) => {}
                _ = wake.wait_for(|stop| *stop) => return,
            }
            wait = (wait * 2).min(LONGEST_BACKOFF);
            if runtime.ping().await.is_ok() {
                health.set(true);
                wait = FIRST_BACKOFF;
            }
        }
    };
    // Scanning runs beside the event loop, so a slow scan never delays a live webhook.
    let scanning = async {
        let mut wake = stopped.clone();
        loop {
            let started = tokio::time::Instant::now();
            match runtime.tick().await {
                Ok(Some(scan)) => tracing::info!(
                    found = scan.found,
                    queued = scan.queued,
                    unchanged = scan.unchanged,
                    rejected = scan.rejected,
                    skipped = scan.skipped,
                    seconds = started.elapsed().as_secs_f32(),
                    "scheduled scan finished"
                ),
                Ok(None) => {}
                Err(error) => tracing::warn!(%error, "a scheduled scan failed"),
            }
            tokio::select! {
                () = tokio::time::sleep(TICK) => {}
                _ = wake.wait_for(|stop| *stop) => return,
            }
        }
    };
    let work = async {
        tokio::join!(work, scanning);
    };
    tokio::pin!(work);
    tokio::pin!(stop);
    tokio::select! {
        () = &mut work => return Ok(()),
        () = &mut stop => {}
    }
    tracing::info!("stopping: no new runs will start");
    runtime.stop_claiming();
    let _ = stopping.send(true);
    tokio::select! {
        () = &mut work => tracing::info!("stopped cleanly"),
        () = tokio::time::sleep(grace) => {
            tracing::warn!("abandoning in-flight runs: they will be requeued on restart");
        }
    }
    Ok(())
}
