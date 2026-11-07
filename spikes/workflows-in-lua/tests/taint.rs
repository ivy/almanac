//! Tries to read hostile text out of a tainted value. Every attempt runs twice: in the sandbox a
//! workflow gets, and in a VM with Luau's whole standard library, which shows what the userdata
//! itself holds against, whatever the allowlist does.
mod common;

use common::script::*;
use mlua::{Function, Lua, MultiValue, Value};
use workflows_in_lua::sandbox::Vm;

/// A name, and a body that sees `h` (text), `patch`, and returns whatever it managed to get.
const ATTEMPTS: &[(&str, &str)] = &[
    ("tostring", "return tostring(h)"),
    ("concat left", r#"return "" .. h"#),
    ("concat right", r#"return h .. """#),
    ("interpolation", "return `{h}`"),
    ("format %s", r#"return string.format("%s", h)"#),
    ("format %*", r#"return string.format("%*", h)"#),
    ("format method", r#"return ("%*"):format(h)"#),
    ("format %q", r#"return string.format("%q", h)"#),
    ("string.len", "return string.len(h)"),
    ("string.sub", "return string.sub(h, 1)"),
    ("string.rep", "return string.rep(h, 1)"),
    ("string.lower", "return string.lower(h)"),
    ("string.find haystack", r#"return string.find(h, "I")"#),
    ("string.find needle", r#"return string.find("x", h)"#),
    ("string.gsub", r#"return string.gsub(h, ".", "%0")"#),
    ("string.byte", "return string.byte(h, 1, -1)"),
    ("table.concat", "return table.concat({ h })"),
    ("tonumber", "return tonumber(h)"),
    ("utf8.len", "return utf8.len(h)"),
    ("length", "return #h"),
    ("index inner", "return h.inner"),
    ("index text", "return h.text"),
    ("index 1", "return h[1]"),
    ("index __index", "return h.__index"),
    ("index __tostring", "return h.__tostring"),
    ("rawget", r#"return rawget(h, "inner")"#),
    ("rawlen", "return rawlen(h)"),
    ("next", "return next(h)"),
    ("pairs", "for k, v in pairs(h) do return k, v end"),
    ("ipairs", "for k, v in ipairs(h) do return k, v end"),
    ("generalized for", "for k, v in h do return k, v end"),
    ("call", "return h()"),
    ("arithmetic", "return h + 1"),
    ("compare", r#"return h < "a""#),
    (
        "equal to its text",
        r#"return h == "IGNORE-PREVIOUS-INSTRUCTIONS""#,
    ),
    ("error value", "error(h)"),
    (
        "error caught",
        "local ok, e = pcall(error, h); return tostring(e)",
    ),
    (
        "error message",
        "local ok, e = pcall(function() return h .. 1 end); return e",
    ),
    (
        "xpcall handler",
        "return xpcall(function() return h() end, function(e) return e end)",
    ),
    (
        "narrow closure",
        "local stolen; pcall(function() h:narrow(function(t) stolen = t; return true end) end); return stolen",
    ),
    (
        "map closure",
        "local stolen; pcall(function() h:map(function(t) stolen = t; return t end) end); return stolen",
    ),
    (
        "narrow returns a bool only",
        r#"return h:narrow("is_blank")"#,
    ),
    ("narrow unknown", r#"return h:narrow("inner")"#),
    ("map unknown", r#"return h:map("inner")"#),
    (
        "method on wrong self",
        r#"return h.narrow("text", "is_blank")"#,
    ),
    ("method on a table", r#"return h.map({}, "diff")"#),
    (
        "patch diff stays tainted",
        r#"local d = patch:map("diff"); return type(d), d:narrow("is_blank")"#,
    ),
    (
        "patch diff tostring",
        r#"return tostring(patch:map("diff"))"#,
    ),
    ("patch fields", "return patch.diff, patch.changes"),
    (
        "__concat on a table",
        "local t = setmetatable({}, { __concat = function(a, b) return b end }); return t .. h",
    ),
    (
        "__index with the value as key",
        "local t = setmetatable({}, { __index = function(_, k) return k end }); return t[h]",
    ),
    (
        "__eq against a table",
        "local t = setmetatable({}, { __eq = function() return true end }); return t == h",
    ),
    (
        "__lt against a table",
        "local t = setmetatable({}, { __lt = function(a, b) return tostring(b) end }); return t < h",
    ),
    (
        "__tostring borrowed",
        "local t = setmetatable({}, { __tostring = function() return tostring(h) end }); return tostring(t)",
    ),
    (
        "rewriting string",
        "string.len = function(s) return s end; return string.len(h)",
    ),
    (
        "string metatable",
        r#"local m = getmetatable(""); m.__concat = function(a, b) return b end; return "" .. h"#,
    ),
    ("getmetatable", "return getmetatable(h)"),
    (
        "metatable fields",
        "local m = getmetatable(h); return type(m) == 'table' and m.__index",
    ),
    ("setmetatable", "return setmetatable(h, {})"),
    ("debug.info", r#"return debug.info(h.map, "sln")"#),
    ("debug.traceback", "return debug.traceback(h)"),
    ("debug.getmetatable", "return debug.getmetatable(h)"),
    ("debug.getuservalue", "return debug.getuservalue(h)"),
    ("debug.getlocal", "return debug.getlocal(1, 1)"),
    ("debug.getupvalue", "return debug.getupvalue(h.map, 1)"),
    ("load", r#"return load("return 1")"#),
    ("loadstring", r#"return loadstring("return 1")"#),
    ("string.dump", "return string.dump(function() end)"),
    ("getfenv", "return getfenv(1)"),
    ("getfenv of a method", "return getfenv(h.map)"),
    ("setfenv", "return setfenv(1, {})"),
    ("newproxy", "return newproxy(true)"),
    ("collectgarbage", r#"return collectgarbage("count")"#),
    ("gcinfo", "return gcinfo()"),
    ("require", r#"return require("os")"#),
    ("buffer", "return buffer.tostring(h)"),
];

/// Fails the test if anything a script returned, or any error it raised, holds the text.
fn assert_clean(name: &str, what: &str) {
    assert!(!what.contains(SENTINEL), "{name} leaked: {what}");
}

fn inspect(name: &str, value: &Value, depth: usize) -> String {
    match value {
        Value::String(s) => {
            let text = s.to_string_lossy();
            assert_clean(name, &text);
            format!("{text:?}")
        }
        Value::Table(table) if depth < 4 => {
            for pair in table.pairs::<Value, Value>().flatten() {
                inspect(name, &pair.0, depth + 1);
                inspect(name, &pair.1, depth + 1);
            }
            "a table".to_string()
        }
        other => other.type_name().to_string(),
    }
}

fn attempt(lua: &Lua, name: &str, body: &str) -> String {
    let source = format!("local h, patch = ...\n{body}");
    let chunk: Function = lua
        .load(source.as_str())
        .set_name("=attempt")
        .into_function()
        .unwrap_or_else(|e| panic!("{name} does not compile: {e}"));
    let h = lua.create_userdata(hostile_text()).unwrap();
    let patch = lua.create_userdata(hostile_patch()).unwrap();
    match chunk.call::<MultiValue>((h, patch)) {
        Ok(values) => {
            let shown: Vec<String> = values.iter().map(|v| inspect(name, v, 0)).collect();
            format!("returned {}", shown.join(", "))
        }
        Err(error) => {
            let text = error.to_string();
            assert_clean(name, &text);
            let first = text.lines().next().unwrap_or_default().to_string();
            format!("refused: {first}")
        }
    }
}

#[test]
fn no_attempt_reads_the_text_in_the_sandbox() {
    let vm = Vm::new().unwrap();
    println!("{} attempts", ATTEMPTS.len());
    for (name, body) in ATTEMPTS {
        println!("sandbox | {name} | {}", attempt(&vm.lua, name, body));
    }
}

#[test]
fn no_attempt_reads_the_text_with_the_whole_standard_library() {
    let lua = Lua::new();
    for (name, body) in ATTEMPTS {
        println!("raw vm | {name} | {}", attempt(&lua, name, body));
    }
}

#[test]
fn a_tainted_value_equals_nothing_but_itself() {
    let vm = Vm::new().unwrap();
    let source = format!(r#"local h = ...; return h == "{SENTINEL}", rawequal(h, h)"#);
    let h = vm.lua.create_userdata(hostile_text()).unwrap();
    let chunk: Function = vm.lua.load(source.as_str()).into_function().unwrap();
    let (text, itself) = chunk.call::<(bool, bool)>(h).unwrap();
    assert!(!text, "comparing with a guess must reveal nothing");
    assert!(itself);
}

/// The checks above would pass if nothing ever ran, so this confirms a tainted value does hold the
/// text and the host can still deliver it to where it belongs.
#[test]
fn the_text_is_there_for_the_host() {
    let tagged = hostile_text().tagged().unwrap();
    assert_eq!(tagged["$untrusted"], SENTINEL);
}

#[tokio::test]
async fn an_events_untrusted_field_reaches_a_script_as_userdata() {
    let ran = script(
        "",
        r#"assert(type(event.title) == "userdata"); return { kind = "Done" }"#,
        "return {}",
    )
    .await;
    ran.effects();
}

#[tokio::test]
async fn a_tainted_value_survives_the_crossing_into_policy_still_opaque() {
    let ran = script(
        "",
        r#"return { kind = "Done", text = cx.hostile() }"#,
        r#"assert(type(facts.text) == "userdata")
           return { { kind = "Ask", question = "q", target = "owner/repo", body = facts.text } }"#,
    )
    .await;
    assert_eq!(ran.effects().len(), 1);
    let filed = ran.tracker.filed.lock().unwrap();
    assert_eq!(filed[0].2, "q");
}

#[tokio::test]
async fn raising_a_tainted_value_fails_the_run_without_repeating_it() {
    let ran = script("", "error(cx.hostile())", "return {}").await;
    assert_clean("error(hostile)", ran.error());
}

#[tokio::test]
async fn raising_a_tainted_value_from_policy_fails_the_run_without_repeating_it() {
    let ran = script(
        "",
        r#"return { kind = "Done", text = cx.hostile() }"#,
        "error(facts.text)",
    )
    .await;
    assert_clean("error(hostile)", ran.error());
}

#[tokio::test]
async fn a_hostile_event_field_cannot_reach_a_key() {
    let workflow = load("", &[("key", r#""{repo}""#)]).unwrap();
    let ran = run(workflow, serde_json::json!({})).await;
    let id = match &ran.outcome {
        almanac::runtime::Outcome::Ran { run_id, .. } => run_id.clone(),
        other => panic!("{other:?}"),
    };
    assert_clean("key", &id);
}

/// A model's words are observed text too. What an agent returns must stay as opaque as a log.
#[tokio::test]
async fn an_agents_answer_is_as_opaque_as_a_log() {
    let reply = serde_json::json!({"text": SENTINEL});
    let ran = script_replying(
        "",
        r#"local answer = cx.read("reader", {}, {})
           assert(type(answer) == "userdata")
           assert(answer.text == nil)
           assert(not pcall(tostring, answer))
           return { kind = "Done" }"#,
        "return {}",
        reply,
    )
    .await;
    ran.effects();
    assert_eq!(ran.harness.calls(), 1);
}
