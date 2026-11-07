//! Everything that runs with no way to reach the world: `when`, policy, and narrowers. A pure VM
//! has no `cx`, no provider, and no clock, and it is new for every call, so what it returns can
//! depend only on what it was given.
use crate::sandbox::Vm;
use crate::value::{self, CheckedPatch, Planned, Script, Tainted, intern, raw_into_lua, reveal};
use crate::workflow::FennelWorkflow;
use almanac::prelude::*;
use mlua::{Error, Function, Lua, MultiValue, Table, Value as LuaValue, Variadic};

fn refused(what: impl std::fmt::Display) -> Error {
    Error::runtime(what.to_string())
}

fn tainted(value: &LuaValue, what: &str) -> mlua::Result<Tainted> {
    let LuaValue::UserData(data) = value else {
        return Err(refused(format!("{what} must be tainted content")));
    };
    let held = data.borrow::<Tainted>();
    let held = held.map_err(|_| refused(format!("{what} must be tainted content")))?;
    Ok(held.clone())
}

fn text(options: &Table, key: &str) -> mlua::Result<String> {
    match options.get(key)? {
        LuaValue::String(text) => Ok(text.to_string_lossy()),
        _ => Err(refused(format!("`:{key}` must be a string"))),
    }
}

fn parsed<T: std::str::FromStr>(options: &Table, key: &str) -> mlua::Result<T> {
    let value = text(options, key)?;
    value.parse().map_err(|_| refused(format!("`:{key}` is not valid")))
}

fn body(options: &Table) -> mlua::Result<Untrusted<String>> {
    match tainted(&options.get("body")?, "`:body`")? {
        Tainted::Text(body) => Ok(body),
        _ => Err(refused("`:body` must be tainted text")),
    }
}

fn state(options: &Table) -> mlua::Result<AttestState> {
    match text(options, "state")?.as_str() {
        "pending" => Ok(AttestState::Pending),
        "success" => Ok(AttestState::Success),
        "failure" => Ok(AttestState::Failure),
        _ => Err(refused("`:state` must be pending, success, or failure")),
    }
}

fn checked(options: &Table) -> mlua::Result<Checked> {
    let reason = "`:patch` must be what `patches.check` returned";
    let LuaValue::UserData(data) = options.get("patch")? else {
        return Err(refused(reason));
    };
    let held = data.borrow::<CheckedPatch>().map_err(|_| refused(reason))?;
    Ok(held.0.clone())
}

fn hold(options: &Table) -> mlua::Result<Option<&'static str>> {
    match options.get("hold")? {
        LuaValue::Nil => Ok(None),
        LuaValue::String(text) => Ok(Some(intern(&text.to_string_lossy()))),
        _ => Err(refused("`:hold` must be a string")),
    }
}

/// The five constructors. Every way to touch the world in a script is one of these, and each
/// holds its effect in a userdata, so a table cannot pose as one.
pub fn effect_table(lua: &Lua) -> mlua::Result<Table> {
    let table = lua.create_table()?;
    let attest = lua.create_function(|_, options: Table| {
        Ok(Planned(Effect::Attest {
            context: intern(&text(&options, "context")?),
            state: state(&options)?,
            repo: parsed(&options, "repo")?,
            sha: parsed(&options, "sha")?,
        }))
    })?;
    let wait = lua.create_function(|_, until: String| {
        Ok(Planned(Effect::Await {
            until: intern(&until),
        }))
    })?;
    let ask = lua.create_function(|_, options: Table| {
        Ok(Planned(Effect::Ask {
            question: Title::__compose(text(&options, "question")?),
            target: parsed(&options, "target")?,
            body: body(&options)?,
        }))
    })?;
    let file = lua.create_function(|_, options: Table| {
        Ok(Planned(Effect::File {
            title: Title::__compose(text(&options, "title")?),
            target: parsed(&options, "target")?,
            body: body(&options)?,
        }))
    })?;
    let push = lua.create_function(|_, options: Table| {
        let pull: i64 = options.get("pull")?;
        Ok(Planned(Effect::Push {
            repo: parsed(&options, "repo")?,
            pull: u64::try_from(pull).map_err(|_| refused("`:pull` is not a pull request"))?,
            branch: parsed(&options, "branch")?,
            message: intern(&text(&options, "message")?),
            hold: hold(&options)?,
            patch: checked(&options)?,
        }))
    })?;
    table.set("attest", attest)?;
    table.set("await", wait)?;
    table.set("ask", ask)?;
    table.set("file", file)?;
    table.set("push", push)?;
    Ok(table)
}

pub fn patches_table(lua: &Lua) -> mlua::Result<Table> {
    let table = lua.create_table()?;
    let check = lua.create_function(|lua, (patch, head): (LuaValue, String)| {
        let Tainted::Patch(patch) = tainted(&patch, "a patch")? else {
            return Err(refused("`patches.check` takes a patch"));
        };
        let head: Sha = head.parse().map_err(|_| refused("`head` is not a sha"))?;
        match DEFAULT_POLICY.check(&patch, &head) {
            Ok(checked) => {
                let held = lua.create_userdata(CheckedPatch(checked))?;
                Ok(MultiValue::from_vec(vec![LuaValue::UserData(held)]))
            }
            Err(rejection) => {
                let reason = lua.create_string(format!("{rejection:?}"))?;
                let reason = LuaValue::String(reason);
                Ok(MultiValue::from_vec(vec![LuaValue::Nil, reason]))
            }
        }
    })?;
    table.set("check", check)?;
    Ok(table)
}

fn text_of(value: &LuaValue) -> mlua::Result<String> {
    match value {
        LuaValue::String(text) => Ok(text.to_string_lossy()),
        other => match tainted(other, "a piece of text")? {
            Tainted::Text(text) => reveal(&text).map_err(|StepError(e)| refused(e)),
            _ => Err(refused("only text can be joined")),
        },
    }
}

/// Runs a narrower in a VM of its own, on the raw text. It holds nothing else and returns only a
/// bool, so like a `fn` passed to `Untrusted::narrow`, it has nowhere to put the text. Its errors
/// are replaced, because a script can write the text into one.
fn narrow(workflow_narrowers: &[(String, String)], content: &Tainted, name: &str) -> mlua::Result<bool> {
    let found = workflow_narrowers.iter().find(|(key, _)| key == name);
    let Some((_, code)) = found else {
        return Err(refused(format!("no narrower named `{name}`")));
    };
    let hidden = || refused(format!("narrower `{name}` failed"));
    let vm = Vm::new().map_err(|_| hidden())?;
    let function: Function = vm.load(name, code).map_err(|_| hidden())?;
    let raw = content.raw().map_err(|_| hidden())?;
    let raw = raw_into_lua(&vm.lua, &raw).map_err(|_| hidden())?;
    match function.call::<LuaValue>(raw).map_err(|_| hidden())? {
        LuaValue::Boolean(answer) => Ok(answer),
        _ => Err(refused(format!(
            "narrower `{name}` must return a bool: anything wider could carry the text out"
        ))),
    }
}

/// Everything a script can do to tainted content. Each result is tainted, except `narrow`.
pub fn taint_table(lua: &Lua, narrowers: Vec<(String, String)>) -> mlua::Result<Table> {
    let table = lua.create_table()?;
    let wrap = lua.create_function(|_, text: String| Ok(Tainted::Text(Untrusted::new(text))))?;
    let map = lua.create_function(|_, (content, mapper): (LuaValue, String)| {
        value::map(&tainted(&content, "`taint.map`'s first argument")?, &mapper).map_err(refused)
    })?;
    let concat = lua.create_function(|_, pieces: Variadic<LuaValue>| {
        let mut joined = String::new();
        for piece in pieces.iter() {
            joined.push_str(&text_of(piece)?);
        }
        Ok(Tainted::Text(Untrusted::new(joined)))
    })?;
    let tail = lua.create_function(|_, (content, limit): (LuaValue, usize)| {
        let full = text_of(&content)?;
        let start = full.len().saturating_sub(limit);
        let start = (start..full.len())
            .find(|at| full.is_char_boundary(*at))
            .unwrap_or(0);
        Ok(Tainted::Text(Untrusted::new(full[start..].to_string())))
    })?;
    let narrower = lua.create_function(move |_, (content, name): (LuaValue, String)| {
        narrow(&narrowers, &tainted(&content, "`taint.narrow`'s first argument")?, &name)
    })?;
    table.set("wrap", wrap)?;
    table.set("map", map)?;
    table.set("concat", concat)?;
    table.set("tail", tail)?;
    table.set("narrow", narrower)?;
    Ok(table)
}

pub fn install_taint(vm: &Vm, workflow: &FennelWorkflow) -> mlua::Result<()> {
    let narrowers = workflow.declaration().narrowers.clone().into_iter().collect();
    vm.env.set("taint", taint_table(&vm.lua, narrowers)?)
}

/// An event as a script sees it. A field it does not have is an error, not a `nil`.
pub fn event_table(vm: &Vm, workflow: &FennelWorkflow, event: &Script) -> mlua::Result<LuaValue> {
    let table = event.into_lua(&vm.lua)?;
    vm.strict.call((table, workflow.declaration().event.name))
}

fn pure_vm(workflow: &FennelWorkflow) -> mlua::Result<(Vm, Table)> {
    let vm = Vm::new()?;
    install_taint(&vm, workflow)?;
    vm.env.set("effect", effect_table(&vm.lua)?)?;
    vm.env.set("patches", patches_table(&vm.lua)?)?;
    let chunk: Table = vm.load(workflow.name(), workflow.lua())?;
    Ok((vm, chunk))
}

/// Whether an event deserves a run.
pub fn wanted(workflow: &FennelWorkflow, event: &Script) -> mlua::Result<bool> {
    let (vm, chunk) = pure_vm(workflow)?;
    let when: Option<Function> = chunk.get("when")?;
    let Some(when) = when else {
        return Ok(true);
    };
    let event = event_table(&vm, workflow, event)?;
    match when.call::<LuaValue>(event)? {
        LuaValue::Boolean(answer) => Ok(answer),
        LuaValue::Nil => Ok(false),
        _ => Err(refused("`:when` must return a bool")),
    }
}

/// Runs policy on `facts`. Nothing returns from here but effects, and only these constructors
/// make one. A `nil` means no pattern matched, which in Rust the compiler would have refused.
pub fn decide(workflow: &FennelWorkflow, facts: &Script) -> mlua::Result<Vec<Effect>> {
    let (vm, chunk) = pure_vm(workflow)?;
    let policy: Function = chunk.get("decide")?;
    let facts = facts.into_lua(&vm.lua)?;
    let LuaValue::Table(planned) = policy.call::<LuaValue>(facts)? else {
        return Err(refused(
            "policy returned no list of effects: no pattern matched these facts",
        ));
    };
    let mut effects = vec![];
    for item in planned.sequence_values::<LuaValue>() {
        let LuaValue::UserData(data) = item? else {
            return Err(refused("policy returned something that is not an effect"));
        };
        let effect = data.borrow::<Planned>();
        let effect = effect.map_err(|_| refused("policy returned something that is not an effect"))?;
        effects.push(effect.0.clone());
    }
    Ok(effects)
}
