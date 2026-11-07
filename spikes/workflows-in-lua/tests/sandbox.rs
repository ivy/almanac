//! What a script cannot reach, and what keeps policy pure and replay deterministic.
mod common;

use almanac::prelude::*;
use almanac::runtime::Outcome;
use common::script::*;
use mlua::{Table, Value};
use serde_json::json;
use std::collections::BTreeSet;
use std::sync::atomic::Ordering;
use std::time::Instant;
use workflows_in_lua::sandbox::{INSTRUCTION_BUDGET, Vm};
use workflows_in_lua::value::Node;

fn evaluates(body: &str) -> mlua::Result<Value> {
    let vm = Vm::new().unwrap();
    vm.lua.load(body).eval::<Value>()
}

fn text_of(body: &str) -> String {
    let vm = Vm::new().unwrap();
    vm.lua.load(body).eval::<String>().unwrap()
}

/// These are called rather than compared with `nil`: under `safeenv` a builtin runs with its
/// global deleted, so a check for `nil` passes while the function still works.
#[test]
fn nothing_outside_the_allowlist_can_be_called() {
    let calls = [
        "getmetatable('')",
        "setmetatable({}, {})",
        "rawset({}, 'a', 1)",
        "getfenv()",
        "setfenv(1, {})",
        "loadstring('return 1')",
        "load('return 1')",
        "newproxy(true)",
        "collectgarbage()",
        "gcinfo()",
        "print('x')",
        "os.time()",
        "os.clock()",
        "os.date()",
        "io.open('x')",
        "math.random()",
        "math.randomseed(1)",
        "coroutine.yield()",
        "coroutine.wrap(function() end)",
        "debug.info(1, 's')",
        "debug.traceback()",
        "buffer.create(1)",
        "vector.create(1, 2, 3)",
        "integer.create(1)",
        "string.dump(function() end)",
        "require('os')",
        "_G.x",
    ];
    for call in calls {
        let body = format!("return {call}");
        assert!(evaluates(&body).is_err(), "{call} must not work");
    }
}

#[test]
fn the_environment_is_exactly_the_allowlist() {
    let vm = Vm::new().unwrap();
    let globals = vm.lua.globals();
    let base: Table = globals.metatable().unwrap().raw_get("__index").unwrap();
    let found: BTreeSet<String> = base
        .pairs::<String, Value>()
        .map(|pair| pair.unwrap().0)
        .collect();
    let expected = [
        "assert", "bit32", "error", "ipairs", "math", "next", "pairs", "pcall", "rawequal",
        "rawget", "rawlen", "require", "select", "string", "table", "tonumber", "tostring", "type",
        "typeof", "unpack", "utf8", "xpcall",
    ];
    let expected: BTreeSet<String> = expected.iter().map(ToString::to_string).collect();
    assert_eq!(found, expected);
}

#[test]
fn a_script_cannot_write_to_its_environment_or_the_libraries() {
    for write in [
        "x = 1",
        "string.rep = nil",
        "table.insert = nil",
        "math.pi = 3",
        "tostring = nil",
    ] {
        assert!(evaluates(write).is_err(), "{write} must not work");
    }
}

#[test]
fn nothing_a_script_can_print_holds_an_address() {
    for body in [
        "return tostring({})",
        "return tostring(function() end)",
        r#"return string.format("%*", {})"#,
        r#"return ("%*"):format(function() end)"#,
        "return `{ {} }`",
    ] {
        let shown = text_of(body);
        assert!(!shown.contains("0x"), "{body} printed {shown}");
    }
}

#[test]
fn a_runaway_loop_stops() {
    for body in [
        "while true do end",
        "repeat until false",
        "while true do pcall(function() while true do end end) end",
    ] {
        let vm = Vm::new().unwrap();
        let error = vm.lua.load(body).exec().unwrap_err().to_string();
        assert!(error.contains("instruction budget"), "{body}: {error}");
    }
}

#[test]
fn a_memory_bomb_stops() {
    let body = r#"local t = {}; while true do t[#t + 1] = string.rep("x", 1000000) .. #t end"#;
    assert!(evaluates(body).is_err());
}

#[test]
fn deep_recursion_stops() {
    assert!(evaluates("local function f() return 1 + f() end; return f()").is_err());
    assert!(evaluates("local function f() return f() end; return f()").is_err());
}

/// Luau raises an interrupt inside its pattern matcher too, so a pattern that backtracks is
/// stopped like a loop instead of holding the thread.
#[test]
fn a_pattern_that_backtracks_stops_at_the_budget() {
    let body = r#"
        local subject = string.rep("a", 40)
        local pattern = string.rep("a?", 40) .. string.rep("a", 40) .. "b"
        return string.find(subject, pattern)
    "#;
    let started = Instant::now();
    let error = evaluates(body).unwrap_err().to_string();
    assert!(error.contains("instruction budget"), "{error}");
    println!("stopped after {:?}", started.elapsed());
}

#[tokio::test]
async fn policy_cannot_use_a_cx_that_gather_stashed() {
    let ran = script(
        "local stashed",
        r#"stashed = cx; return { kind = "Done" }"#,
        "return stashed.attempts()",
    )
    .await;
    assert!(
        ran.error().contains("attempt to index nil"),
        "{}",
        ran.error()
    );
}

#[tokio::test]
async fn policy_cannot_see_what_gather_left_in_the_file() {
    let ran = script(
        "local count = 0",
        r#"count = count + 1; return { kind = "Done" }"#,
        r#"return { { kind = "Ask", question = "count is " .. count, target = "owner/repo" } }"#,
    )
    .await;
    let filed = ran.tracker.filed.lock().unwrap();
    assert_eq!(filed[0].2, "count is 0", "decide starts from a fresh file");
}

#[tokio::test]
async fn a_cx_function_cannot_cross_as_a_fact() {
    let ran = script(
        "",
        r#"return { kind = "Done", f = cx.attempts }"#,
        "return {}",
    )
    .await;
    assert!(ran.error().contains("only data"), "{}", ran.error());
}

#[tokio::test]
async fn a_proof_cannot_cross_as_a_fact() {
    let ran = script(
        "",
        r#"return { kind = "Done", p = cx.proof() }"#,
        "return {}",
    )
    .await;
    assert!(ran.error().contains("cannot be a fact"), "{}", ran.error());
}

#[tokio::test]
async fn a_table_that_holds_itself_cannot_cross() {
    let ran = script(
        "",
        r#"local t = { kind = "Done" }; t.self = t; return t"#,
        "return {}",
    )
    .await;
    assert!(ran.error().contains("nested too deeply"), "{}", ran.error());
}

#[tokio::test]
async fn a_scripts_pcall_cannot_swallow_an_outage() {
    let ran = script(
        "",
        r#"pcall(cx.broken); return { kind = "Done" }"#,
        r#"return { { kind = "Await", awaiting = "wait" } }"#,
    )
    .await;
    assert_eq!(ran.error(), "the forge is down");
}

#[tokio::test]
async fn one_bad_effect_stops_every_effect_in_the_run() {
    let ran = script(
        "",
        r#"return { kind = "Done" }"#,
        r#"return {
            { kind = "Ask", question = "q", target = "owner/repo" },
            { kind = "Merge" },
        }"#,
    )
    .await;
    assert!(ran.error().contains("not an effect"), "{}", ran.error());
    assert_eq!(ran.tracker.filed.lock().unwrap().len(), 0);
}

/// `may` needs nothing new: the runtime applies it to whatever a workflow produces.
#[tokio::test]
async fn an_effect_the_workflow_did_not_declare_is_denied_by_the_runtime() {
    let halves = gather_then(
        r#"return { kind = "Done" }"#,
        r#"return { { kind = "Ask", question = "q", target = "owner/repo" } }"#,
    );
    let mut changes: Vec<(&str, &str)> = halves.iter().map(|(k, v)| (*k, v.as_str())).collect();
    changes.push(("may", r#"{ "Await" }"#));
    let ran = run(load("", &changes).unwrap(), json!({})).await;
    let Outcome::Ran {
        effects, denied, ..
    } = &ran.outcome
    else {
        panic!("{:?}", ran.outcome);
    };
    assert!(effects.is_empty());
    assert_eq!(denied[0].kind, EffectKind::Ask);
    assert_eq!(ran.tracker.filed.lock().unwrap().len(), 0);
}

#[tokio::test]
async fn a_cx_outside_gather_does_not_exist() {
    let top = "local stolen = cx";
    let ran = script(
        top,
        r#"assert(stolen == nil); return { kind = "Done" }"#,
        "return {}",
    )
    .await;
    ran.effects();
}

#[test]
fn when_that_fails_stops_the_event_rather_than_starting_a_run() {
    let workflow = load("", &[("when", r#"function(event) error("broken") end"#)]).unwrap();
    let payload = json!({"repo": "owner/repo", "title": "x"});
    assert!(workflow.identify(&payload).is_err());
}

#[test]
fn when_sees_a_hostile_field_only_as_userdata() {
    let when = r#"function(event) return type(event.title) == "userdata" end"#;
    let workflow = load("", &[("when", when)]).unwrap();
    let payload = json!({"repo": "owner/repo", "title": SENTINEL});
    assert!(workflow.identify(&payload).unwrap().wanted);
}

/// The same facts in a fresh VM each time, with everything a policy could use to vary.
#[test]
fn policy_gives_the_same_effects_in_every_fresh_vm() {
    let decide = r#"{ Done = function(facts)
        local seen = {}
        for name in pairs({ alpha = 1, beta = 2, gamma = 3, delta = 4, epsilon = 5, zeta = 6 }) do
            table.insert(seen, name)
        end
        local question = table.concat(seen, ",") .. tostring({}) .. `{ {} }`
        return { { kind = "Ask", question = question, target = "owner/repo" } }
    end }"#;
    let workflow = load("", &[("decide", decide)]).unwrap();
    let facts = Node::map([("kind", Node::text("Done"))]);
    let asked = |workflow: &workflows_in_lua::workflow::LuaWorkflow| {
        let effects = workflow.decide(&facts).unwrap();
        match &effects[0] {
            Effect::Ask { question, .. } => question.to_string(),
            other => panic!("{other:?}"),
        }
    };
    let first = asked(&workflow);
    for _ in 0..50 {
        assert_eq!(asked(&workflow), first);
    }
}

const OBJECT_KEYS: &str = r#"
    local keyed = {}
    for i = 1, 16 do keyed[{}] = i end
    local order = {}
    for _, i in keyed do table.insert(order, tostring(i)) end
    return table.concat(order, ",")
"#;

/// Run by the test below, once per process.
#[test]
#[ignore = "a helper, started as a child process"]
fn print_object_key_order() {
    println!("order={}", text_of(OBJECT_KEYS));
}

/// A gap, kept visible: a table keyed by tables iterates in address order, which changes from one
/// process to the next. A replay is a later process, so a policy doing this would decide
/// differently. Nothing can refuse it without losing tables, because the VM's own `for` reaches
/// the order directly, and Luau's analyzer does not flag it either.
#[test]
fn iterating_a_table_keyed_by_tables_is_not_replay_safe() {
    let binary = std::env::current_exe().unwrap();
    let across_processes: BTreeSet<String> = (0..12)
        .map(|_| {
            let output = std::process::Command::new(&binary)
                .args([
                    "--ignored",
                    "--exact",
                    "print_object_key_order",
                    "--nocapture",
                ])
                .output()
                .unwrap();
            let text = String::from_utf8_lossy(&output.stdout).to_string();
            let line = text
                .lines()
                .find(|l| l.contains("order="))
                .unwrap_or_default();
            line.to_string()
        })
        .collect();
    println!("{} distinct orders in 12 processes", across_processes.len());
    assert!(
        across_processes.len() > 1,
        "if this fails, the gap has closed"
    );
}

/// A run builds two VMs and an event builds one, so this is the price of keeping them apart.
#[test]
fn print_what_a_fresh_vm_costs() {
    let source = include_str!("../workflows/build_failures.luau");
    let started = Instant::now();
    for _ in 0..200 {
        let vm = Vm::new().unwrap();
        vm.evaluate(source, "build_failures.luau").unwrap();
    }
    println!("one VM, file loaded: {:?}", started.elapsed() / 200);
}

#[test]
fn a_replay_spends_the_same_budget() {
    let body = "local n = 0; for i = 1, 1000 do n = n + i end; return n";
    let spent = || {
        let vm = Vm::new().unwrap();
        vm.lua.load(body).exec().unwrap();
        vm.used.load(Ordering::Relaxed)
    };
    assert_eq!(spent(), spent());
    assert!(spent() < INSTRUCTION_BUDGET);
}
