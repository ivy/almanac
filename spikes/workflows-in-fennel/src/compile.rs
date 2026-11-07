//! Turns Fennel source into Lua text and a declaration. This is the only VM that holds the
//! compiler, and it has no `io`, `os`, or `debug`, so a macro cannot reach outside it either.
use crate::sandbox::{HOST_GLOBALS, PURE_GLOBALS, limit_instructions};
use crate::value::Script;
use mlua::chunk::ChunkMode;
use mlua::{Function, Lua, LuaOptions, StdLib, Table};
use std::fmt;

const FENNEL: &str = include_str!("../vendor/fennel-1.6.1.lua");
const MACROS: &str = include_str!("../fennel/macros.fnl");
const ANALYZE: &str = include_str!("../fennel/analyze.fnl");
const BOOTSTRAP: &str = r#"
local fennel, macros, analyze, globals = ...
package.loaded.fennel = fennel
package.path, package.cpath = "", ""
fennel["macro-loaded"].almanac = fennel.eval(macros, {env = "_COMPILER", filename = "macros.fnl"})
local analyzer = fennel.eval(analyze, {env = _G, filename = "analyze.fnl"})
return {
  analyze = function(source, filename, pure)
    return analyzer.analyze(source, filename, pure)
  end,
  compile = function(source, filename)
    return fennel.compileString(source, {
      allowedGlobals = globals,
      ["error-pinpoint"] = false,
      filename = filename,
      useMetadata = false,
    })
  end,
}
"#;

/// Why a workflow did not load: what happened, why it matters, and what to do next.
#[derive(Clone, Debug, PartialEq)]
pub struct LoadError {
    pub code: String,
    pub line: u32,
    pub what: String,
    pub why: String,
    pub next: String,
}

impl LoadError {
    pub fn new(code: &str, what: impl ToString, why: &str, next: &str) -> Self {
        Self {
            code: code.to_string(),
            line: 0,
            what: what.to_string(),
            why: why.to_string(),
            next: next.to_string(),
        }
    }
}

impl fmt::Display for LoadError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let Self {
            code,
            line,
            what,
            why,
            next,
        } = self;
        write!(f, "{code} at line {line}: {what}. {why}. {next}.")
    }
}

impl std::error::Error for LoadError {}

/// What `analyze.fnl` read, as data.
pub struct Analysis {
    /// `None` if the source declares no workflow.
    pub workflow: Option<Script>,
    pub agents: Script,
    pub narrowers: Script,
}

pub struct Compiled {
    pub lua: String,
    pub analysis: Analysis,
}

fn failed(code: &str, error: impl fmt::Display) -> LoadError {
    LoadError::new(
        code,
        error,
        "a workflow that does not compile cannot run",
        "fix the source and load it again",
    )
}

fn script_of(table: &Table, key: &str) -> Result<Script, LoadError> {
    let value = table
        .get(key)
        .map_err(|e| failed("load/analysis", e.to_string()))?;
    Script::from_lua(&value).map_err(|e| failed("load/analysis", e))
}

fn load_error(item: &Script) -> LoadError {
    let text = |key: &str| match item.get(key) {
        Some(Script::Text(text)) => text.clone(),
        _ => String::new(),
    };
    let line = match item.get("line") {
        Some(Script::Int(line)) => *line as u32,
        _ => 0,
    };
    LoadError {
        code: text("code"),
        line,
        what: text("what"),
        why: text("why"),
        next: text("next"),
    }
}

struct Compiler {
    analyze: Function,
    compile: Function,
    pure: Table,
    _lua: Lua,
}

impl Compiler {
    fn new() -> mlua::Result<Self> {
        let libs = StdLib::TABLE | StdLib::STRING | StdLib::UTF8 | StdLib::MATH | StdLib::PACKAGE;
        let lua = Lua::new_with(libs, LuaOptions::new())?;
        limit_instructions(&lua)?;
        let fennel: Table = lua
            .load(FENNEL)
            .set_name("=fennel")
            .set_mode(ChunkMode::Text)
            .eval()?;
        let names = |lists: &[&[&'static str]]| -> mlua::Result<Table> {
            lua.create_sequence_from(lists.iter().flat_map(|l| l.iter().copied()))
        };
        let globals = names(&[PURE_GLOBALS, HOST_GLOBALS])?;
        let pure = names(&[PURE_GLOBALS])?;
        let api: Table = lua
            .load(BOOTSTRAP)
            .set_name("=bootstrap")
            .call((fennel, MACROS, ANALYZE, globals))?;
        Ok(Self {
            analyze: api.get("analyze")?,
            compile: api.get("compile")?,
            pure,
            _lua: lua,
        })
    }
}

/// Reads and compiles a workflow, running none of it.
pub fn compile(filename: &str, source: &str) -> Result<Compiled, LoadError> {
    let compiler = Compiler::new().map_err(|e| failed("load/compiler", e))?;
    let read: Table = compiler
        .analyze
        .call((source, filename, compiler.pure.clone()))
        .map_err(|e| failed("load/parse", e))?;
    let errors = script_of(&read, "errors")?;
    if let Script::List(errors) = &errors
        && let Some(first) = errors.first()
    {
        return Err(load_error(first));
    }
    let lua: String = compiler
        .compile
        .call((source, filename))
        .map_err(|e| failed("load/compile", e))?;
    Ok(Compiled {
        lua,
        analysis: Analysis {
            workflow: match script_of(&read, "workflow")? {
                Script::Nil => None,
                declared => Some(declared),
            },
            agents: script_of(&read, "agents")?,
            narrowers: script_of(&read, "narrowers")?,
        },
    })
}
