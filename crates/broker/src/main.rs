//! `almanac-broker`: run it behind a public HTTPS ingress. It only listens on loopback.
use broker::{GithubSource, PostgresLog, router};
use sqlx::postgres::PgPoolOptions;
use std::net::SocketAddr;
use std::process::ExitCode;
use tokio::signal::unix::{SignalKind, signal};

const DEFAULT_LISTEN: &str = "127.0.0.1:8787";

fn required(name: &str) -> Result<String, String> {
    std::env::var(name).map_err(|_| format!("{name} is not set"))
}

async fn terminated() {
    let Ok(mut term) = signal(SignalKind::terminate()) else {
        return;
    };
    tokio::select! {
        _ = term.recv() => {}
        _ = tokio::signal::ctrl_c() => {}
    }
}

async fn run() -> Result<(), String> {
    let secret = required("ALMANAC_WEBHOOK_SECRET")?;
    let url = required("DATABASE_URL")?;
    let listen = std::env::var("ALMANAC_BROKER_LISTEN");
    let listen = listen.unwrap_or_else(|_| DEFAULT_LISTEN.to_string());
    let address: SocketAddr = listen
        .parse()
        .map_err(|_| format!("{listen} is not an address"))?;
    if !address.ip().is_loopback() {
        return Err(format!("{listen} is not a loopback address"));
    }
    let pool = PgPoolOptions::new()
        .connect(&url)
        .await
        .map_err(|e| format!("cannot reach the database: {e}"))?;
    deliveries::migrate(&pool)
        .await
        .map_err(|e| format!("cannot migrate the database: {e}"))?;
    let app = router(GithubSource::new(secret), PostgresLog(pool));
    let listener = tokio::net::TcpListener::bind(address)
        .await
        .map_err(|e| format!("cannot listen on {listen}: {e}"))?;
    tracing::info!(%address, "listening");
    axum::serve(listener, app)
        .with_graceful_shutdown(terminated())
        .await
        .map_err(|e| e.to_string())
}

#[tokio::main]
async fn main() -> ExitCode {
    tracing_subscriber::fmt().json().init();
    match run().await {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("almanac-broker: {e}");
            ExitCode::FAILURE
        }
    }
}
