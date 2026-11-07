//! Persistent native fixtures only; broker/daemon execute the actual workflows.
use axum::{
    Json, Router,
    extract::State,
    http::StatusCode,
    routing::{get, post},
};
use claude::stub::{StubModel, Turn, tool};
use flate2::{Compression, write::GzEncoder};
use serde_json::{Value, json};
use simforge::{Pull, RENOVATE, Repo, SimForge, Suite};

const REPO: &str = "fleet/demo";
const BRANCH: &str = "renovate/demo-package";
const SECRET: &str = "synthetic-demo-webhook-secret";
const HOOK: &str = "http://127.0.0.1:8787/webhooks/github";

#[derive(Clone)]
struct Fixtures {
    forge: SimForge,
    model: StubModel,
    number: u64,
    initial_head: String,
}

fn agent(request: &Value) -> Turn {
    let messages = request["messages"].as_array().cloned().unwrap_or_default();
    let tool_ran = messages
        .iter()
        .flat_map(|m| m["content"].as_array().cloned().unwrap_or_default())
        .any(|block| block["type"] == "tool_result");
    if tool_ran {
        Turn::Text(r#"{"summary":"repaired"}"#.to_string())
    } else {
        tool(
            "mcp__sandbox__write_file",
            json!({"path":"src/main.py", "content":"print('fixed')\n"}),
        )
    }
}

fn source_tarball() -> Vec<u8> {
    let mut builder = tar::Builder::new(GzEncoder::new(vec![], Compression::fast()));
    let text = b"print('broken')\n";
    let mut header = tar::Header::new_gnu();
    header.set_size(text.len() as u64);
    header.set_mode(0o644);
    header.set_cksum();
    builder
        .append_data(&mut header, "repo-demo/src/main.py", &text[..])
        .unwrap();
    builder.into_inner().unwrap().finish().unwrap()
}

async fn state(State(f): State<Fixtures>) -> Json<Value> {
    let pull = f.forge.pull(REPO, f.number);
    let writes: Vec<_> = f
        .forge
        .writes()
        .iter()
        .map(|w| json!({"method":w.method,"path":w.path,"body":w.body}))
        .collect();
    Json(json!({
        "repo":REPO, "number":f.number, "branch":BRANCH,
        "initial_head":f.initial_head, "head":pull.head_sha,
        "model_requests":f.model.requests().len(), "writes":writes,
        "forge_url":f.forge.base_url(), "model_url":f.model.url,
        "registry_url":"http://127.0.0.1:8811", "webhook_target":HOOK,
        "note":"Fixture observations only. No workflow state is inferred."
    }))
}

async fn ci(f: &Fixtures, conclusion: &str) -> Value {
    let head = f.forge.pull(REPO, f.number).head_sha;
    let suite = Suite::finished(&head, BRANCH, conclusion, "error: synthetic tests failed");
    let id = f.forge.add_suite(REPO, suite);
    let delivery = f.forge.suite_completed(REPO, id).await;
    json!({"head":head,"conclusion":conclusion,"delivery":delivery})
}

async fn fail(State(f): State<Fixtures>) -> Json<Value> {
    Json(ci(&f, "failure").await)
}
async fn succeed(State(f): State<Fixtures>) -> Result<Json<Value>, (StatusCode, &'static str)> {
    if f.forge.pull(REPO, f.number).head_sha == f.initial_head {
        return Err((
            StatusCode::CONFLICT,
            "No repaired head yet; let the real daemon push first.",
        ));
    }
    Ok(Json(ci(&f, "success").await))
}
async fn hold(State(f): State<Fixtures>) -> &'static str {
    f.model.hold();
    "held\n"
}
async fn release(State(f): State<Fixtures>) -> &'static str {
    f.model.release();
    "released\n"
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Bind fixed listeners before publishing fixture connection details.
    let control = tokio::net::TcpListener::bind("127.0.0.1:8810").await?;
    let registry = tokio::net::TcpListener::bind("127.0.0.1:8811").await?;
    let forge = SimForge::start().await;
    forge.add_repo(Repo::public(REPO));
    forge.add_repo(Repo::public("fleet/ops"));
    forge.set_tarball(REPO, source_tarball());
    let mut pull = Pull::renovate(BRANCH, "Update synthetic demo-package");
    pull.body = Some("| Package | Change |\n|---|---|\n| [demo-package](https://x) | [`1.0.0` → `2.0.0`](https://renovatebot.com/diffs/npm/demo-package/1.0.0/2.0.0) |\n".to_string());
    let number = forge.add_pull(REPO, pull);
    let initial_head = forge.pull(REPO, number).head_sha;
    let healthy = forge.add_pull(
        REPO,
        Pull::renovate("renovate/healthy", "Healthy synthetic update"),
    );
    let sha = forge.pull(REPO, healthy).head_sha;
    forge.add_suite(
        REPO,
        Suite::finished(&sha, "renovate/healthy", "success", ""),
    );
    forge.deliver_to(HOOK, SECRET);
    let model = StubModel::answering(agent).await;
    let env = format!(
        "export GITHUB_API_URL={}\nexport GITHUB_TOKEN=t\nexport ALMANAC_OWNER=fleet\nexport ALMANAC_RENOVATE_AUTHOR='{}'\nexport ALMANAC_REGISTRY_URL=http://127.0.0.1:8811\nexport ANTHROPIC_BASE_URL={}\nexport ANTHROPIC_API_KEY=stub\nexport ALMANAC_WEBHOOK_SECRET={}\nexport ALMANAC_BROKER_LISTEN=127.0.0.1:8787\nexport ALMANAC_LISTEN=127.0.0.1:8788\nexport ALMANAC_ALERTS_REPO=fleet/ops\nexport ALMANAC_CONFIG=/nonexistent/almanac.toml\nexport ALMANAC_SANDBOX_IMAGE=localhost/almanac-fixer:test\nexport ALMANAC_MODE=live\n",
        forge.base_url(),
        RENOVATE,
        model.url,
        SECRET
    );
    std::fs::write("fixtures.env", env)?;
    let f = Fixtures {
        forge,
        model,
        number,
        initial_head,
    };
    let app = Router::new()
        .route("/state", get(state))
        .route("/ci/fail", post(fail))
        .route("/ci/success", post(succeed))
        .route("/model/hold", post(hold))
        .route("/model/release", post(release))
        .with_state(f);
    // Fixed old publish date is synthetic evidence, not an external registry call.
    let registry_app = Router::new().route(
        "/{name}",
        get(|| async { Json(json!({"time":{"2.0.0":"2020-01-01T00:00:00Z"}})) }),
    );
    tokio::spawn(async move { axum::serve(registry, registry_app).await });
    println!("Native fixtures ready: http://127.0.0.1:8810/state; connection data in fixtures.env");
    axum::serve(control, app)
        .with_graceful_shutdown(async {
            let _ = tokio::signal::ctrl_c().await;
        })
        .await?;
    Ok(())
}
