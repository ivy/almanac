//! Builds the VM a script runs in. Each one is new and is dropped after use, so nothing a script
//! does can reach the next run, and `gather` and policy never share a VM.
use mlua::chunk::ChunkMode;
use mlua::debug::HookTriggers;
use mlua::{Function, Lua, LuaOptions, StdLib, Table, VmState};
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};

/// Globals every script may name. Compilation refuses anything else.
pub const PURE_GLOBALS: &[&str] = &[
    "_G", "assert", "error", "ipairs", "math", "pairs", "select", "string", "table", "tonumber",
    "tostring", "type", "utf8",
];
/// What the host adds. A gather VM has no `effect` or `patches`, and a pure VM has no `cx`.
pub const HOST_GLOBALS: &[&str] = &["effect", "patches", "taint"];

const PRELUDE: &str = include_str!("../fennel/prelude.lua");
const MEMORY_LIMIT: usize = 64 * 1024 * 1024;
/// Instructions a VM may run, counted in `HOOK_EVERY` steps.
const INSTRUCTION_LIMIT: u64 = 200_000_000;
const HOOK_EVERY: u32 = 10_000;

/// A VM, with the tables the prelude built.
pub struct Vm {
    pub lua: Lua,
    pub env: Table,
    pub cx: Table,
    pub providers: Table,
    pub strict: Function,
}

impl Vm {
    pub fn new() -> mlua::Result<Self> {
        let libs = StdLib::COROUTINE | StdLib::TABLE | StdLib::STRING | StdLib::UTF8 | StdLib::MATH;
        let lua = Lua::new_with(libs, LuaOptions::new())?;
        lua.set_memory_limit(MEMORY_LIMIT)?;
        limit_instructions(&lua)?;
        let prelude: Table = lua
            .load(PRELUDE)
            .set_name("=prelude")
            .set_mode(ChunkMode::Text)
            .eval()?;
        Ok(Self {
            env: prelude.get("env")?,
            cx: prelude.get("cx")?,
            providers: prelude.get("providers")?,
            strict: prelude.get("strict")?,
            lua,
        })
    }

    /// Runs compiled source with `env` as its only globals. Text only: a binary chunk can corrupt
    /// the VM.
    pub fn load<R: mlua::FromLuaMulti>(&self, name: &str, code: &str) -> mlua::Result<R> {
        self.lua
            .load(code)
            .set_name(format!("={name}"))
            .set_mode(ChunkMode::Text)
            .set_environment(self.env.clone())
            .eval()
    }
}

/// Applies to the main state and to every thread mlua creates after it, so a loop in a coroutine
/// stops too. Both an endless loop and an endless allocation end the run.
pub fn limit_instructions(lua: &Lua) -> mlua::Result<()> {
    let spent = Arc::new(AtomicU64::new(0));
    let triggers = HookTriggers::new().every_nth_instruction(HOOK_EVERY);
    lua.set_global_hook(triggers, move |_, _| {
        let total = spent.fetch_add(HOOK_EVERY as u64, Ordering::Relaxed);
        if total > INSTRUCTION_LIMIT {
            return Err(mlua::Error::runtime(
                "this script ran too long and was stopped: look for a loop that does not end",
            ));
        }
        Ok(VmState::Continue)
    })
}
