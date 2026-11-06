//! The agent's only hands: an MCP endpoint on localhost whose tools are sandbox calls. It lives
//! in this process, so it reaches any `Sandbox`, local or remote, and the sandbox needs no
//! network. It is part of the trust boundary and exposes nothing else.
use almanac::cx::StepError;
use axum::extract::State;
use axum::http::{HeaderMap, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::post;
use axum::{Json, Router};
use sandbox::{Sandbox, SandboxId, WORKDIR};
use serde_json::{Value, json};
use std::sync::Arc;
use std::time::Duration;
use tokio::task::JoinHandle;

const SERVER: &str = "sandbox";

/// A running endpoint. Dropping it stops the server.
pub struct Tools {
    pub config: String,
    server: JoinHandle<()>,
}

impl Drop for Tools {
    fn drop(&mut self) {
        self.server.abort();
    }
}

pub fn allowed() -> [String; 3] {
    ["exec", "read_file", "write_file"].map(|t| format!("mcp__{SERVER}__{t}"))
}

#[derive(Clone)]
struct Session {
    sandbox: Arc<dyn Sandbox>,
    id: SandboxId,
    token: String,
    timeout: Duration,
}

pub async fn serve(
    sandbox: Arc<dyn Sandbox>,
    id: SandboxId,
    timeout: Duration,
) -> Result<Tools, StepError> {
    let token = uuid::Uuid::new_v4().to_string();
    let session = Session {
        sandbox,
        id,
        token: token.clone(),
        timeout,
    };
    let app = Router::new().route("/", post(rpc)).with_state(session);
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .map_err(|e| StepError(e.to_string()))?;
    let address = listener
        .local_addr()
        .map_err(|e| StepError(e.to_string()))?;
    let server = tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });
    let config = json!({"mcpServers": {SERVER: {
        "type": "http",
        "url": format!("http://{address}/"),
        "headers": {"Authorization": format!("Bearer {token}")},
    }}});
    Ok(Tools {
        config: config.to_string(),
        server,
    })
}

fn tool(name: &str, description: &str, properties: Value, required: &[&str]) -> Value {
    json!({
        "name": name,
        "description": description,
        "inputSchema": {"type": "object", "properties": properties, "required": required},
    })
}

fn tools() -> Value {
    let text = json!({"type": "string"});
    json!([
        tool(
            "exec",
            "Run a shell command in the checkout. There is no network.",
            json!({"command": text}),
            &["command"]
        ),
        tool(
            "read_file",
            "Read a file in the checkout.",
            json!({"path": text}),
            &["path"]
        ),
        tool(
            "write_file",
            "Replace a file in the checkout.",
            json!({"path": text, "content": text}),
            &["path", "content"]
        ),
    ])
}

/// Quotes a value for the sandbox's shell. The value came from the model.
fn quoted(text: &str) -> String {
    format!("'{}'", text.replace('\'', r"'\''"))
}

fn inside(path: &str) -> String {
    match path.strip_prefix('/') {
        Some(_) => path.to_string(),
        None => format!("{WORKDIR}/{path}"),
    }
}

async fn call(session: &Session, name: &str, arguments: &Value) -> Result<String, String> {
    let text = |key: &str| arguments[key].as_str().unwrap_or_default();
    let command = match name {
        "exec" => text("command").to_string(),
        "read_file" => format!("cat -- {}", quoted(&inside(text("path")))),
        "write_file" => {
            let path = quoted(&inside(text("path")));
            let content = quoted(text("content"));
            format!("mkdir -p \"$(dirname -- {path})\" && printf %s {content} > {path}")
        }
        other => return Err(format!("unknown tool `{other}`")),
    };
    let out = session
        .sandbox
        .exec(&session.id, &command, session.timeout)
        .await
        .map_err(|e| e.to_string())?;
    let shown = format!("exit {}\n{}{}", out.exit_code, out.stdout, out.stderr);
    match out.exit_code {
        0 => Ok(shown),
        _ => Err(shown),
    }
}

async fn rpc(
    State(session): State<Session>,
    headers: HeaderMap,
    Json(request): Json<Value>,
) -> Response {
    let expected = format!("Bearer {}", session.token);
    let authorized = headers
        .get(header::AUTHORIZATION)
        .is_some_and(|h| h.as_bytes() == expected.as_bytes());
    if !authorized {
        return StatusCode::UNAUTHORIZED.into_response();
    }
    let Some(id) = request.get("id").cloned() else {
        return StatusCode::ACCEPTED.into_response();
    };
    let result = match request["method"].as_str() {
        Some("initialize") => json!({
            "protocolVersion": request["params"]["protocolVersion"],
            "capabilities": {"tools": {}},
            "serverInfo": {"name": SERVER, "version": "0"},
        }),
        Some("tools/list") => json!({"tools": tools()}),
        Some("tools/call") => {
            let name = request["params"]["name"].as_str().unwrap_or_default();
            let answer = call(&session, name, &request["params"]["arguments"]).await;
            let is_error = answer.is_err();
            let text = answer.unwrap_or_else(|e| e);
            json!({"content": [{"type": "text", "text": text}], "isError": is_error})
        }
        _ => json!({}),
    };
    Json(json!({"jsonrpc": "2.0", "id": id, "result": result})).into_response()
}
