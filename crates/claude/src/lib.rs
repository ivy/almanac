//! A `Harness` on the Claude Code CLI, built as `docs/adrs/0001-agent-sandbox.md` decides: the
//! agent loop runs here with no tools of its own, and its only tools are sandbox calls.
mod mcp;
pub mod stub;

use almanac::agent::{Grant, Harness, Pass};
use almanac::cx::StepError;
use base64::Engine;
use futures::future::BoxFuture;
use sandbox::{Sandbox, SandboxId, SandboxSpec};
use serde_json::{Value, json};
use std::process::Stdio;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::process::{Child, Command};

/// The input field holding a base64 tar of the checkout. The harness takes it out, so it never
/// reaches the model.
pub const WORKSPACE_FIELD: &str = "$workspace";
const PATCH_TIMEOUT: Duration = Duration::from_secs(60);

#[derive(Clone)]
pub struct ClaudeConfig {
    pub binary: String,
    pub api_key: String,
    /// Overrides the API endpoint. Tests point this at a stub.
    pub base_url: Option<String>,
    pub model: Option<String>,
    /// Input plus output tokens a session may use before it is killed.
    pub token_budget: u64,
    pub timeout: Duration,
    pub max_turns: u32,
    /// Replaces `--bare`, for local evaluation only. See the ADR.
    pub isolation_flags: Vec<String>,
}

impl ClaudeConfig {
    pub fn new(api_key: impl Into<String>) -> Self {
        Self {
            binary: "claude".to_string(),
            api_key: api_key.into(),
            base_url: None,
            model: None,
            token_budget: 2_000_000,
            timeout: Duration::from_secs(1800),
            max_turns: 60,
            isolation_flags: vec!["--bare".to_string()],
        }
    }
}

pub struct ClaudeHarness {
    config: ClaudeConfig,
    sandbox: Option<(Arc<dyn Sandbox>, SandboxSpec)>,
}

fn step(reason: impl std::fmt::Display) -> StepError {
    StepError(reason.to_string())
}

impl ClaudeHarness {
    pub fn new(config: ClaudeConfig) -> Self {
        Self {
            config,
            sandbox: None,
        }
    }

    /// Where sessions that may write do their work. Without this they are refused.
    pub fn with_sandbox(mut self, sandbox: Arc<dyn Sandbox>, spec: SandboxSpec) -> Self {
        self.sandbox = Some((sandbox, spec));
        self
    }

    /// One session. Workflows reach this through `Cx`, which records it.
    pub async fn session(
        &self,
        prompt: &str,
        grant: Grant,
        input: Value,
    ) -> Result<Value, StepError> {
        if grant.egress || grant.secrets {
            return Err(step(
                "this harness runs only sessions with no egress and no secrets",
            ));
        }
        if !grant.write {
            return self.converse(prompt, input, None).await;
        }
        let (sandbox, spec) = self
            .sandbox
            .as_ref()
            .ok_or_else(|| step("a session that may write needs a sandbox"))?;
        let id = sandbox.create(spec).await.map_err(step)?;
        let outcome = self.sandboxed(sandbox.clone(), &id, prompt, input).await;
        let destroyed = sandbox.destroy(&id).await;
        let value = outcome?;
        destroyed.map_err(step)?;
        Ok(value)
    }

    async fn sandboxed(
        &self,
        sandbox: Arc<dyn Sandbox>,
        id: &SandboxId,
        prompt: &str,
        mut input: Value,
    ) -> Result<Value, StepError> {
        if let Some(encoded) = input
            .as_object_mut()
            .and_then(|o| o.remove(WORKSPACE_FIELD))
        {
            let text = encoded.as_str().unwrap_or_default();
            let archive = base64::engine::general_purpose::STANDARD
                .decode(text)
                .map_err(step)?;
            sandbox.upload(id, archive).await.map_err(step)?;
        }
        baseline(sandbox.as_ref(), id).await?;
        let tools = mcp::serve(sandbox.clone(), id.clone(), self.config.timeout).await?;
        let answer = self.converse(prompt, input, Some(&tools)).await?;
        let patch = sandbox
            .exec(
                id,
                "git add -A && git diff --cached --binary",
                PATCH_TIMEOUT,
            )
            .await
            .map_err(step)?;
        if patch.exit_code != 0 {
            return Err(step(format!("could not read the patch: {}", patch.stderr)));
        }
        Ok(json!({"answer": answer, "patch": patch.stdout}))
    }

    fn command(&self, prompt: &str, home: &str, tools: Option<&mcp::Tools>) -> Command {
        let c = &self.config;
        let mut command = Command::new(&c.binary);
        command
            .args(["-p", "--tools", "", "--strict-mcp-config"])
            .args(&c.isolation_flags)
            .args(["--no-session-persistence", "--verbose"])
            .args(["--output-format", "stream-json"])
            .args(["--max-turns", &c.max_turns.to_string()])
            .args(["--system-prompt", prompt]);
        if let Some(model) = &c.model {
            command.args(["--model", model]);
        }
        if let Some(tools) = tools {
            command
                .args(["--mcp-config", &tools.config])
                .arg("--allowedTools")
                .args(mcp::allowed());
        }
        command
            .env_clear()
            .env("PATH", std::env::var("PATH").unwrap_or_default())
            .env("HOME", home)
            .env("ANTHROPIC_API_KEY", &c.api_key)
            .current_dir(home)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .kill_on_drop(true);
        if let Some(url) = &c.base_url {
            command.env("ANTHROPIC_BASE_URL", url);
        }
        command
    }

    /// The prompt is the system prompt and the input is the user turn, so hostile text is never
    /// presented as instructions.
    async fn converse(
        &self,
        prompt: &str,
        input: Value,
        tools: Option<&mcp::Tools>,
    ) -> Result<Value, StepError> {
        let home = tempfile::tempdir().map_err(step)?;
        let home_path = home.path().to_string_lossy().into_owned();
        let mut child = self
            .command(prompt, &home_path, tools)
            .spawn()
            .map_err(|e| step(format!("cannot run {}: {e}", self.config.binary)))?;
        let mut stdin = child.stdin.take().expect("stdin was piped");
        stdin
            .write_all(input.to_string().as_bytes())
            .await
            .map_err(step)?;
        drop(stdin);
        let reading = read_result(&mut child, self.config.token_budget);
        let result = tokio::time::timeout(self.config.timeout, reading).await;
        let _ = child.kill().await;
        match result {
            Err(_) => Err(step(format!(
                "session killed: over its {}s time limit",
                self.config.timeout.as_secs()
            ))),
            Ok(answer) => parse_answer(&answer?),
        }
    }
}

/// A commit to diff against, so the patch holds only what the agent changed.
async fn baseline(sandbox: &dyn Sandbox, id: &SandboxId) -> Result<(), StepError> {
    let command = "git init -q . && git -c user.name=almanac -c user.email=almanac@localhost \
                   add -A && git -c user.name=almanac -c user.email=almanac@localhost \
                   commit -q --allow-empty -m baseline";
    let out = sandbox
        .exec(id, command, PATCH_TIMEOUT)
        .await
        .map_err(step)?;
    if out.exit_code != 0 {
        return Err(step(format!(
            "could not set up the checkout: {}",
            out.stderr
        )));
    }
    Ok(())
}

fn tokens(usage: &Value) -> u64 {
    ["input_tokens", "output_tokens"]
        .iter()
        .filter_map(|k| usage[k].as_u64())
        .sum()
}

async fn read_result(child: &mut Child, budget: u64) -> Result<String, StepError> {
    let stdout = child.stdout.take().expect("stdout was piped");
    let mut lines = BufReader::new(stdout).lines();
    let mut used = 0;
    while let Some(line) = lines.next_line().await.map_err(step)? {
        let Ok(event) = serde_json::from_str::<Value>(&line) else {
            continue;
        };
        match event["type"].as_str() {
            Some("assistant") => {
                used += tokens(&event["message"]["usage"]);
                if used > budget {
                    return Err(step(format!(
                        "session killed: used {used} tokens of a {budget} budget"
                    )));
                }
            }
            Some("result") => {
                if event["is_error"].as_bool().unwrap_or(false) {
                    let reason = event["subtype"].as_str().unwrap_or("unknown");
                    return Err(step(format!("session failed: {reason}")));
                }
                return Ok(event["result"].as_str().unwrap_or_default().to_string());
            }
            _ => {}
        }
    }
    Err(step("session ended without a result"))
}

/// Accepts JSON alone or inside a code fence. Anything else is a failure, not a guess.
fn parse_answer(text: &str) -> Result<Value, StepError> {
    let trimmed = text.trim();
    let inner = trimmed
        .strip_prefix("```json")
        .or_else(|| trimmed.strip_prefix("```"))
        .and_then(|t| t.strip_suffix("```"))
        .unwrap_or(trimmed);
    serde_json::from_str(inner.trim()).map_err(|e| step(format!("output is not JSON: {e}")))
}

impl Harness for ClaudeHarness {
    fn run<'a>(
        &'a self,
        _: &'a Pass,
        prompt: &'a str,
        grant: Grant,
        input: Value,
    ) -> BoxFuture<'a, Result<Value, StepError>> {
        Box::pin(self.session(prompt, grant, input))
    }
}
