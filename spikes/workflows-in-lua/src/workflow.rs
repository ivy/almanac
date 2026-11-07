//! A workflow file as the runtime sees it. `gather` runs as a coroutine: each `cx` call yields a
//! request, and this file serves it with the real `Cx`, so every read, model call, and hold goes
//! through the same recording a compiled workflow gets. A `Lua` value never outlives one run.
use crate::catalog::Catalog;
use crate::declare::{self, Agent, AgentProfile, Declared, Piece, SubjectSpec, VarKind};
use crate::effects::effects_of;
use crate::sandbox::Vm;
use crate::taint::Tainted;
use crate::value::Node;
use almanac::agent::Template;
use almanac::cooled::Cooled;
use almanac::prelude::*;
use almanac::providers::Requirement;
use almanac::workflow::{Decided, Found, Identity};
use futures::future::BoxFuture;
use mlua::{Function, MultiValue, Table, Value, thread::ThreadStatus};
use serde_json::{Value as Json, json};
use std::time::Duration;

/// A prompt rendered for one call. `Template`'s constants describe a compiled prompt; the manifest
/// here is built from `Agent`, so they are never read.
struct Rendered(String);

impl Template for Rendered {
    const SOURCE: &'static str = "lua";
    const VARS: &'static [&'static str] = &[];
    fn render(&self) -> String {
        self.0.clone()
    }
}

almanac::clean! {
    /// All a `Worker` may be told.
    pub struct WorkerInput { pub flag: Option<bool>, pub name: Option<Slug> }
}

almanac::clean! {
    /// All a `Worker` may answer.
    pub struct WorkerOutput { pub ok: bool }
}

#[derive(Debug)]
pub struct LoadError(pub String);

impl std::fmt::Display for LoadError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for LoadError {}

pub struct LuaWorkflow {
    source: &'static str,
    file: &'static str,
    declared: Declared,
}

fn failed(error: impl ToString) -> Halt {
    Halt::Failed(StepError(error.to_string()))
}

impl LuaWorkflow {
    /// Runs the file once, to read its table. This is the moment a compiled workflow's macro
    /// errors happen, so a binary should do it at startup, and a test should do it in CI.
    pub fn load(
        source: &'static str,
        file: &'static str,
        catalog: &Catalog,
    ) -> Result<Self, LoadError> {
        let refuse = |e: String| LoadError(format!("{file}: {e}"));
        let vm = Vm::new().map_err(|e| refuse(e.to_string()))?;
        let table = vm
            .evaluate(source, file)
            .map_err(|e| refuse(e.to_string()))?;
        let declared = declare::declared(&table, catalog).map_err(refuse)?;
        Ok(Self {
            source,
            file,
            declared,
        })
    }

    pub fn declared(&self) -> &Declared {
        &self.declared
    }

    fn vm(&self) -> Result<(Vm, Table), String> {
        let vm = Vm::new().map_err(|e| e.to_string())?;
        let table = vm
            .evaluate(self.source, self.file)
            .map_err(|e| e.to_string())?;
        Ok((vm, table))
    }

    fn event(&self, payload: &Json) -> Result<Json, Malformed> {
        (self.declared.event.parse)(payload)
    }

    fn key(&self, event: &Json) -> String {
        let mut key = String::new();
        for piece in &self.declared.key {
            match piece {
                Piece::Text(text) => key.push_str(text),
                Piece::Var(name) => match &event[name] {
                    Json::String(text) => key.push_str(text),
                    other => key.push_str(&other.to_string()),
                },
            }
        }
        key
    }

    fn subjects(&self, event: &Json) -> Result<Vec<Subject>, Malformed> {
        let slug = |name: &str| {
            event[name]
                .as_str()
                .and_then(|text| text.parse::<Slug>().ok())
                .ok_or_else(|| Malformed {
                    pointer: format!("/{name}"),
                })
        };
        let subject = |spec: &SubjectSpec| match spec {
            SubjectSpec::Repo(repo) => Ok(Subject::repo(&slug(repo)?)),
            SubjectSpec::Package(repo, package) => {
                Ok(Subject::package(&slug(repo)?, &slug(package)?))
            }
        };
        self.declared.subjects.iter().map(subject).collect()
    }

    /// `when`, in a VM of its own. An error stops the event: a check that cannot answer must not
    /// start a run.
    fn wanted(&self, event: &Json) -> Result<bool, String> {
        if !self.declared.has_when {
            return Ok(true);
        }
        let (vm, table) = self.vm()?;
        let when: Function = table.raw_get("when").map_err(|e| e.to_string())?;
        let event = Node::from_observed(event)
            .to_lua(&vm.lua)
            .map_err(|e| e.to_string())?;
        when.call::<bool>(event).map_err(|e| e.to_string())
    }

    /// Serves one `gather` to its end. A failed request stops the coroutine where it stands
    /// rather than raising inside it, so a script's `pcall` cannot swallow an outage, and no error
    /// text from a read reaches a script.
    pub async fn gather(&self, cx: &Cx, event: &Json) -> Result<Node, Halt> {
        let (vm, table) = self.vm().map_err(failed)?;
        let mut operations = vec!["attempts", "hold", "read", "repair", "act"];
        operations.extend(self.declared.reads.iter().map(|(name, _)| *name));
        let handle = vm.cx(&operations).map_err(failed)?;
        let gather: Function = table.raw_get("gather").map_err(failed)?;
        let thread = vm.lua.create_thread(gather).map_err(failed)?;
        let event = Node::from_observed(event).to_lua(&vm.lua).map_err(failed)?;
        let mut proofs: Vec<Cooled> = vec![];
        let mut input = MultiValue::from_vec(vec![handle, event]);
        loop {
            let output: MultiValue = thread.resume(input).map_err(failed)?;
            if thread.status() == ThreadStatus::Finished {
                let first = output.into_iter().next().unwrap_or(Value::Nil);
                return Node::from_lua(&first).map_err(failed);
            }
            let (operation, arguments) = request(output).map_err(failed)?;
            let reply = self.serve(cx, &operation, arguments, &mut proofs).await?;
            let reply = reply.to_lua(&vm.lua).map_err(failed)?;
            input = MultiValue::from_vec(vec![reply]);
        }
    }

    async fn serve(
        &self,
        cx: &Cx,
        operation: &str,
        arguments: Vec<Node>,
        proofs: &mut Vec<Cooled>,
    ) -> Result<Node, Halt> {
        match operation {
            "attempts" => {
                let attempts = cx.attempts().await?;
                Ok(Node::map([
                    ("used", Node::Num(attempts.used.into())),
                    ("asked", Node::Bool(attempts.asked)),
                ]))
            }
            "hold" => self.hold(cx, &arguments).await,
            "read" => self.read(cx, &arguments).await,
            "repair" => self.repair(cx, &arguments, proofs).await,
            "act" => self.act(cx, &arguments).await,
            name => {
                let (_, spec) = self
                    .declared
                    .reads
                    .iter()
                    .find(|(read, _)| *read == name)
                    .ok_or_else(|| failed(format!("no read named `{name}` is declared")))?;
                (spec.run)(cx, arguments, proofs).await
            }
        }
    }

    async fn hold(&self, cx: &Cx, arguments: &[Node]) -> Result<Node, Halt> {
        let argument = |i: usize| arguments.get(i).and_then(Node::as_str).unwrap_or_default();
        let context = self.declared.literals.iter().find(|l| **l == argument(0));
        let context = context
            .copied()
            .ok_or_else(|| failed("hold: the context must be listed in `literals`"))?;
        let repo: Slug = argument(1).parse().map_err(failed)?;
        let sha: Sha = argument(2).parse().map_err(failed)?;
        cx.hold(context, &repo, &sha).await?;
        Ok(Node::Nil)
    }

    /// The agent a call names, or why it may not make this call. `Bound` makes the wrong verb a
    /// compile error, and this is what stands in for it.
    fn agent(&self, name: &Node, profile: AgentProfile, verb: &str) -> Result<&Agent, Halt> {
        let name = name.as_str().unwrap_or_default();
        let found = self.declared.agents.iter().find(|a| a.name == name);
        let agent = found.ok_or_else(|| failed(format!("agent `{name}` is not declared")))?;
        if agent.profile != profile {
            let info = agent.info();
            return Err(failed(format!(
                "agent `{name}` is {}, so it cannot `{verb}`",
                info.profile
            )));
        }
        Ok(agent)
    }

    async fn repair(&self, cx: &Cx, arguments: &[Node], proofs: &[Cooled]) -> Result<Node, Halt> {
        let [name, vars, input, proof] = arguments else {
            return Err(failed(
                "repair takes an agent, its vars, its input, and a Cooled proof",
            ));
        };
        let agent = self.agent(name, AgentProfile::Fixer, "repair")?;
        let prompt = render(agent, vars)?;
        let input = input.to_agent_input().map_err(failed)?;
        let Node::Proof(ticket) = proof else {
            return Err(failed(format!(
                "repair needs a Cooled proof from cx.cooled, not {}",
                proof.shape()
            )));
        };
        let proof = proofs
            .get(*ticket)
            .ok_or_else(|| failed("no such proof in this run"))?;
        let spec = AgentSpec::<Fixer, Rendered>::fixer(agent.name);
        let patch: Untrusted<Patch> = cx.agent(spec).repair(prompt, &input, proof).await?;
        Ok(Node::Tainted(Tainted::Patch(patch)))
    }

    async fn read(&self, cx: &Cx, arguments: &[Node]) -> Result<Node, Halt> {
        let [name, vars, input] = arguments else {
            return Err(failed("read takes an agent, its vars, and its input"));
        };
        let agent = self.agent(name, AgentProfile::Quarantined, "read")?;
        let prompt = render(agent, vars)?;
        let input = input.to_agent_input().map_err(failed)?;
        let spec = AgentSpec::<Quarantined, Rendered>::quarantined(agent.name);
        let answer: Untrusted<Json> = cx.agent(spec).read(prompt, &input).await?;
        Ok(Node::Tainted(Tainted::Answer(answer)))
    }

    async fn act(&self, cx: &Cx, arguments: &[Node]) -> Result<Node, Halt> {
        let [name, vars, input] = arguments else {
            return Err(failed("act takes an agent, its vars, and its input"));
        };
        let agent = self.agent(name, AgentProfile::Worker, "act")?;
        let prompt = render(agent, vars)?;
        let input = worker_input(input)?;
        let spec = AgentSpec::<Worker, Rendered>::worker(agent.name);
        let output: WorkerOutput = cx.agent(spec).act(prompt, &input).await?;
        Ok(Node::map([("ok", Node::Bool(output.ok))]))
    }

    /// One branch of `decide`, in a VM that has never held a `cx`. It reloads the file, so nothing
    /// `gather` did can be seen here except what it put in `facts`.
    pub fn decide(&self, facts: &Node) -> Result<Vec<Effect>, String> {
        if facts.holds_proof() {
            return Err("a Cooled proof cannot be a fact: it means nothing to policy".to_string());
        }
        let outcome = facts.field("kind").as_str().ok_or_else(|| {
            "`gather` must return a table whose `kind` names a branch of `decide`".to_string()
        })?;
        if !self.declared.outcomes.contains(outcome) {
            return Err(format!("`decide` has no branch for `{outcome}`"));
        }
        let (vm, table) = self.vm()?;
        let decide: Table = table.raw_get("decide").map_err(|e| e.to_string())?;
        let branch: Function = decide.raw_get(outcome).map_err(|e| e.to_string())?;
        let input = facts.to_lua(&vm.lua).map_err(|e| e.to_string())?;
        let effects: Value = branch.call(input).map_err(|e| e.to_string())?;
        let effects = Node::from_lua(&effects)?;
        effects_of(&effects, &self.declared.literals)
    }
}

/// What a yield asked for. Every yield comes from the closures `Vm::cx` makes, because scripts
/// have no `coroutine`, so anything else is a bug in the host.
fn request(output: MultiValue) -> Result<(String, Vec<Node>), String> {
    let mut values = output.into_iter();
    let operation = match values.next() {
        Some(Value::String(name)) => name.to_str().map_err(|e| e.to_string())?.to_string(),
        _ => return Err("a yield did not name an operation".to_string()),
    };
    let arguments = values.map(|value| Node::from_lua(&value));
    Ok((operation, arguments.collect::<Result<_, _>>()?))
}

/// Fills a prompt's variables, each checked against its declared kind. Anything that fails stops
/// the run before a model is called.
fn render(agent: &Agent, vars: &Node) -> Result<Rendered, Halt> {
    let refuse = |message: String| failed(format!("agent `{}`: {message}", agent.name));
    let Node::Map(given) = vars else {
        return Err(refuse(format!(
            "vars must be a table, not {}",
            vars.shape()
        )));
    };
    if let Some(extra) = given
        .keys()
        .find(|k| !agent.vars.iter().any(|(v, _)| v == *k))
    {
        return Err(refuse(format!("its prompt has no variable `{extra}`")));
    }
    let mut out = String::new();
    for piece in &agent.pieces {
        match piece {
            Piece::Text(text) => out.push_str(text),
            Piece::Var(name) => {
                let value = given
                    .get(name)
                    .ok_or_else(|| refuse(format!("`{name}` is missing")))?;
                let kind = agent.vars.iter().find(|(v, _)| v == name).map(|(_, k)| *k);
                let text = clean_text(value, kind)
                    .map_err(|e| refuse(format!("prompt variable `{name}` {e}")))?;
                out.push_str(&text);
            }
        }
    }
    Ok(Rendered(out))
}

fn clean_text(value: &Node, kind: Option<VarKind>) -> Result<String, String> {
    let shape = || format!("is {}", value.shape());
    match (kind, value) {
        (Some(VarKind::Slug), Node::Str(s)) => s
            .parse::<Slug>()
            .map(|s| s.to_string())
            .map_err(|_| "is not a Slug".to_string()),
        (Some(VarKind::Sha), Node::Str(s)) => s
            .parse::<Sha>()
            .map(|s| s.to_string())
            .map_err(|_| "is not a Sha".to_string()),
        (Some(VarKind::Number), node) => node.as_u64().map(|n| n.to_string()).ok_or_else(shape),
        (Some(VarKind::Flag), Node::Bool(b)) => Ok(b.to_string()),
        _ => Err(shape()),
    }
}

/// A `Worker` has credentials, so nothing tainted reaches one, and a string must be a `Slug`
/// rather than free text.
fn worker_input(input: &Node) -> Result<WorkerInput, Halt> {
    if input.holds_taint() {
        return Err(failed(
            "a Worker takes only Clean input, and this holds Untrusted",
        ));
    }
    let Node::Map(fields) = input else {
        return Err(failed(format!(
            "a Worker's input is a table of Clean fields, not {}",
            input.shape()
        )));
    };
    let name = match fields.get("name") {
        None => None,
        Some(Node::Str(text)) => Some(text.parse::<Slug>().map_err(failed)?),
        Some(other) => {
            return Err(failed(format!(
                "`name` must be a Slug, not {}",
                other.shape()
            )));
        }
    };
    let flag = match fields.get("flag") {
        None => None,
        Some(Node::Bool(flag)) => Some(*flag),
        Some(other) => {
            return Err(failed(format!(
                "`flag` must be a boolean, not {}",
                other.shape()
            )));
        }
    };
    if fields.keys().any(|k| k != "name" && k != "flag") {
        return Err(failed("a Worker's input has only `name` and `flag`"));
    }
    Ok(WorkerInput { flag, name })
}

/// A duration a script chose. `Duration` panics on a negative or non-finite number.
pub fn seconds(node: Option<&Node>) -> Result<Duration, Halt> {
    match node {
        Some(Node::Num(n)) => {
            Duration::try_from_secs_f64(*n).map_err(|_| failed("a duration must be positive"))
        }
        _ => Err(failed("a duration is a number of seconds")),
    }
}

impl Registered for LuaWorkflow {
    fn name(&self) -> &'static str {
        self.declared.name
    }
    fn intent(&self) -> &'static str {
        self.declared.intent
    }
    fn kind(&self) -> &'static str {
        self.declared.event.kind
    }
    fn handles(&self, kind: &str) -> bool {
        kind == self.declared.event.kind || self.declared.event.also.contains(&kind)
    }
    fn runs_per_hour(&self) -> u32 {
        self.declared.runs_per_hour
    }
    fn may(&self) -> &'static [EffectKind] {
        self.declared.may
    }
    fn agent_names(&self) -> Vec<&'static str> {
        self.declared.agents.iter().map(|a| a.name).collect()
    }
    fn requirements(&self) -> Vec<Requirement> {
        let reads = self.declared.reads.iter().map(|(_, spec)| spec.requires);
        let mut needs: Vec<Requirement> = vec![];
        for need in reads
            .chain(self.declared.scan.map(|s| s.requires))
            .flat_map(|f| f())
        {
            if !needs.contains(&need) {
                needs.push(need);
            }
        }
        needs
    }
    fn identify(&self, payload: &Json) -> Result<Identity, Malformed> {
        let event = self.event(payload)?;
        let wanted = self.wanted(&event).map_err(|reason| Malformed {
            pointer: format!("when: {reason}"),
        })?;
        Ok(Identity {
            key: self.key(&event),
            subjects: self.subjects(&event)?,
            wanted,
        })
    }
    fn execute<'a>(
        &'a self,
        cx: &'a Cx,
        payload: &'a Json,
    ) -> BoxFuture<'a, Result<Decided, Halt>> {
        Box::pin(async move {
            let event = self.event(payload).map_err(StepError::from)?;
            let facts = self.gather(cx, &event).await?;
            let effects = self.decide(&facts).map_err(failed)?;
            Ok(Decided {
                effects,
                links: vec![],
            })
        })
    }
    fn scan<'a>(&'a self, scanner: &'a Scanner) -> BoxFuture<'a, Result<Vec<Found>, StepError>> {
        match self.declared.scan {
            Some(spec) => (spec.run)(scanner),
            None => Box::pin(async { Ok(vec![]) }),
        }
    }
    fn manifest(&self) -> Json {
        let agents: Vec<_> = self.declared.agents.iter().map(Agent::info).collect();
        json!({
            "workflow": self.declared.name,
            "intent": self.declared.intent,
            "on": self.declared.event.kind,
            "scans": self.declared.scan.is_some(),
            "runs_per_hour": self.declared.runs_per_hour,
            "may": self.declared.may,
            "agents": agents,
            "requires": self.requirements(),
        })
    }
}
