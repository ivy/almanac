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
/// The input field naming a commit to check out, as `{"repo": ..., "sha": ...}`. Like the
/// workspace, it is taken out before the model sees the input.
pub const CHECKOUT_FIELD: &str = "$checkout";
const PATCH_TIMEOUT: Duration = Duration::from_secs(60);
const FETCH_TIMEOUT: Duration = Duration::from_secs(900);
const TAIL_LINES: usize = 60;
const SOURCE_PATH: &str = ".almanac/source.tar.gz";
const DEPENDENCIES_PATH: &str = ".dependencies.tar";
/// Runs no package code: each of these only downloads what a lockfile names. See ADR 1.
const FETCH_SCRIPT: &str = "set -e; \
    if [ -f Cargo.lock ]; then cargo fetch --locked; fi; \
    find . -name go.sum -not -path './node_modules/*' -not -path './.cache/*' | while read -r sum; do \
      (cd \"$(dirname \"$sum\")\" && GOWORK=off go mod download); done; \
    if [ -f pnpm-lock.yaml ]; then pnpm fetch --ignore-scripts --store-dir /work/.cache/pnpm; fi; \
    if [ -f package-lock.json ]; then \
      npm install --ignore-scripts --no-audit --no-fund --cache /work/.cache/npm || \
      echo 'npm install failed: the cache holds what it reached' >&2; fi; \
    mkdir -p .cargo .cache; \
    tar -cf .dependencies.tar .cargo .cache";
const LINK_MODE: &str = "120000";
const EXECUTABLE_MODE: &str = "100755";

#[derive(Clone)]
pub struct ClaudeConfig {
    pub binary: String,
    pub api_key: String,
    /// A `claude setup-token`, used in place of `api_key`.
    pub oauth_token: Option<String>,
    /// Overrides the API endpoint. Tests point this at a stub.
    pub base_url: Option<String>,
    pub model: Option<String>,
    /// Tokens a session may use before it is killed, cached ones included, because a long
    /// session's context is nearly all cache reads.
    pub token_budget: u64,
    pub timeout: Duration,
    pub max_turns: u32,
    /// Stops a session at this spend, if the CLI reports one.
    pub max_budget_usd: Option<f64>,
    /// Run after the agent finishes and before the patch is read, to record whether the
    /// repository's own checks pass.
    pub verify: Option<String>,
    /// Uses the CLI's own login from this home, for local evaluation with no API key.
    pub home: Option<String>,
    /// Replaces `--bare`, which refuses an OAuth token. See the ADR.
    pub isolation_flags: Vec<String>,
}

/// What stands in for `--bare`, with [`UNBARE_ENV`].
const UNBARE_FLAGS: [&str; 2] = ["--setting-sources=", "--disable-slash-commands"];
const UNBARE_ENV: [(&str, &str); 3] = [
    ("ENABLE_TOOL_SEARCH", "false"),
    ("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "1"),
    ("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1"),
];

impl ClaudeConfig {
    pub fn new(api_key: impl Into<String>) -> Self {
        Self {
            binary: "claude".to_string(),
            api_key: api_key.into(),
            oauth_token: None,
            base_url: None,
            model: None,
            token_budget: 2_000_000,
            timeout: Duration::from_secs(1800),
            max_turns: 60,
            max_budget_usd: None,
            verify: None,
            home: None,
            isolation_flags: vec!["--bare".to_string()],
        }
    }

    /// Authenticates with a `claude setup-token` rather than an API key.
    pub fn oauth(token: impl Into<String>) -> Self {
        Self {
            oauth_token: Some(token.into()),
            isolation_flags: UNBARE_FLAGS.map(str::to_string).to_vec(),
            ..Self::new("")
        }
    }
}

/// Where a commit's source comes from.
pub trait Checkouts: Send + Sync {
    /// A gzipped tar with one top directory.
    fn tarball<'a>(
        &'a self,
        repo: &'a str,
        sha: &'a str,
    ) -> BoxFuture<'a, Result<Vec<u8>, StepError>>;
}

pub struct ClaudeHarness {
    config: ClaudeConfig,
    sandbox: Option<(Arc<dyn Sandbox>, SandboxSpec)>,
    checkouts: Option<Arc<dyn Checkouts>>,
    fetching: Option<SandboxSpec>,
}

fn step(reason: impl std::fmt::Display) -> StepError {
    StepError(reason.to_string())
}

/// A sandbox this harness made. One dropped before `destroy` finishes, as when a cancelled run's
/// future is dropped, is abandoned rather than left running.
struct Made<'a> {
    sandbox: &'a dyn Sandbox,
    id: SandboxId,
    ended: bool,
}

impl<'a> Made<'a> {
    async fn create(sandbox: &'a dyn Sandbox, spec: &SandboxSpec) -> Result<Self, StepError> {
        let id = sandbox.create(spec).await.map_err(step)?;
        Ok(Self {
            sandbox,
            id,
            ended: false,
        })
    }

    async fn destroy(mut self) -> Result<(), StepError> {
        let destroyed = self.sandbox.destroy(&self.id).await;
        self.ended = destroyed.is_ok();
        destroyed.map_err(step)
    }
}

impl Drop for Made<'_> {
    fn drop(&mut self) {
        if !self.ended {
            self.sandbox.abandon(&self.id);
        }
    }
}

impl ClaudeHarness {
    pub fn new(config: ClaudeConfig) -> Self {
        Self {
            config,
            sandbox: None,
            checkouts: None,
            fetching: None,
        }
    }

    pub fn with_checkouts(mut self, checkouts: Arc<dyn Checkouts>) -> Self {
        self.checkouts = Some(checkouts);
        self
    }

    /// Fetches a checkout's dependencies first, in a separate sandbox with no agent in it, because
    /// the agent's sandbox has no network.
    pub fn with_fetching(mut self, spec: SandboxSpec) -> Self {
        self.fetching = Some(spec);
        self
    }

    /// Unpacks in the sandbox, so a hostile archive is never read on the host.
    async fn unpack_source(
        &self,
        sandbox: &dyn Sandbox,
        id: &SandboxId,
        wrapped: Vec<u8>,
    ) -> Result<(), StepError> {
        sandbox.upload(id, wrapped).await.map_err(step)?;
        let unpack = format!("tar -xzf {SOURCE_PATH} --strip-components=1 && rm -rf .almanac");
        captured(sandbox, id, &unpack).await.map(|_| ())
    }

    async fn fetch_dependencies(
        &self,
        sandbox: &dyn Sandbox,
        spec: &SandboxSpec,
        wrapped: Vec<u8>,
    ) -> Result<Vec<u8>, StepError> {
        let made = Made::create(sandbox, spec).await?;
        let id = made.id.clone();
        let fetched = async {
            self.unpack_source(sandbox, &id, wrapped).await?;
            let out = sandbox
                .exec(&id, FETCH_SCRIPT, FETCH_TIMEOUT)
                .await
                .map_err(step)?;
            if out.exit_code != 0 {
                return Err(step(format!(
                    "fetching dependencies failed: {}",
                    out.stderr
                )));
            }
            sandbox.download(&id, DEPENDENCIES_PATH).await.map_err(step)
        };
        let result = fetched.await;
        let destroyed = made.destroy().await;
        let archive = result?;
        destroyed?;
        Ok(archive)
    }

    async fn check_out(
        &self,
        sandbox: &dyn Sandbox,
        id: &SandboxId,
        wanted: Value,
    ) -> Result<(), StepError> {
        let source = self
            .checkouts
            .as_ref()
            .ok_or_else(|| step("a checkout was asked for and none is configured"))?;
        let repo = wanted["repo"].as_str().unwrap_or_default();
        let sha = wanted["sha"].as_str().unwrap_or_default();
        let wrapped = wrapped_source(source.tarball(repo, sha).await?)?;
        if let Some(spec) = &self.fetching {
            let dependencies = self
                .fetch_dependencies(sandbox, spec, wrapped.clone())
                .await?;
            sandbox.upload(id, dependencies).await.map_err(step)?;
            let unpack = format!("tar -xf {DEPENDENCIES_PATH} && rm {DEPENDENCIES_PATH}");
            captured(sandbox, id, &unpack).await?;
        }
        self.unpack_source(sandbox, id, wrapped).await
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
        let made = Made::create(sandbox.as_ref(), spec).await?;
        let outcome = self
            .sandboxed(sandbox.clone(), &made.id, prompt, input)
            .await;
        let destroyed = made.destroy().await;
        let value = outcome?;
        destroyed?;
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
        if let Some(wanted) = input.as_object_mut().and_then(|o| o.remove(CHECKOUT_FIELD)) {
            self.check_out(sandbox.as_ref(), id, wanted).await?;
        }
        baseline(sandbox.as_ref(), id).await?;
        let tools = mcp::serve(sandbox.clone(), id.clone(), self.config.timeout).await?;
        let (text, usage) = self.conversation(prompt, input, Some(&tools)).await?;
        // The patch is the result here, and the closing message only describes it, so prose
        // costs nothing but the structure.
        let answer = parse_answer(&text).unwrap_or(Value::String(text));
        // Read first: checks can leave files behind, and those are not the agent's change.
        let changes = changes(sandbox.as_ref(), id).await?;
        let diff = captured(sandbox.as_ref(), id, "git diff --cached --binary").await?;
        let verified = match &self.config.verify {
            Some(command) => {
                let out = sandbox
                    .exec(id, command, self.config.timeout)
                    .await
                    .map_err(step)?;
                json!({"exit_code": out.exit_code, "tail": tail(&out.stdout, &out.stderr)})
            }
            None => Value::Null,
        };
        Ok(json!({
            "answer": answer, "changes": changes, "diff": diff, "usage": usage, "verified": verified,
        }))
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
            .env("HOME", c.home.as_deref().unwrap_or(home))
            .current_dir(home)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .kill_on_drop(true);
        if !c.api_key.is_empty() {
            command.env("ANTHROPIC_API_KEY", &c.api_key);
        }
        if let Some(token) = &c.oauth_token {
            command
                .env("CLAUDE_CODE_OAUTH_TOKEN", token)
                .envs(UNBARE_ENV);
        }
        if let Some(budget) = c.max_budget_usd {
            command.args(["--max-budget-usd", &budget.to_string()]);
        }
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
        let (text, _) = self.conversation(prompt, input, tools).await?;
        parse_answer(&text)
    }

    async fn conversation(
        &self,
        prompt: &str,
        input: Value,
        tools: Option<&mcp::Tools>,
    ) -> Result<(String, Value), StepError> {
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
            Ok(finished) => finished,
        }
    }
}

/// A commit to diff against, so the patch holds only what the agent changed. Scratch space, the
/// dependency cache, and build output are excluded, because a repository may not ignore them.
async fn baseline(sandbox: &dyn Sandbox, id: &SandboxId) -> Result<(), StepError> {
    let command = "git init -q . && mkdir -p .git/info && \
                   printf '.tmp/\\n.cargo/\\n.cache/\\ntarget/\\nnode_modules/\\n__pycache__/\\n' >> .git/info/exclude && git add -A && \
                   git -c user.name=almanac -c user.email=almanac@localhost \
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

/// A plain tar holding the gzipped source as one file, so `Sandbox::upload` can place it without
/// the host reading what is inside.
fn wrapped_source(tarball: Vec<u8>) -> Result<Vec<u8>, StepError> {
    let mut builder = tar::Builder::new(vec![]);
    let mut header = tar::Header::new_gnu();
    header.set_size(tarball.len() as u64);
    header.set_mode(0o644);
    header.set_cksum();
    builder
        .append_data(&mut header, SOURCE_PATH, tarball.as_slice())
        .map_err(step)?;
    builder.into_inner().map_err(step)
}

async fn captured(
    sandbox: &dyn Sandbox,
    id: &SandboxId,
    command: &str,
) -> Result<String, StepError> {
    let out = sandbox
        .exec(id, command, PATCH_TIMEOUT)
        .await
        .map_err(step)?;
    if out.exit_code != 0 {
        return Err(step(format!("`{command}` failed: {}", out.stderr)));
    }
    Ok(out.stdout)
}

/// What the agent changed, as file changes. Everything read here is the agent's to shape, and
/// a false report only changes what the patch policy sees and what gets pushed.
async fn changes(sandbox: &dyn Sandbox, id: &SandboxId) -> Result<Vec<Value>, StepError> {
    let raw = "git add -A && git diff --cached --raw --no-renames --abbrev=40 -z";
    let listing = captured(sandbox, id, raw).await?;
    let mut fields = listing.split('\0');
    let mut changes = vec![];
    while let (Some(meta), Some(path)) = (fields.next(), fields.next()) {
        let columns: Vec<&str> = meta.trim_start_matches(':').split(' ').collect();
        let [_, mode, _, blob, status] = columns[..] else {
            return Err(step("unreadable change list"));
        };
        changes.push(if status == "D" {
            json!({"Delete": {"path": path}})
        } else if mode == LINK_MODE {
            json!({"Link": {"path": path}})
        } else if blob.len() == 40 && blob.chars().all(|c| c.is_ascii_hexdigit()) {
            let read = format!("git cat-file blob {blob} | base64 | tr -d '\\n'");
            let content = captured(sandbox, id, &read).await?;
            let executable = mode == EXECUTABLE_MODE;
            json!({"Write": {"path": path, "content": content, "executable": executable}})
        } else {
            return Err(step("unreadable change list"));
        });
    }
    Ok(changes)
}

fn tokens(usage: &Value) -> u64 {
    [
        "input_tokens",
        "output_tokens",
        "cache_creation_input_tokens",
        "cache_read_input_tokens",
    ]
    .iter()
    .filter_map(|k| usage[k].as_u64())
    .sum()
}

/// The end of a command's output, where a failure usually shows.
fn tail(stdout: &str, stderr: &str) -> String {
    let all = format!("{stdout}{stderr}");
    let lines: Vec<&str> = all.lines().collect();
    lines[lines.len().saturating_sub(TAIL_LINES)..].join("\n")
}

async fn read_result(child: &mut Child, budget: u64) -> Result<(String, Value), StepError> {
    let stdout = child.stdout.take().expect("stdout was piped");
    let mut lines = BufReader::new(stdout).lines();
    let mut used = 0;
    let mut counted = None;
    while let Some(line) = lines.next_line().await.map_err(step)? {
        let Ok(event) = serde_json::from_str::<Value>(&line) else {
            continue;
        };
        match event["type"].as_str() {
            Some("assistant") => {
                // A message arrives as one event per content block, each repeating its usage.
                let id = event["message"]["id"].as_str().map(str::to_string);
                if id.is_none() || id != counted {
                    used += tokens(&event["message"]["usage"]);
                }
                counted = id;
                if used > budget {
                    return Err(step(format!(
                        "session killed: used {used} tokens of a {budget} budget"
                    )));
                }
            }
            Some("result") => {
                if event["is_error"].as_bool().unwrap_or(false) {
                    let reason = event["subtype"].as_str().unwrap_or("unknown");
                    let cost = &event["total_cost_usd"];
                    return Err(step(format!(
                        "session failed: {reason}, costing {cost} USD"
                    )));
                }
                let usage = json!({
                    "tokens": used,
                    "cost_usd": event["total_cost_usd"],
                    "turns": event["num_turns"],
                });
                let text = event["result"].as_str().unwrap_or_default().to_string();
                return Ok((text, usage));
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
