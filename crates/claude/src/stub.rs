//! A stand-in for the Messages API. It replays scripted turns and records every request, so a
//! test sees exactly which tools and text reach the model, with no credential and no spend.
use axum::extract::State;
use axum::http::{HeaderMap, header};
use axum::response::{IntoResponse, Response};
use axum::routing::post;
use axum::{Json, Router};
use serde_json::{Value, json};
use std::collections::VecDeque;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

/// One model turn.
#[derive(Clone, Debug)]
pub enum Turn {
    Tool { name: String, input: Value },
    Text(String),
}

pub fn tool(name: &str, input: Value) -> Turn {
    Turn::Tool {
        name: name.to_string(),
        input,
    }
}

type Responder = Box<dyn Fn(&Value) -> Turn + Send + Sync>;

#[derive(Default)]
struct Script {
    turns: VecDeque<Turn>,
    requests: Vec<Value>,
    /// How each request authenticated, as `header: value`.
    credentials: Vec<String>,
    tokens_per_turn: u64,
    /// Answers from the request, for sessions that interleave.
    responder: Option<Responder>,
}

#[derive(Clone)]
pub struct StubModel {
    script: Arc<Mutex<Script>>,
    held: Arc<AtomicBool>,
    pub url: String,
}

impl StubModel {
    pub async fn start(turns: Vec<Turn>) -> Self {
        Self::costing(turns, 1).await
    }

    /// As `start`, with each turn reporting this many output tokens.
    pub async fn costing(turns: Vec<Turn>, tokens_per_turn: u64) -> Self {
        Self::serve(Script {
            turns: turns.into(),
            tokens_per_turn,
            ..Script::default()
        })
        .await
    }

    /// Answers each request by calling `responder`, however sessions interleave.
    pub async fn answering(responder: impl Fn(&Value) -> Turn + Send + Sync + 'static) -> Self {
        Self::serve(Script {
            tokens_per_turn: 1,
            responder: Some(Box::new(responder)),
            ..Script::default()
        })
        .await
    }

    /// Leaves every answer waiting until `release`, so a test can act during a run.
    pub fn hold(&self) {
        self.held.store(true, Ordering::SeqCst);
    }

    pub fn release(&self) {
        self.held.store(false, Ordering::SeqCst);
    }

    async fn serve(script: Script) -> Self {
        let script = Arc::new(Mutex::new(script));
        let held = Arc::new(AtomicBool::new(false));
        let app = Router::new()
            .route("/v1/messages", post(messages))
            .route("/v1/messages/count_tokens", post(count))
            .with_state((script.clone(), held.clone()));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
            .await
            .expect("a free port");
        let url = format!("http://{}", listener.local_addr().expect("a bound port"));
        tokio::spawn(async move { axum::serve(listener, app).await });
        Self { script, held, url }
    }

    pub fn requests(&self) -> Vec<Value> {
        self.script.lock().unwrap().requests.clone()
    }

    /// How requests authenticated, as `header: value`, with no repeats.
    pub fn credentials(&self) -> Vec<String> {
        let mut seen = self.script.lock().unwrap().credentials.clone();
        seen.sort();
        seen.dedup();
        seen
    }

    /// Names of the tools offered across every request.
    pub fn tools_offered(&self) -> Vec<String> {
        let mut names: Vec<String> = self
            .requests()
            .iter()
            .flat_map(|r| r["tools"].as_array().cloned().unwrap_or_default())
            .filter_map(|t| t["name"].as_str().map(str::to_string))
            .collect();
        names.sort();
        names.dedup();
        names
    }
}

async fn count() -> Json<Value> {
    Json(json!({"input_tokens": 1}))
}

fn sse(name: &str, data: &Value) -> String {
    format!("event: {name}\ndata: {data}\n\n")
}

async fn messages(
    State((script, held)): State<(Arc<Mutex<Script>>, Arc<AtomicBool>)>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Response {
    let (turn, tokens, number) = {
        let mut s = script.lock().unwrap();
        s.requests.push(body.clone());
        for name in ["authorization", "x-api-key"] {
            if let Some(value) = headers.get(name).and_then(|v| v.to_str().ok()) {
                s.credentials.push(format!("{name}: {value}"));
            }
        }
        let turn = match &s.responder {
            Some(responder) => responder(&body),
            None => s.turns.pop_front().unwrap_or(Turn::Text("{}".to_string())),
        };
        (turn, s.tokens_per_turn, s.requests.len())
    };
    while held.load(Ordering::SeqCst) {
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    let model = body["model"].as_str().unwrap_or("stub");
    let usage = json!({"input_tokens": 1, "output_tokens": tokens});
    let (block, delta, stop) = match turn {
        Turn::Tool { name, input } => (
            json!({"type": "tool_use", "id": format!("toolu_{number}"), "name": name, "input": {}}),
            json!({"type": "input_json_delta", "partial_json": input.to_string()}),
            "tool_use",
        ),
        Turn::Text(text) => (
            json!({"type": "text", "text": ""}),
            json!({"type": "text_delta", "text": text}),
            "end_turn",
        ),
    };
    let message = json!({
        "id": "msg_stub", "type": "message", "role": "assistant", "model": model, "content": [],
        "stop_reason": null, "stop_sequence": null, "usage": usage,
    });
    let events = [
        sse(
            "message_start",
            &json!({"type": "message_start", "message": message}),
        ),
        sse(
            "content_block_start",
            &json!({"type": "content_block_start", "index": 0, "content_block": block}),
        ),
        sse(
            "content_block_delta",
            &json!({"type": "content_block_delta", "index": 0, "delta": delta}),
        ),
        sse(
            "content_block_stop",
            &json!({"type": "content_block_stop", "index": 0}),
        ),
        sse(
            "message_delta",
            &json!({"type": "message_delta", "delta": {"stop_reason": stop, "stop_sequence": null}, "usage": usage}),
        ),
        sse("message_stop", &json!({"type": "message_stop"})),
    ];
    (
        [(header::CONTENT_TYPE, "text/event-stream")],
        events.concat(),
    )
        .into_response()
}
