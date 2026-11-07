//! Checks what `analyze.fnl` read against what the host allows, and builds the declaration a
//! workflow runs under. This is the check that cannot be skipped: it reads the source's tree, so a
//! script that avoids the `workflow` macro still passes through here.
use crate::compile::{Analysis, LoadError};
use crate::host::{EventDef, Field, Host, Need};
use crate::value::Script;
use almanac::agent::{AgentInfo, Fixer, Profile, Quarantined, Worker};
use almanac::effect::EffectKind;
use almanac::providers::Requirement;
use serde_json::{Value, json};
use std::collections::BTreeMap;

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Kind {
    Quarantined,
    Worker,
    Fixer,
}

#[derive(Clone, Debug)]
pub enum Piece {
    Text(String),
    Var(String),
}

/// An agent a workflow declared.
#[derive(Clone, Debug)]
pub struct AgentDecl {
    pub name: &'static str,
    pub profile: Kind,
    pub template: Vec<Piece>,
    pub vars: Vec<String>,
    pub info: AgentInfo,
}

pub enum SubjectDecl {
    Repo(String),
    Package(String, String),
}

/// Everything a workflow declared, checked.
pub struct Declaration {
    pub name: &'static str,
    pub intent: &'static str,
    pub event: EventDef,
    pub key: Vec<Piece>,
    pub may: &'static [EffectKind],
    pub runs_per_hour: u32,
    pub subjects: Vec<SubjectDecl>,
    pub has_when: bool,
    pub agents: BTreeMap<String, AgentDecl>,
    pub needs: Vec<Need>,
    pub reads: Vec<String>,
    pub narrowers: BTreeMap<String, String>,
}

fn leak(text: &str) -> &'static str {
    Box::leak(text.to_string().into_boxed_str())
}

fn text<'a>(declared: &'a Script, key: &str) -> Option<&'a str> {
    match declared.get(key) {
        Some(Script::Text(text)) => Some(text),
        _ => None,
    }
}

fn strings(value: Option<&Script>) -> Vec<String> {
    let Some(Script::List(items)) = value else {
        return vec![];
    };
    let texts = items.iter().filter_map(|item| match item {
        Script::Text(text) => Some(text.clone()),
        _ => None,
    });
    texts.collect()
}

/// The same grammar as `#[prompt]`: `{{ name }}` is a variable, `\{{` is a literal, and anything
/// else in braces is an error, so a typo cannot quietly reach a model as text.
pub fn parse_template(text: &str) -> Result<Vec<Piece>, String> {
    let line = |at: usize| text[..at].matches('\n').count() + 1;
    let (mut pieces, mut literal, mut at) = (Vec::new(), String::new(), 0);
    while let Some(found) = text[at..].find("{{") {
        let open = at + found;
        if text[..open].ends_with('\\') {
            literal.push_str(&text[at..open - 1]);
            literal.push_str("{{");
            at = open + 2;
            continue;
        }
        literal.push_str(&text[at..open]);
        let close = text[open..]
            .find("}}")
            .ok_or(format!("line {}: unclosed `{{{{`", line(open)))?
            + open;
        let name = text[open + 2..close].trim();
        let valid = name
            .chars()
            .next()
            .is_some_and(|c| c.is_ascii_alphabetic() || c == '_')
            && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_');
        if !valid {
            return Err(format!(
                "line {}: `{{{{ {name} }}}}` is not a variable name",
                line(open)
            ));
        }
        pieces.push(Piece::Text(std::mem::take(&mut literal)));
        pieces.push(Piece::Var(name.to_string()));
        at = close + 2;
    }
    literal.push_str(&text[at..]);
    pieces.push(Piece::Text(literal));
    Ok(pieces)
}

/// `"{repo}:{branch}"` as pieces, with every field checked against the event.
fn parse_key(key: &str, event: &EventDef) -> Result<Vec<Piece>, LoadError> {
    let (mut pieces, mut rest) = (Vec::new(), key);
    while let Some(open) = rest.find('{') {
        pieces.push(Piece::Text(rest[..open].to_string()));
        let close = rest[open..].find('}').ok_or_else(|| {
            LoadError::new(
                "load/key",
                "unclosed `{` in key",
                "a key decides which events coalesce",
                "close the brace",
            )
        })? + open;
        let field = &rest[open + 1..close];
        clean_field(event, field, "key")?;
        pieces.push(Piece::Var(field.to_string()));
        rest = &rest[close + 1..];
    }
    pieces.push(Piece::Text(rest.to_string()));
    Ok(pieces)
}

fn clean_field(event: &EventDef, field: &str, used_in: &str) -> Result<(), LoadError> {
    let found = event.fields.iter().find(|(name, _)| *name == field);
    match found {
        None => Err(LoadError::new(
            "load/unknown-field",
            format!("{} has no field `{field}`, used in a {used_in}", event.name),
            "a name that matches nothing would read as empty",
            "use one of the event's fields",
        )),
        Some((_, Field::Tainted)) => Err(LoadError::new(
            "load/tainted-field",
            format!(
                "`{field}` of {} is tainted, and a {used_in} holds it",
                event.name
            ),
            "text an attacker wrote must not decide how work is grouped",
            "use a validated field",
        )),
        Some((_, Field::Clean)) => Ok(()),
    }
}

fn effect_kind(name: &str) -> Result<EffectKind, LoadError> {
    match name {
        "Attest" => Ok(EffectKind::Attest),
        "Await" => Ok(EffectKind::Await),
        "File" => Ok(EffectKind::File),
        "Ask" => Ok(EffectKind::Ask),
        "Push" => Ok(EffectKind::Push),
        other => Err(LoadError::new(
            "load/unknown-effect",
            format!("`{other}` is not an effect"),
            "effects are a closed set: Attest, Await, File, Ask, and Push",
            "name one of those. Merging is the repository's job",
        )),
    }
}

fn agent_info(name: &'static str, kind: Kind, prompt: &'static str, vars: &[String]) -> AgentInfo {
    let (profile, grant) = match kind {
        Kind::Quarantined => (Quarantined::NAME, Quarantined::GRANT),
        Kind::Worker => (Worker::NAME, Worker::GRANT),
        Kind::Fixer => (Fixer::NAME, Fixer::GRANT),
    };
    let names: Vec<&'static str> = vars.iter().map(|v| leak(v)).collect();
    AgentInfo {
        name,
        profile,
        grant,
        prompt,
        prompt_vars: Box::leak(names.into_boxed_slice()),
    }
}

fn agent(host: &Host, name: &str, declared: &Script) -> Result<AgentDecl, LoadError> {
    let profile = match text(declared, "profile") {
        Some("quarantined") => Kind::Quarantined,
        Some("worker") => Kind::Worker,
        Some("fixer") => Kind::Fixer,
        other => {
            return Err(LoadError::new(
                "load/unknown-profile",
                format!("agent `{name}` has profile {other:?}"),
                "a profile fixes what an agent can do, so the set is closed",
                "use quarantined, worker, or fixer",
            ));
        }
    };
    let prompt = text(declared, "prompt").unwrap_or_default();
    if prompt.contains("..") || prompt.starts_with('/') {
        return Err(LoadError::new(
            "load/prompt-path",
            format!("`{prompt}` is outside `prompts/`"),
            "prompts are built into the binary",
            "name a file in `prompts/`",
        ));
    }
    let found = host.prompts.iter().find(|(file, _)| *file == prompt);
    let Some((file, source)) = found else {
        return Err(LoadError::new(
            "load/prompt-missing",
            format!("no prompt named `{prompt}` is built in"),
            "an agent with no prompt has no instructions",
            "add the file to the host's prompts",
        ));
    };
    let template = parse_template(source).map_err(|problem| {
        LoadError::new(
            "load/prompt-template",
            format!("prompts/{prompt}: {problem}"),
            "a malformed variable would reach a model as text",
            "fix the template",
        )
    })?;
    let vars = strings(declared.get("vars"));
    let used: Vec<&String> = template
        .iter()
        .filter_map(|piece| match piece {
            Piece::Var(var) => Some(var),
            Piece::Text(_) => None,
        })
        .collect();
    if let Some(var) = used.iter().find(|var| !vars.contains(var)) {
        return Err(LoadError::new(
            "load/prompt-var",
            format!("prompts/{prompt} uses `{{{{ {var} }}}}`, which `{name}` does not declare"),
            "the variable would have no value",
            "add it to `:vars`, or fix the template",
        ));
    }
    if let Some(var) = vars.iter().find(|var| !used.contains(var)) {
        return Err(LoadError::new(
            "load/prompt-var",
            format!("prompts/{prompt} never uses `{{{{ {var} }}}}`"),
            "a declared variable that nothing uses is a mistake somewhere",
            "remove it from `:vars`, or use it in the template",
        ));
    }
    let name = leak(name);
    let info = agent_info(name, profile, file, &vars);
    Ok(AgentDecl {
        name,
        profile,
        template,
        vars,
        info,
    })
}

fn agents(host: &Host, analysis: &Analysis) -> Result<BTreeMap<String, AgentDecl>, LoadError> {
    let mut all = BTreeMap::new();
    if let Script::Map(declared) = &analysis.agents {
        for (name, value) in declared {
            all.insert(name.clone(), agent(host, name, value)?);
        }
    }
    Ok(all)
}

/// Checks each agent call the reader saw, so a wrong verb fails here and not mid-run.
fn used_agents(
    declared: &Script,
    all: BTreeMap<String, AgentDecl>,
) -> Result<BTreeMap<String, AgentDecl>, LoadError> {
    let mut used = BTreeMap::new();
    let Some(Script::List(calls)) = declared.get("used") else {
        return Ok(used);
    };
    for call in calls {
        let name = text(call, "agent").unwrap_or_default();
        let verb = text(call, "verb").unwrap_or_default();
        let Some(agent) = all.get(name) else {
            return Err(LoadError::new(
                "load/unknown-agent",
                format!("agent `{name}` is not declared"),
                "an agent's capabilities come from its declaration",
                "declare it with `(agent ...)`",
            ));
        };
        let allowed = match agent.profile {
            Kind::Quarantined => "read",
            Kind::Worker => "act",
            Kind::Fixer => "repair",
        };
        if verb != allowed {
            return Err(LoadError::new(
                "load/wrong-verb",
                format!(
                    "agent `{name}` is {:?} and can only `{allowed}`",
                    agent.profile
                ),
                "an agent's profile decides what it may do with what it reads",
                "use the verb its profile allows",
            ));
        }
        if agent.profile == Kind::Fixer
            && call
                .get("cooled")
                .is_none_or(|c| !matches!(c, Script::Bool(true)))
        {
            return Err(LoadError::new(
                "load/fixer-needs-cooled",
                format!("`cx.repair :{name}` is not given `:cooled`"),
                "no agent reads an update until it has cooled",
                "pass the proof from `cx.cooled`",
            ));
        }
        if let Some(keys) = call.get("prompt-keys").map(|keys| strings(Some(keys))) {
            let missing = agent.vars.iter().find(|var| !keys.contains(var));
            let extra = keys.iter().find(|key| !agent.vars.contains(key));
            if let Some(var) = missing.or(extra) {
                return Err(LoadError::new(
                    "load/prompt-field",
                    format!("`{name}` is called with the wrong prompt variables, at `{var}`"),
                    "a prompt must be filled in exactly",
                    "pass every variable it declares and no others",
                ));
            }
        }
        used.insert(name.to_string(), agent.clone());
    }
    Ok(used)
}

fn needs(host: &Host, declared: &Script) -> Result<Vec<Need>, LoadError> {
    let mut found = vec![];
    for name in strings(declared.get("needs")) {
        let need = host.needs.iter().find(|need| need.name == name);
        let Some(need) = need else {
            return Err(LoadError::new(
                "load/unknown-need",
                format!("no provider named `{name}`"),
                "a workflow can reach only what the host offers",
                "use a provider the host declares",
            ));
        };
        found.push(need.clone());
    }
    Ok(found)
}

fn subjects(declared: &Script, event: &EventDef) -> Result<Vec<SubjectDecl>, LoadError> {
    let mut found = vec![];
    let Some(Script::List(calls)) = declared.get("subjects") else {
        return Ok(found);
    };
    for call in calls {
        let parts = strings(Some(call));
        let names: Vec<&str> = parts.iter().map(String::as_str).collect();
        for field in names.iter().skip(1) {
            clean_field(event, field, "subject")?;
        }
        found.push(match names.as_slice() {
            ["repo", repo] => SubjectDecl::Repo(repo.to_string()),
            ["package", repo, package] => {
                SubjectDecl::Package(repo.to_string(), package.to_string())
            }
            other => {
                return Err(LoadError::new(
                    "load/unknown-subject",
                    format!("`{other:?}` is not a subject"),
                    "subjects are a closed set",
                    "use `(repo field)` or `(package repo-field package-field)`",
                ));
            }
        });
    }
    Ok(found)
}

pub fn declare(host: &Host, analysis: &Analysis) -> Result<Declaration, LoadError> {
    let Some(declared) = &analysis.workflow else {
        return Err(LoadError::new(
            "load/no-workflow",
            "this file declares no workflow",
            "the host reads the declaration to build the manifest",
            "add a `(workflow ...)` form",
        ));
    };
    let on = text(declared, "on").unwrap_or_default();
    let event = host.events.iter().find(|e| e.name == on);
    let Some(event) = event else {
        return Err(LoadError::new(
            "load/unknown-event",
            format!("no event named `{on}`"),
            "a workflow can trigger only on events the host parses",
            "use an event the host declares",
        ));
    };
    let key = parse_key(text(declared, "key").unwrap_or_default(), event)?;
    let intent = text(declared, "intent").unwrap_or_default();
    if intent.is_empty() {
        return Err(LoadError::new(
            "load/intent",
            "the intent is empty",
            "a guard checks actions against it",
            "say what the workflow is for",
        ));
    }
    let may: Result<Vec<EffectKind>, LoadError> = strings(declared.get("may"))
        .iter()
        .map(|name| effect_kind(name))
        .collect();
    let may = may?;
    if may.is_empty() {
        return Err(LoadError::new(
            "load/may",
            "`:may` is empty",
            "a workflow that may do nothing has no effect",
            "list the effects it may produce",
        ));
    }
    let runs_per_hour = match declared.get("runs-per-hour") {
        Some(Script::Int(n)) if *n > 0 => u32::try_from(*n).ok(),
        _ => None,
    };
    let Some(runs_per_hour) = runs_per_hour else {
        return Err(LoadError::new(
            "load/budget",
            "`:runs-per-hour` is not a positive integer",
            "the budget bounds what a burst of events can spend",
            "give it a whole number",
        ));
    };
    let mut narrowers = BTreeMap::new();
    if let Script::Map(found) = &analysis.narrowers {
        for (name, lua) in found {
            if let Script::Text(lua) = lua {
                narrowers.insert(name.clone(), lua.clone());
            }
        }
    }
    Ok(Declaration {
        name: leak(text(declared, "name").unwrap_or_default()),
        intent: leak(intent),
        event: event.clone(),
        key,
        may: Box::leak(may.into_boxed_slice()),
        runs_per_hour,
        subjects: subjects(declared, event)?,
        has_when: text(declared, "when").is_some(),
        agents: used_agents(declared, agents(host, analysis)?)?,
        needs: needs(host, declared)?,
        reads: strings(declared.get("reads")),
        narrowers,
    })
}

impl Declaration {
    pub fn requirements(&self) -> Vec<Requirement> {
        self.needs.iter().map(|need| need.requirement).collect()
    }

    /// The same document `Registered::manifest` produces for a Rust workflow, derived without
    /// running anything. `scans` is false because this spike does not port `scan`.
    pub fn manifest(&self) -> Value {
        let agents: Vec<&AgentInfo> = self.agents.values().map(|a| &a.info).collect();
        json!({
            "workflow": self.name,
            "intent": self.intent,
            "on": self.event.kind,
            "scans": false,
            "runs_per_hour": self.runs_per_hour,
            "may": self.may,
            "agents": agents,
            "requires": self.requirements(),
        })
    }
}
