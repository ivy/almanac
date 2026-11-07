//! Reads the table a workflow file returns and refuses what `#[workflow]` and `#[prompt]` refuse
//! at compile time. Nothing here runs `gather` or `decide`: everything it needs is in the table.
use crate::catalog::{Catalog, EventSpec, Field, ReadSpec, ScanSpec};
use almanac::agent::{AgentInfo, Fixer, Grant, Profile, Quarantined, Worker};
use almanac::prelude::*;
use mlua::{Table, Value};
use std::collections::BTreeSet;

const OPTIONS: &[&str] = &[
    "name",
    "on",
    "key",
    "intent",
    "may",
    "runs_per_hour",
    "subjects",
    "when",
    "scan",
    "reads",
    "agents",
    "literals",
    "gather",
    "decide",
];
const AGENT_OPTIONS: &[&str] = &["profile", "prompt", "vars"];
/// More than a workflow would need: a list longer than this was generated, not written.
const MAX_LITERALS: usize = 32;

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum VarKind {
    Slug,
    Sha,
    Number,
    Flag,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum AgentProfile {
    Quarantined,
    Fixer,
    Worker,
}

#[derive(Clone, Debug)]
pub enum Piece {
    Text(String),
    Var(String),
}

#[derive(Clone)]
pub struct Agent {
    pub name: &'static str,
    pub profile: AgentProfile,
    pub prompt: &'static str,
    pub pieces: Vec<Piece>,
    pub vars: Vec<(String, VarKind)>,
    pub prompt_vars: &'static [&'static str],
}

impl Agent {
    pub fn info(&self) -> AgentInfo {
        let (profile, grant): (&'static str, Grant) = match self.profile {
            AgentProfile::Quarantined => (Quarantined::NAME, Quarantined::GRANT),
            AgentProfile::Fixer => (Fixer::NAME, Fixer::GRANT),
            AgentProfile::Worker => (Worker::NAME, Worker::GRANT),
        };
        AgentInfo {
            name: self.name,
            profile,
            grant,
            prompt: self.prompt,
            prompt_vars: self.prompt_vars,
        }
    }
}

#[derive(Clone, Debug)]
pub enum SubjectSpec {
    Repo(String),
    Package(String, String),
}

/// Everything known about a workflow without running it.
#[derive(Clone)]
pub struct Declared {
    pub name: &'static str,
    pub intent: &'static str,
    pub event: EventSpec,
    pub key: Vec<Piece>,
    pub may: &'static [EffectKind],
    pub runs_per_hour: u32,
    pub subjects: Vec<SubjectSpec>,
    pub has_when: bool,
    pub scan: Option<ScanSpec>,
    pub reads: Vec<(&'static str, ReadSpec)>,
    pub agents: Vec<Agent>,
    pub literals: Vec<&'static str>,
    pub outcomes: BTreeSet<String>,
}

/// A workflow lives as long as the process, as a compiled one does, and `Registered` asks for
/// `'static` text. This is bounded by the number of files in the binary, not by events.
fn forever(text: String) -> &'static str {
    Box::leak(text.into_boxed_str())
}

fn text(table: &Table, field: &str) -> Result<Option<String>, String> {
    match table.raw_get::<Value>(field).map_err(|e| e.to_string())? {
        Value::Nil => Ok(None),
        Value::String(s) => s
            .to_str()
            .map(|s| Some(s.to_string()))
            .map_err(|_| format!("`{field}` is not UTF-8")),
        other => Err(format!(
            "`{field}` must be a string, not a {}",
            other.type_name()
        )),
    }
}

fn required(table: &Table, field: &str) -> Result<String, String> {
    text(table, field)?.ok_or_else(|| format!("a workflow needs `{field} = ...`"))
}

fn strings(table: &Table, field: &str) -> Result<Vec<String>, String> {
    match table.raw_get::<Value>(field).map_err(|e| e.to_string())? {
        Value::Nil => Ok(vec![]),
        Value::Table(list) => list
            .sequence_values::<String>()
            .collect::<Result<_, _>>()
            .map_err(|_| format!("`{field}` must be a list of strings")),
        other => Err(format!(
            "`{field}` must be a list, not a {}",
            other.type_name()
        )),
    }
}

fn only_known(table: &Table, allowed: &[&str], what: &str) -> Result<(), String> {
    for pair in table.pairs::<Value, Value>() {
        let (key, _) = pair.map_err(|e| e.to_string())?;
        let name = key.to_string().unwrap_or_default();
        if !allowed.contains(&name.as_str()) {
            return Err(format!("unknown {what} option `{name}`"));
        }
    }
    Ok(())
}

fn effect_kinds(names: Vec<String>) -> Result<&'static [EffectKind], String> {
    if names.is_empty() {
        return Err("a workflow needs `may = {...}`".to_string());
    }
    let kinds = names.iter().map(|name| {
        serde_json::from_value::<EffectKind>(name.as_str().into()).map_err(|_| {
            format!(
                "`may` names `{name}`, which is not an effect: effects are a closed set, \
                 Attest, Await, File, Ask, and Push"
            )
        })
    });
    Ok(kinds.collect::<Result<Vec<_>, _>>()?.leak())
}

fn field_of(event: &EventSpec, name: &str, on: &str) -> Result<Field, String> {
    let found = event.fields.iter().find(|(field, _)| *field == name);
    found
        .map(|(_, kind)| *kind)
        .ok_or_else(|| format!("{on} names `{name}`, which this event does not have"))
}

fn clean_field(event: &EventSpec, name: &str, on: &str) -> Result<(), String> {
    match field_of(event, name, on)? {
        Field::Tainted => Err(format!(
            "{on} names `{name}`, which is Untrusted: an attacker must not choose how runs are \
             grouped, so use a Clean field"
        )),
        Field::Clean | Field::Slug => Ok(()),
    }
}

/// `"{repo}:{branch}"`, split once so a run only joins strings.
fn key_pieces(event: &EventSpec, key: &str) -> Result<Vec<Piece>, String> {
    let (mut pieces, mut rest) = (vec![], key);
    while let Some(open) = rest.find('{') {
        pieces.push(Piece::Text(rest[..open].to_string()));
        let close = rest[open..]
            .find('}')
            .ok_or_else(|| "unclosed `{` in `key`".to_string())?
            + open;
        let name = &rest[open + 1..close];
        clean_field(event, name, "`key`")?;
        pieces.push(Piece::Var(name.to_string()));
        rest = &rest[close + 1..];
    }
    pieces.push(Piece::Text(rest.to_string()));
    Ok(pieces)
}

fn subjects(table: &Table, event: &EventSpec) -> Result<Vec<SubjectSpec>, String> {
    let list = match table
        .raw_get::<Value>("subjects")
        .map_err(|e| e.to_string())?
    {
        Value::Nil => return Ok(vec![]),
        Value::Table(list) => list,
        _ => return Err("`subjects` must be a list like `{ { repo = \"repo\" } }`".to_string()),
    };
    let slug = |name: &str| match field_of(event, name, "`subjects`")? {
        Field::Slug => Ok(name.to_string()),
        _ => Err(format!(
            "`subjects` names `{name}`, which is not a Slug: a subject outlives its run, so it \
             must not hold text an attacker chose"
        )),
    };
    let mut found = vec![];
    for entry in list.sequence_values::<Table>() {
        let entry = entry.map_err(|_| "each subject must be a table".to_string())?;
        if let Some(repo) = text(&entry, "repo")? {
            found.push(SubjectSpec::Repo(slug(&repo)?));
        } else if let Value::Table(pair) = entry.raw_get("package").map_err(|e| e.to_string())? {
            let repo: String = pair.raw_get(1).map_err(|e| e.to_string())?;
            let package: String = pair.raw_get(2).map_err(|e| e.to_string())?;
            found.push(SubjectSpec::Package(slug(&repo)?, slug(&package)?));
        } else {
            return Err("a subject is `{ repo = field }` or `{ package = { a, b } }`".to_string());
        }
    }
    Ok(found)
}

/// `{{ name }}` is a variable and `\{{` is a literal `{{`. Anything else in braces is an error, so
/// a typo in a template cannot quietly reach a model as text. The same grammar as `#[prompt]`.
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

fn var_kind(name: &str, kind: &str) -> Result<VarKind, String> {
    match kind {
        "Slug" => Ok(VarKind::Slug),
        "Sha" => Ok(VarKind::Sha),
        "number" => Ok(VarKind::Number),
        "boolean" => Ok(VarKind::Flag),
        other => Err(format!(
            "prompt variable `{name}` is declared `{other}`: a prompt's variables are instructions, \
             so they must be Clean: Slug, Sha, number, or boolean"
        )),
    }
}

fn agent(name: String, spec: &Table, catalog: &Catalog) -> Result<Agent, String> {
    only_known(spec, AGENT_OPTIONS, "agent")?;
    let profile = match required(spec, "profile")?.as_str() {
        "quarantined" => AgentProfile::Quarantined,
        "fixer" => AgentProfile::Fixer,
        "worker" => AgentProfile::Worker,
        other => {
            return Err(format!(
                "agent `{name}` has profile `{other}`: profiles are quarantined, fixer, and worker"
            ));
        }
    };
    let prompt = required(spec, "prompt")?;
    if prompt.contains("..") || prompt.starts_with('/') {
        return Err(format!("`{prompt}`: a prompt is a name in the catalog"));
    }
    let (source, template) = catalog
        .prompts
        .get_key_value(prompt.as_str())
        .ok_or_else(|| format!("no prompt named `{prompt}` is embedded in this binary"))?;
    let pieces = parse_template(template).map_err(|e| format!("prompt `{prompt}`: {e}"))?;

    let mut vars = vec![];
    if let Value::Table(declared) = spec.raw_get("vars").map_err(|e| e.to_string())? {
        for pair in declared.pairs::<String, String>() {
            let (var, kind) = pair.map_err(|_| "`vars` maps a name to a kind".to_string())?;
            let kind = var_kind(&var, &kind)?;
            vars.push((var, kind));
        }
    }
    vars.sort_by(|a, b| a.0.cmp(&b.0));
    let mut used: Vec<&'static str> = vec![];
    for piece in &pieces {
        let Piece::Var(var) = piece else { continue };
        if !vars.iter().any(|(declared, _)| declared == var) {
            return Err(format!(
                "prompt `{prompt}` uses `{{{{ {var} }}}}`, but agent `{name}` has no such var"
            ));
        }
        if !used.contains(&var.as_str()) {
            used.push(forever(var.clone()));
        }
    }
    for (var, _) in &vars {
        if !used.contains(&var.as_str()) {
            return Err(format!("prompt `{prompt}` never uses `{{{{ {var} }}}}`"));
        }
    }
    Ok(Agent {
        name: forever(name),
        profile,
        prompt: source,
        pieces,
        vars,
        prompt_vars: used.leak(),
    })
}

fn agents(table: &Table, catalog: &Catalog) -> Result<Vec<Agent>, String> {
    let mut found = vec![];
    if let Value::Table(agents) = table.raw_get("agents").map_err(|e| e.to_string())? {
        for pair in agents.pairs::<String, Table>() {
            let (name, spec) = pair.map_err(|_| "`agents` maps a name to a table".to_string())?;
            found.push(agent(name, &spec, catalog)?);
        }
    }
    found.sort_by_key(|agent| agent.name);
    Ok(found)
}

/// `decide` is a table of what can happen, so its keys are the closed set of outcomes.
fn outcomes(table: &Table) -> Result<BTreeSet<String>, String> {
    let Value::Table(decide) = table.raw_get("decide").map_err(|e| e.to_string())? else {
        return Err(
            "a workflow needs `decide = { Outcome = function(facts) ... end }`".to_string(),
        );
    };
    let mut outcomes = BTreeSet::new();
    for pair in decide.pairs::<String, Value>() {
        let (outcome, branch) = pair.map_err(|_| "`decide` is keyed by outcome".to_string())?;
        if !matches!(branch, Value::Function(_)) {
            return Err(format!("`decide.{outcome}` must be a function"));
        }
        outcomes.insert(outcome);
    }
    if outcomes.is_empty() {
        return Err("`decide` has no outcomes".to_string());
    }
    Ok(outcomes)
}

pub fn declared(table: &Table, catalog: &Catalog) -> Result<Declared, String> {
    only_known(table, OPTIONS, "workflow")?;
    let on = required(table, "on")?;
    let event = *catalog
        .events
        .get(on.as_str())
        .ok_or_else(|| format!("`on` names `{on}`, which is not an event in the catalog"))?;
    let key = key_pieces(&event, &required(table, "key")?)?;
    let runs_per_hour = match table.raw_get::<Value>("runs_per_hour") {
        Ok(Value::Integer(n)) if n > 0 => n as u32,
        Ok(Value::Number(n)) if n > 0.0 && n.fract() == 0.0 => n as u32,
        Ok(Value::Nil) => return Err("a workflow needs `runs_per_hour = ...`".to_string()),
        _ => return Err("`runs_per_hour` must be a whole number above zero".to_string()),
    };
    if !matches!(table.raw_get("gather"), Ok(Value::Function(_))) {
        return Err("a workflow needs `gather = function(cx, event) ... end`".to_string());
    }
    let has_when = match table.raw_get::<Value>("when").map_err(|e| e.to_string())? {
        Value::Nil => false,
        Value::Function(_) => true,
        _ => return Err("`when` must be a function of the event".to_string()),
    };
    let scan = match text(table, "scan")? {
        None => None,
        Some(name) => {
            let spec = *catalog
                .scans
                .get(name.as_str())
                .ok_or_else(|| format!("`scan` names `{name}`, which is not in the catalog"))?;
            if spec.event != on {
                return Err(format!(
                    "`scan` names `{name}`, which finds `{}` and not `{on}`",
                    spec.event
                ));
            }
            Some(spec)
        }
    };
    let mut reads = vec![];
    for name in strings(table, "reads")? {
        let (name, spec) = catalog
            .reads
            .get_key_value(name.as_str())
            .ok_or_else(|| format!("`reads` names `{name}`, which is not in the catalog"))?;
        reads.push((*name, *spec));
    }
    let literals = strings(table, "literals")?;
    if literals.len() > MAX_LITERALS {
        return Err(format!(
            "`literals` has {} entries, and the limit is {MAX_LITERALS}",
            literals.len()
        ));
    }
    Ok(Declared {
        name: forever(required(table, "name")?),
        intent: forever(required(table, "intent")?),
        event,
        key,
        may: effect_kinds(strings(table, "may")?)?,
        runs_per_hour,
        subjects: subjects(table, &event)?,
        has_when,
        scan,
        reads,
        agents: agents(table, catalog)?,
        literals: literals.into_iter().map(forever).collect(),
        outcomes: outcomes(table)?,
    })
}
