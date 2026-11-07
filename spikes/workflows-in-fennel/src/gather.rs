//! Runs `gather`. The script is a coroutine that yields a request for each step, and this loop
//! performs it through `Cx` and resumes the script with the answer. `Cx` never enters Lua, so a
//! script has no reference to hold on to, and a failed step ends the run instead of reaching a
//! `pcall`.
use crate::manifest::{AgentDecl, Kind, Piece};
use crate::pure;
use crate::sandbox::Vm;
use crate::value::{AgeRead, Proof, Script, Tainted, intern, reveal};
use crate::workflow::FennelWorkflow;
use almanac::__private::Sealed;
use almanac::agent::{AgentSpec, Template};
use almanac::prelude::*;
use almanac_renovate::update::Ages;
use futures::future::BoxFuture;
use github::client::GithubClient;
use mlua::thread::ThreadStatus;
use mlua::{Function, MultiValue, Table, Thread, Value as LuaValue};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::sync::Arc;
use std::time::Duration;

observed! {
    /// What a read produced, as the run log stores it. It is tagged as a whole: what is clean
    /// inside it is sorted out again by `Script::from_record`.
    pub struct Reading {
        pub value: Untrusted<Value>,
    }
}

/// Prompt text a script's declaration rendered. `#[prompt]` fixes these at compile time, and
/// here the check happens at load, so the constants say nothing.
pub struct Rendered(String);

impl Template for Rendered {
    const SOURCE: &'static str = "declared in the workflow";
    const VARS: &'static [&'static str] = &[];
    fn render(&self) -> String {
        self.0.clone()
    }
}

/// `Clean` is sealed because a type must prove it holds no free text. A script's value has no
/// static type, so this checks the proof when the value is built: the only strings allowed are
/// slugs. This is the hand-written seal `taint::__private` calls the deliberate way around.
#[derive(Clone, Serialize, Deserialize)]
#[serde(try_from = "Value", into = "Value")]
pub struct CleanValue(Value);

impl Sealed for CleanValue {}
impl Clean for CleanValue {}

impl From<CleanValue> for Value {
    fn from(clean: CleanValue) -> Value {
        clean.0
    }
}

fn is_clean(value: &Value) -> bool {
    match value {
        Value::String(text) => text.parse::<Slug>().is_ok(),
        Value::Array(items) => items.iter().all(is_clean),
        Value::Object(map) => !map.contains_key("$untrusted") && map.values().all(is_clean),
        _ => true,
    }
}

impl TryFrom<Value> for CleanValue {
    type Error = String;
    fn try_from(value: Value) -> Result<Self, String> {
        match is_clean(&value) {
            true => Ok(Self(value)),
            false => Err("a worker's input and output hold no free text".to_string()),
        }
    }
}

fn failed(error: impl ToString) -> Halt {
    Halt::Failed(StepError(error.to_string()))
}

struct Session<'a> {
    workflow: &'a FennelWorkflow,
    cx: &'a Cx,
    vm: &'a Vm,
}

fn step_error(halt: Halt) -> StepError {
    match halt {
        Halt::Failed(error) => error,
        Halt::Stop(reason) => StepError(reason),
    }
}

fn args_of(values: &[LuaValue]) -> Result<Vec<Script>, Halt> {
    let read = values.iter().map(Script::from_lua);
    read.collect::<Result<_, _>>().map_err(failed)
}

fn text_arg(values: &[LuaValue], at: usize, what: &str) -> Result<String, Halt> {
    match values.get(at) {
        Some(LuaValue::String(text)) => Ok(text.to_string_lossy()),
        _ => Err(failed(format!("{what} must be a string"))),
    }
}

fn prompt_value(value: &Script) -> Result<String, Halt> {
    match value {
        Script::Text(text) => match text.parse::<Slug>() {
            Ok(slug) => Ok(slug.to_string()),
            Err(_) => Err(failed(
                "a prompt variable must be a slug, a number, or a bool",
            )),
        },
        Script::Int(n) => Ok(n.to_string()),
        Script::Bool(b) => Ok(b.to_string()),
        Script::Tainted(_) => Err(failed("hostile text never reaches a prompt's instructions")),
        _ => Err(failed(
            "a prompt variable must be a slug, a number, or a bool",
        )),
    }
}

/// `{{ repo }}` filled in. A variable the declaration has must have a value, and nothing else may.
fn render(agent: &AgentDecl, values: &Script) -> Result<Rendered, Halt> {
    let Script::Map(values) = values else {
        return Err(failed("`:prompt` must be a table of its variables"));
    };
    if let Some(extra) = values.keys().find(|key| !agent.vars.contains(key)) {
        return Err(failed(format!(
            "`{}` has no prompt variable `{extra}`",
            agent.name
        )));
    }
    let mut out = String::new();
    for piece in &agent.template {
        match piece {
            Piece::Text(text) => out.push_str(text),
            Piece::Var(var) => {
                let value = values.get(var).ok_or_else(|| {
                    failed(format!("`{}` needs prompt variable `{var}`", agent.name))
                })?;
                out.push_str(&prompt_value(value)?);
            }
        }
    }
    Ok(Rendered(out))
}

impl Session<'_> {
    fn agent(&self, name: &str) -> Result<&AgentDecl, Halt> {
        let found = self.workflow.declaration().agents.get(name);
        found.ok_or_else(|| failed(format!("agent `{name}` is not declared by this workflow")))
    }

    fn reply(&self, value: &Script) -> Result<MultiValue, Halt> {
        let value = value.into_lua(&self.vm.lua).map_err(failed)?;
        Ok(MultiValue::from_vec(vec![value]))
    }

    async fn get(&self, name: &str) -> Result<MultiValue, Halt> {
        let needs = &self.workflow.declaration().needs;
        let Some(need) = needs.iter().find(|need| need.name == name) else {
            return Err(failed(format!(
                "`{name}` is not in this workflow's manifest, so it cannot be used. Name it \
                 literally in `cx.get` where the file's functions can be seen to reach it"
            )));
        };
        let value = (need.resolve)(self.cx)?;
        self.reply(&value)
    }

    /// The read runs on a coroutine of its own inside `Cx::observe`, so on a replay it is skipped
    /// and the recorded value comes back by the same path.
    async fn observe(&self, name: &str, read: Function) -> Result<MultiValue, Halt> {
        if !self
            .workflow
            .declaration()
            .reads
            .iter()
            .any(|read| read == name)
        {
            return Err(failed(format!(
                "`{name}` is not an observation this workflow declares: name it literally in \
                 `cx.observe`, because the run log is keyed by it"
            )));
        }
        let thread = self.vm.lua.create_thread(read).map_err(failed)?;
        let reading = async {
            let value = drive(self, thread, MultiValue::new(), true).await;
            let value = value.map_err(step_error)?;
            Ok(Reading {
                value: Untrusted::new(value.to_record()),
            })
        };
        let reading = self.cx.observe(intern(name), reading).await?;
        let value = Script::from_record(&reveal(&reading.value)?)?;
        self.reply(&value)
    }

    async fn cooled(&self, values: &[LuaValue]) -> Result<MultiValue, Halt> {
        let seconds = match values.first() {
            Some(LuaValue::Integer(n)) => *n as f64,
            Some(LuaValue::Number(n)) => *n,
            _ => return Err(failed("`cx.cooled` takes a minimum age in seconds")),
        };
        let min_age = Duration::try_from_secs_f64(seconds).map_err(failed)?;
        let Some(LuaValue::UserData(read)) = values.get(1) else {
            return Err(failed("`cx.cooled` takes the result of `ages.newest`"));
        };
        let update = {
            let read = read.borrow::<AgeRead>();
            let read = read.map_err(|_| failed("`cx.cooled` takes the result of `ages.newest`"))?;
            read.0.clone()
        };
        let ages = self.cx.get::<dyn Ages>()?;
        match self.cx.cooled(min_age, ages.newest(&update)).await? {
            None => Ok(MultiValue::new()),
            Some(cooled) => {
                let proof = self.vm.lua.create_userdata(Proof(Arc::new(cooled)));
                let proof = proof.map_err(failed)?;
                Ok(MultiValue::from_vec(vec![LuaValue::UserData(proof)]))
            }
        }
    }

    fn proof(options: &Table) -> Result<Arc<Cooled>, Halt> {
        let value: LuaValue = options.get("cooled").map_err(failed)?;
        let refused = || failed("`:cooled` must be the proof `cx.cooled` returned");
        let LuaValue::UserData(data) = value else {
            return Err(refused());
        };
        let proof = data.borrow::<Proof>().map_err(|_| refused())?;
        Ok(proof.0.clone())
    }

    fn field(options: &Table, key: &str) -> Result<Script, Halt> {
        let value: LuaValue = options.get(key).map_err(failed)?;
        Script::from_lua(&value).map_err(failed)
    }

    /// An agent's input, with its checkout under the `$checkout` key the harness takes it from.
    fn input(options: &Table) -> Result<Value, Halt> {
        let mut input = Self::field(options, "input")?.to_wire();
        if input.is_null() {
            input = json!({});
        }
        if let Script::Map(_) = Self::field(options, "checkout")? {
            input["$checkout"] = Self::field(options, "checkout")?.to_wire();
        }
        Ok(input)
    }

    async fn agent_step(&self, verb: &str, values: &[LuaValue]) -> Result<MultiValue, Halt> {
        let agent = self.agent(&text_arg(values, 0, "an agent's name")?)?;
        let Some(LuaValue::Table(options)) = values.get(1) else {
            return Err(failed("an agent step takes a table of options"));
        };
        let prompt = render(agent, &Self::field(options, "prompt")?)?;
        let input = Self::input(options)?;
        let answer = match (agent.profile, verb) {
            (Kind::Fixer, "repair") => {
                let proof = Self::proof(options)?;
                let spec = AgentSpec::<Fixer, Rendered>::fixer(agent.name);
                let bound = self.cx.agent(spec);
                let patch = bound.repair::<Value, Patch>(prompt, &input, &proof);
                Script::Tainted(Tainted::Patch(patch.await?))
            }
            (Kind::Quarantined, "read") => {
                let spec = AgentSpec::<Quarantined, Rendered>::quarantined(agent.name);
                let bound = self.cx.agent(spec);
                let answer = bound.read::<Value, Value>(prompt, &input).await?;
                Script::Tainted(Tainted::Answer(answer))
            }
            (Kind::Worker, "act") => {
                let input = CleanValue::try_from(input).map_err(failed)?;
                let spec = AgentSpec::<Worker, Rendered>::worker(agent.name);
                let bound = self.cx.agent(spec);
                let answer = bound.act::<_, CleanValue>(prompt, &input).await?;
                Script::from_record(&answer.0)?
            }
            (profile, verb) => {
                return Err(failed(format!("a {profile:?} agent cannot `{verb}`")));
            }
        };
        self.reply(&answer)
    }

    async fn github(&self, operation: &str, args: &[Script]) -> Result<MultiValue, Halt> {
        let client = self.cx.get::<GithubClient>()?;
        let value = crate::github::read(&client, operation, args).await?;
        self.reply(&value)
    }

    async fn step(&self, request: MultiValue, reading: bool) -> Result<MultiValue, Halt> {
        let values: Vec<LuaValue> = request.into_iter().collect();
        let op = text_arg(&values, 0, "a step")?;
        let rest = &values[1..];
        if op == "github" {
            if !reading {
                return Err(failed(
                    "a read outside `cx.observe` would not be recorded, so a replay could not \
                     repeat it: wrap it in `cx.observe`",
                ));
            }
            return self
                .github(&text_arg(rest, 0, "a read")?, &args_of(&rest[1..])?)
                .await;
        }
        if reading {
            return Err(failed(format!(
                "`cx.{op}` inside `cx.observe` would be skipped on replay: do it outside the read"
            )));
        }
        match op.as_str() {
            "get" => self.get(&text_arg(rest, 0, "a provider's name")?).await,
            "observe" => {
                let name = text_arg(rest, 0, "an observation's name")?;
                let Some(LuaValue::Function(read)) = rest.get(1) else {
                    return Err(failed("`cx.observe` takes a name and a function"));
                };
                self.observe(&name, read.clone()).await
            }
            "attempts" => {
                let attempts = self.cx.attempts().await?;
                self.reply(&Script::map([
                    ("used", Script::Int(attempts.used as i64)),
                    ("asked", Script::Bool(attempts.asked)),
                ]))
            }
            "cooled" => self.cooled(rest).await,
            "hold" => {
                let context = intern(&text_arg(rest, 0, "a status context")?);
                let repo: Slug = text_arg(rest, 1, "a repo")?.parse().map_err(failed)?;
                let sha: Sha = text_arg(rest, 2, "a sha")?.parse().map_err(failed)?;
                self.cx.hold(context, &repo, &sha).await?;
                Ok(MultiValue::new())
            }
            verb @ ("repair" | "read" | "act") => self.agent_step(verb, rest).await,
            other => Err(failed(format!("no step named `{other}`"))),
        }
    }
}

/// Resumes `thread` until it finishes, performing each step it asks for.
fn drive<'a>(
    session: &'a Session<'a>,
    thread: Thread,
    first: MultiValue,
    reading: bool,
) -> BoxFuture<'a, Result<Script, Halt>> {
    Box::pin(async move {
        let mut reply = first;
        loop {
            let out: MultiValue = thread.resume(reply).map_err(failed)?;
            if thread.status() != ThreadStatus::Resumable {
                let value = out.into_iter().next().unwrap_or(LuaValue::Nil);
                return Script::from_lua(&value).map_err(failed);
            }
            reply = session.step(out, reading).await?;
        }
    })
}

/// Runs `gather` in a VM of its own and returns its facts.
pub async fn run(workflow: &FennelWorkflow, cx: &Cx, event: &Script) -> Result<Script, Halt> {
    let vm = Vm::new().map_err(failed)?;
    pure::install_taint(&vm, workflow).map_err(failed)?;
    install_ages(&vm).map_err(failed)?;
    let chunk: Table = vm.load(workflow.name(), workflow.lua()).map_err(failed)?;
    let gather: Function = chunk.get("gather").map_err(failed)?;
    let thread = vm.lua.create_thread(gather).map_err(failed)?;
    let event = pure::event_table(&vm, workflow, event).map_err(failed)?;
    let args = MultiValue::from_vec(vec![LuaValue::Table(vm.cx.clone()), event]);
    let session = Session {
        workflow,
        cx,
        vm: &vm,
    };
    drive(&session, thread, args, false).await
}

/// `ages.newest` builds a read and performs nothing, so holding one proves nothing.
fn install_ages(vm: &Vm) -> mlua::Result<()> {
    let newest = vm.lua.create_function(|_, update: LuaValue| {
        let refused = || mlua::Error::runtime("`ages.newest` takes an update from `taint.map`");
        let LuaValue::UserData(data) = update else {
            return Err(refused());
        };
        let tainted = data.borrow::<Tainted>().map_err(|_| refused())?;
        match &*tainted {
            Tainted::Update(update) => Ok(AgeRead(update.clone())),
            _ => Err(refused()),
        }
    })?;
    let ages: Table = vm.providers.get("ages")?;
    ages.set("newest", newest)
}
