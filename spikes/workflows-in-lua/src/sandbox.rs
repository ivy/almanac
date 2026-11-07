//! The VM a script runs in. Each one starts from an allowlist, so anything a future Luau adds is
//! absent until someone decides otherwise, and each one lives for a single purpose: loading a
//! file, answering `when`, gathering one run, or deciding one run.
use crate::taint::{CheckedPatch, Tainted};
use crate::value::{Node, install_strict};
use almanac::prelude::*;
use mlua::{Function, Lua, LuaOptions, MultiValue, StdLib, Table, Value, VmState};
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};

/// Interrupts a script may take. Luau raises one per loop iteration and function call, so this
/// counts work rather than time and a replay stops at the same place.
pub const INSTRUCTION_BUDGET: u64 = 2_000_000;
pub const MEMORY_LIMIT: usize = 32 * 1024 * 1024;

/// The whole base library a script sees. Nothing here reads a clock, a file, or randomness, reaches
/// another function's environment, or compiles code.
const GLOBALS: &[&str] = &[
    "assert", "bit32", "error", "ipairs", "math", "next", "pairs", "pcall", "rawequal", "rawget",
    "rawlen", "require", "select", "string", "table", "tonumber", "tostring", "type", "typeof",
    "unpack", "utf8", "xpcall",
];
const NOT_IN_MATH: &[&str] = &["random", "randomseed"];

/// Runs before the allowlist is applied, so it can keep what scripts lose. `yield` stays reachable
/// only through the closures this returns.
const PRELUDE: &str = r#"
local yield, native_tostring, native_format = coroutine.yield, tostring, string.format
local type, pack, unpack = type, table.pack, table.unpack

-- An address differs between runs, so it must not reach anything a replay compares.
local function addressless(value)
    local kind = type(value)
    if kind == "table" or kind == "function" or kind == "thread" then
        return kind
    end
    return value
end

function tostring(value)
    local plain = addressless(value)
    return if type(plain) == "string" then plain else native_tostring(plain)
end

function string.format(format, ...)
    local values = pack(...)
    for i = 1, values.n do
        values[i] = addressless(values[i])
    end
    return native_format(format, unpack(values, 1, values.n))
end

return function(name)
    return function(...)
        return yield(name, ...)
    end
end
"#;

pub struct Vm {
    pub lua: Lua,
    /// Makes a `cx` operation: a function that hands a request to the driver.
    operation: Function,
    pub used: Arc<AtomicU64>,
}

fn prune(table: &Table, keep: impl Fn(&str) -> bool) -> mlua::Result<()> {
    let names: Vec<String> = table
        .pairs::<String, Value>()
        .filter_map(Result::ok)
        .map(|(name, _)| name)
        .collect();
    for name in names.iter().filter(|name| !keep(name)) {
        table.raw_remove(name.as_str())?;
    }
    Ok(())
}

/// The host side of `require("./almanac")`.
fn almanac_module(lua: &Lua) -> mlua::Result<Table> {
    let module = lua.create_table()?;
    let check = lua.create_function(|lua, (patch, base): (Value, Value)| {
        let refuse = |what: &str| mlua::Error::runtime(format!("check_patch: {what}"));
        let Node::Tainted(Tainted::Patch(patch)) =
            Node::from_lua(&patch).map_err(|e| refuse(&e))?
        else {
            return Err(refuse(
                "the first argument must be the patch an agent returned",
            ));
        };
        let base: Sha = Node::from_lua(&base)
            .map_err(|e| refuse(&e))?
            .as_str()
            .ok_or_else(|| refuse("the second argument must be the commit it applies to"))?
            .parse()
            .map_err(|_| refuse("the second argument is not a full commit hash"))?;
        let mut answer = MultiValue::new();
        match DEFAULT_POLICY.check(&patch, &base) {
            Ok(checked) => {
                answer.push_back(Value::UserData(lua.create_userdata(CheckedPatch(checked))?))
            }
            Err(rejection) => {
                answer.push_back(Value::Nil);
                let reason = lua.create_string(format!("{rejection:?}"))?;
                answer.push_back(Value::String(reason));
            }
        }
        Ok(answer)
    })?;
    module.raw_set("check_patch", check)?;
    module.set_readonly(true);
    Ok(module)
}

impl Vm {
    pub fn new() -> mlua::Result<Self> {
        let libs = StdLib::COROUTINE | StdLib::TABLE | StdLib::STRING | StdLib::UTF8 | StdLib::BIT;
        let lua = Lua::new_with(libs | StdLib::MATH, LuaOptions::new())?;
        install_strict(&lua)?;
        let operation: Function = lua.load(PRELUDE).set_name("=almanac").eval()?;

        let globals = lua.globals();
        let module = almanac_module(&lua)?;
        let require = lua.create_function(move |_, path: String| match path.as_str() {
            "./almanac" => Ok(module.clone()),
            other => Err(mlua::Error::runtime(format!(
                "cannot require `{other}`: a workflow is one file, and only ./almanac is provided"
            ))),
        })?;
        globals.raw_set("require", require)?;
        prune(&globals, |name| GLOBALS.contains(&name))?;
        prune(&globals.get::<Table>("math")?, |name| {
            !NOT_IN_MATH.contains(&name)
        })?;
        lua.sandbox(true)?;
        let environment = lua.globals();
        environment.set_readonly(true);
        // `sandbox` turns `safeenv` on, which lets a call to a builtin skip the global lookup. That
        // would run `setmetatable` with the global deleted, and skip every wrapper in the prelude,
        // so the allowlist only means anything with it off.
        environment.set_safeenv(false);

        lua.set_memory_limit(MEMORY_LIMIT)?;
        let used = Arc::new(AtomicU64::new(0));
        let counter = used.clone();
        lua.set_interrupt(move |_| {
            if counter.fetch_add(1, Ordering::Relaxed) >= INSTRUCTION_BUDGET {
                return Err(mlua::Error::runtime(
                    "this script ran past its instruction budget: a workflow decides, it does not \
                     compute, so move the work into a host function",
                ));
            }
            Ok(VmState::Continue)
        });
        Ok(Self {
            lua,
            operation,
            used,
        })
    }

    /// Runs a workflow file and returns what it declared.
    pub fn evaluate(&self, source: &str, name: &str) -> mlua::Result<Table> {
        let chunk = self.lua.load(source).set_name(format!("={name}"));
        chunk.eval()
    }

    /// A `cx` table offering exactly `operations`.
    pub fn cx(&self, operations: &[&str]) -> mlua::Result<Value> {
        let table = self.lua.create_table()?;
        for name in operations {
            let operation: Function = self.operation.call(*name)?;
            table.raw_set(*name, operation)?;
        }
        let strict = self.lua.named_registry_value::<Table>("almanac.strict")?;
        table.set_metatable(Some(strict))?;
        table.set_readonly(true);
        Ok(Value::Table(table))
    }
}
