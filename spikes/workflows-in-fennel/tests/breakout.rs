//! Tries to read `Tainted` content from inside a script. Attacks are raw Lua, not Fennel, because
//! Lua is everything a Fennel script could compile to, and raw Lua skips every load-time check. So
//! what is proved here rests on the VM alone.
mod common;
use almanac::prelude::*;
use common::*;
use mlua::{Lua, Value as LuaValue};
use workflows_in_fennel::pure::{effect_table, patches_table, taint_table};
use workflows_in_fennel::sandbox::{HOST_GLOBALS, PURE_GLOBALS, Vm};
use workflows_in_fennel::value::Tainted;

fn sandbox() -> Vm {
    let vm = Vm::new().unwrap();
    let narrowers = vec![
        (
            "yes".to_string(),
            "return function(t) return true end".to_string(),
        ),
        (
            "echo".to_string(),
            "return function(t) return t end".to_string(),
        ),
        (
            "shout".to_string(),
            "return function(t) error(t) end".to_string(),
        ),
        (
            "keep".to_string(),
            "return function(t) _G.kept = t return true end".to_string(),
        ),
    ];
    let lua = &vm.lua;
    vm.env
        .set("taint", taint_table(lua, narrowers).unwrap())
        .unwrap();
    vm.env.set("effect", effect_table(lua).unwrap()).unwrap();
    vm.env.set("patches", patches_table(lua).unwrap()).unwrap();
    let hostile = Tainted::Text(Untrusted::new(HOSTILE.to_string()));
    vm.env.set("u", hostile).unwrap();
    vm
}

fn show(value: &LuaValue) -> String {
    match value {
        LuaValue::String(text) => format!("string {:?}", text.to_string_lossy()),
        LuaValue::UserData(_) => "userdata".to_string(),
        other => format!("{other:?}"),
    }
}

/// What an attack got: a value, or the error Lua raised. Addresses are masked so that this reads
/// the same each run.
fn attempt(vm: &Vm, attack: &str) -> String {
    match vm.load::<LuaValue>("attack", attack) {
        Ok(value) => format!("-> {}", show(&value)),
        Err(error) => {
            let text = error.to_string();
            let first = text.lines().next().unwrap_or_default().to_string();
            format!("!! {first}")
        }
    }
}

/// Every string-shaped way out of a value, in the order someone would try them.
const COERCIONS: &[&str] = &[
    "return tostring(u)",
    "return _G.tostring(u)",
    "return '' .. u",
    "return u .. ''",
    "return string.format('%s', u)",
    "return string.format('%q', u)",
    "return string.format('%p', u)",
    "return ('%s'):format(u)",
    "return ('%d'):format(u)",
    "return string.len(u)",
    "return string.sub(u, 1)",
    "return string.upper(u)",
    "return string.rep(u, 1)",
    "return string.byte(u, 1, -1)",
    "return string.gsub(u, '.', '')",
    "return string.find(u, 'I')",
    "return string.match(u, '.*')",
    "return string.pack('z', u)",
    "return table.concat({u})",
    "return table.unpack(u)",
    "return tonumber(u)",
    "return math.floor(u)",
    "return utf8.len(u)",
    "return utf8.codepoint(u, 1)",
    "return #u",
];

/// Metamethods: everything Lua does to a value that a type could customise.
const METAMETHODS: &[&str] = &[
    "return u.inner",
    "return u[1]",
    "return u.value",
    "return u:inner()",
    "return u:reveal()",
    "u.inner = 1",
    "return u()",
    "return u + 1",
    "return -u",
    "return u < 'a'",
    "return u <= 'a'",
    "return u == 'IGNORE-PREVIOUS-INSTRUCTIONS'",
    "for k, v in pairs(u) do return k end",
    "for k, v in ipairs(u) do return k end",
];

/// What is missing from the sandbox, reached both as a global and through `_G`.
const UNREACHABLE: &[&str] = &[
    "return debug",
    "return _G.debug",
    "return load",
    "return _G.load",
    "return loadstring",
    "return dofile",
    "return loadfile",
    "return require",
    "return package",
    "return io",
    "return os",
    "return coroutine",
    "return collectgarbage",
    "return getmetatable",
    "return setmetatable",
    "return rawget",
    "return rawset",
    "return rawequal",
    "return rawlen",
    "return next",
    "return pcall",
    "return xpcall",
    "return print",
    "return warn",
    "return string.dump",
    "return math.random",
    "return math.randomseed",
    "return getmetatable and getmetatable('')",
    "return ('').dump",
    "return ('').__index",
];

/// The host's own functions, used against it.
const HOST_API: &[&str] = &[
    "return taint.map(u, function(t) return t end)",
    "return taint.map(u, 'nope')",
    "return taint.narrow(u, function(t) stolen = t return true end)",
    "return taint.narrow(u, 'missing')",
    "return taint.narrow(u, 'echo')",
    "return taint.narrow(u, 'shout')",
    "return taint.narrow(u, 'keep') and _G.kept",
    "return taint.wrap(u)",
    "return effect.await(u)",
    "return effect.ask({question = u, target = 'a/b', body = u})",
    "return effect.merge",
    "return patches.check(u, ('0'):rep(40))",
    "return taint.tail(u, 3) == taint.tail(u, 3)",
    "return taint.concat(u, 'x') == 'x'",
];

/// Passing a value out through an error instead of a return.
const ERRORS: &[&str] = &[
    "error(u)",
    "error(u, 2)",
    "assert(false, u)",
    "assert(u == nil, u)",
    "error({u})",
    "error(setmetatable)",
];

#[test]
fn no_attack_reads_tainted_content() {
    let groups = [
        ("coercions", COERCIONS),
        ("metamethods", METAMETHODS),
        ("unreachable", UNREACHABLE),
        ("host api", HOST_API),
        ("errors", ERRORS),
    ];
    for (group, attacks) in groups {
        println!("-- {group}");
        for attack in attacks {
            let vm = sandbox();
            let result = attempt(&vm, attack);
            println!("{attack:<62} {result}");
            assert!(!result.contains(HOSTILE), "{attack}: {result}");
        }
    }
}

#[test]
fn what_a_sandbox_lacks_is_really_absent() {
    for attack in UNREACHABLE {
        let vm = sandbox();
        let result = attempt(&vm, attack);
        assert_eq!(result, "-> Nil", "{attack}");
    }
}

#[test]
fn a_script_sees_exactly_the_globals_it_was_promised() {
    let vm = sandbox();
    let names: mlua::Table = vm
        .load(
            "globals",
            "local names = {} for key in pairs(_G) do names[#names + 1] = key end return names",
        )
        .unwrap();
    let mut names: Vec<String> = names.sequence_values().map(Result::unwrap).collect();
    names.retain(|name| name != "u");
    let mut expected: Vec<String> = PURE_GLOBALS
        .iter()
        .chain(HOST_GLOBALS)
        .map(|s| s.to_string())
        .collect();
    expected.sort();
    assert_eq!(names, expected);
}

#[test]
fn g_is_the_sandbox_and_not_the_real_globals() {
    let vm = sandbox();
    assert_eq!(attempt(&vm, "return _G == _ENV"), "-> Boolean(true)");
    assert_eq!(attempt(&vm, "return _ENV.coroutine"), "-> Nil");
}

#[test]
fn a_binary_chunk_is_refused() {
    let vm = sandbox();
    let error = vm.load::<LuaValue>("binary", "\x1bLua").unwrap_err();
    assert!(error.to_string().contains("binary chunk"), "{error}");
}

/// If the allowlist were lost, the content still could not be read: it is in Rust's heap, and the
/// userdata exposes no function that reaches it. This gives a script `debug` and the raw globals.
#[test]
#[allow(unsafe_code)]
fn with_the_sandbox_gone_a_userdata_still_gives_nothing_up() {
    // SAFETY: the attacks are this file's own, and the VM is dropped at the end of the test.
    let lua = unsafe { Lua::unsafe_new() };
    let hostile = Tainted::Text(Untrusted::new(HOSTILE.to_string()));
    lua.globals().set("u", hostile).unwrap();
    let probe = r#"
        local seen = {}
        local function note(value) seen[#seen + 1] = tostring(value) end
        note(u)
        note(debug.getuservalue(u, 1))
        local meta = debug.getmetatable(u)
        note(getmetatable(u))
        for key, value in pairs(meta) do
            note(key)
            if type(value) == "function" then
                note(select(2, pcall(value, u)))
                local at = 1
                while true do
                    local name, up = debug.getupvalue(value, at)
                    if not name then break end
                    note(name) note(up)
                    at = at + 1
                end
            end
        end
        for key, value in pairs(debug.getregistry()) do
            note(key) note(value)
        end
        return table.concat(seen, "\n")
    "#;
    let seen: String = lua.load(probe).eval().unwrap();
    println!("{seen}");
    assert!(!seen.contains(HOSTILE), "{seen}");
}

/// The instruction limit counts VM instructions, and a pattern match is one C call, so a
/// backtracking pattern runs past it. This is why a narrower has no patterns. Prints timings.
#[test]
#[ignore = "prints timings"]
fn a_backtracking_pattern_is_not_stopped_by_the_instruction_limit() {
    for size in [250, 500] {
        let vm = Vm::new().unwrap();
        let code = format!("return string.find(string.rep('a', {size}), '.-.-.-x')");
        let started = std::time::Instant::now();
        let found: LuaValue = vm.load("slow", &code).unwrap();
        println!("{size} bytes: {:?} ({found:?})", started.elapsed());
    }
}

#[tokio::test]
async fn an_error_carrying_tainted_content_never_shows_it() {
    let ran = run(Probe {
        gather: "(error event.title)",
        ..Probe::default()
    })
    .await;
    assert!(!ran.visible().contains(HOSTILE), "{}", ran.visible());
}

#[tokio::test]
async fn a_loop_that_never_ends_is_stopped_in_gather() {
    let ran = run(Probe {
        gather: "(while true nil)",
        ..Probe::default()
    })
    .await;
    assert!(ran.error().contains("ran too long"), "{}", ran.error());
}

#[tokio::test]
async fn a_loop_that_never_ends_is_stopped_in_policy() {
    let ran = run(Probe {
        policy: "(while true nil)",
        ..Probe::default()
    })
    .await;
    assert!(ran.error().contains("ran too long"), "{}", ran.error());
}

#[tokio::test]
async fn memory_is_bounded() {
    let ran = run(Probe {
        gather: r#"{:big (string.rep "x" (* 1024 1024 1024))}"#,
        ..Probe::default()
    })
    .await;
    assert!(
        ran.error().to_lowercase().contains("memory"),
        "{}",
        ran.error()
    );
}

#[tokio::test]
async fn a_narrower_that_never_ends_is_stopped_and_says_nothing() {
    let ran = run(Probe {
        top: "(narrower spin [t] (while true nil))",
        gather: "{:x (taint.narrow event.title :spin)}",
        ..Probe::default()
    })
    .await;
    assert!(
        ran.error().contains("narrower `spin` failed"),
        "{}",
        ran.error()
    );
}

#[tokio::test]
async fn a_narrower_has_no_patterns_to_backtrack_on_hostile_text() {
    let ran = run(Probe {
        top: r#"(narrower slow [t] (= nil (t:match ".-.-.-x")))"#,
        gather: "{:x (taint.narrow event.title :slow)}",
        ..Probe::default()
    })
    .await;
    assert!(
        ran.error().contains("narrower `slow` failed"),
        "{}",
        ran.error()
    );
}

#[tokio::test]
async fn a_narrower_can_still_search_for_literal_text() {
    let ran = run(Probe {
        top: r#"(narrower mentions [t] (not= nil (string.find t "PREVIOUS")))
                (narrower dotted [t] (not= nil (string.find t ".-")))"#,
        gather: "{:found (taint.narrow event.title :mentions)
                  :pattern (taint.narrow event.title :dotted)}",
        policy: "[(effect.await (.. (tostring facts.found) \" \" (tostring facts.pattern)))]",
        ..Probe::default()
    })
    .await;
    assert!(ran.visible().contains("true false"), "{}", ran.visible());
}

#[tokio::test]
async fn a_read_outside_observe_is_refused() {
    let ran = run(Probe {
        gather: "(let [github (cx.get :github)] (github.pull :a/b 1))",
        ..Probe::default()
    })
    .await;
    assert!(
        ran.error().contains("would not be recorded"),
        "{}",
        ran.error()
    );
}

#[tokio::test]
async fn a_step_inside_a_read_is_refused() {
    let ran = run(Probe {
        gather: "(cx.observe :x #(cx.attempts))",
        ..Probe::default()
    })
    .await;
    assert!(ran.error().contains("skipped on replay"), "{}", ran.error());
}
