//! Policy stays pure and a replay decides the same, because no VM outlives a call and nothing in
//! one depends on when, where, or how often it ran.
mod common;
use almanac::prelude::*;
use common::*;
use mlua::{Lua, Table, Value as LuaValue};
use std::collections::BTreeSet;
use std::time::Instant;
use workflows_in_fennel::sandbox::Vm;
use workflows_in_fennel::value::Script;
use workflows_in_fennel::{build_failures, pure};

fn awaits(effects: &[Effect]) -> Vec<String> {
    let until = effects.iter().map(|effect| match effect {
        Effect::Await { until } => until.to_string(),
        other => panic!("{other:?}"),
    });
    until.collect()
}

const MANY_KEYS: &str = "local t = {} for i = 1, 40 do t['key' .. i] = i end ";

/// Why `pairs` is replaced: the same table, built the same way, in a new VM.
#[test]
fn raw_next_can_iterate_differently_in_each_vm_and_sandboxed_pairs_cannot() {
    let mut raw = BTreeSet::new();
    let mut sandboxed = BTreeSet::new();
    for _ in 0..50 {
        let lua = Lua::new();
        let code = format!(
            "{MANY_KEYS} local out = {{}} for k in next, t do out[#out + 1] = k end \
             return table.concat(out, ',')"
        );
        raw.insert(lua.load(&code).eval::<String>().unwrap());

        let vm = Vm::new().unwrap();
        let code = format!(
            "{MANY_KEYS} local out = {{}} for k in pairs(t) do out[#out + 1] = k end \
             return table.concat(out, ',')"
        );
        sandboxed.insert(vm.load::<String>("order", &code).unwrap());
    }
    println!(
        "orders in 50 VMs: raw next {}, sandboxed pairs {}",
        raw.len(),
        sandboxed.len()
    );
    assert_eq!(sandboxed.len(), 1);
}

#[test]
fn nothing_a_script_can_print_differs_between_vms() {
    let probes = [
        "return tostring({})",
        "return tostring(print)",
        "return tostring(function() end)",
        "return string.format('%s', {})",
        "return ('%s'):format(function() end)",
    ];
    for probe in probes {
        let mut seen = BTreeSet::new();
        for _ in 0..10 {
            let vm = Vm::new().unwrap();
            seen.insert(vm.load::<String>("print", probe).unwrap());
        }
        assert_eq!(seen.len(), 1, "{probe}: {seen:?}");
    }
}

#[test]
fn a_policy_iterating_a_table_decides_the_same_in_every_vm() {
    let workflow = Probe {
        policy: r#"(let [names {:alpha 1 :bravo 2 :charlie 3 :delta 4 :echo 5 :foxtrot 6 :golf 7}]
                     (icollect [name _ (pairs names)] (effect.await name)))"#,
        ..Probe::default()
    }
    .load()
    .unwrap();
    let mut seen = BTreeSet::new();
    for _ in 0..20 {
        let effects = pure::decide(&workflow, &Script::map([])).unwrap();
        seen.insert(awaits(&effects).join(","));
    }
    assert_eq!(seen.len(), 1, "{seen:?}");
    assert_eq!(
        seen.first().unwrap(),
        "alpha,bravo,charlie,delta,echo,foxtrot,golf"
    );
}

#[test]
fn state_a_policy_keeps_does_not_survive_to_its_next_call() {
    let workflow = Probe {
        top: "(local state {:calls 0})",
        policy: "(tset state :calls (+ state.calls 1))
                 [(effect.await (tostring state.calls))]",
        ..Probe::default()
    }
    .load()
    .unwrap();
    for _ in 0..3 {
        let effects = pure::decide(&workflow, &Script::map([])).unwrap();
        assert_eq!(awaits(&effects), ["1"]);
    }
}

#[tokio::test]
async fn gather_cannot_hand_policy_anything_but_the_facts() {
    let ran = run(Probe {
        top: "(local shared {})",
        gather: r#"(tset shared :note "from gather") {}"#,
        policy: r#"[(effect.await (tostring shared.note))]"#,
        ..Probe::default()
    })
    .await;
    assert_eq!(awaits(&ran.effects()), ["nil"]);
}

#[tokio::test]
async fn policy_has_no_cx_when_load_time_checks_are_gotten_past() {
    // `cx` is reached by a name the reader cannot follow, so this gets past the load-time check,
    // and the pure VM stops it.
    let ran = run(Probe {
        policy: r#"[((. _G :cx))]"#,
        ..Probe::default()
    })
    .await;
    assert!(
        ran.error().contains("attempt to call a nil value"),
        "{}",
        ran.error()
    );
}

#[test]
fn the_clock_and_the_outside_are_refused_when_named() {
    for policy in [
        "(os.time)",
        "(io.open :x)",
        "(os.getenv :HOME)",
        "(math.random)",
    ] {
        let probe = Probe {
            policy,
            ..Probe::default()
        };
        let result = probe.load();
        if policy == "(math.random)" {
            // `math` is allowed, so this one is caught when it runs. It is gone from the VM.
            let vm = Vm::new().unwrap();
            let value: LuaValue = vm.load("random", "return math.random").unwrap();
            assert_eq!(value, LuaValue::Nil);
        } else {
            let error = result.err().unwrap();
            assert_eq!(error.code, "load/compile", "{error}");
        }
    }
}

#[tokio::test]
async fn a_replay_of_a_read_returns_the_recorded_value() {
    // The read differs on every call in the sense that matters: `run_workflow` is not rerun, and the
    // replay reaches the recorded value by the same path the first run did.
    let ran = run(Probe {
        gather: r#"(let [seen (cx.observe :seen #{:text event.title :n 7})]
                     {:n seen.n :text seen.text})"#,
        policy: r#"[(effect.ask {:question (tostring facts.n) :target "a/b" :body facts.text})]"#,
        ..Probe::default()
    })
    .await;
    let first = format!("{:?}", ran.effects());
    let run_id = match ran.outcomes.first() {
        Some(almanac::runtime::Outcome::Ran { run_id, .. }) => run_id.clone(),
        other => panic!("{other:?}"),
    };
    let replayed = ran.runtime.replay(&run_id).await.unwrap();
    let almanac::runtime::Outcome::Ran { effects, .. } = replayed else {
        panic!("{replayed:?}");
    };
    assert_eq!(first, format!("{effects:?}"));
    assert!(!first.contains(HOSTILE), "{first}");
}

/// Prints timings: `cargo test --release --test determinism -- --ignored --nocapture`.
#[test]
#[ignore = "prints timings"]
fn what_a_vm_and_a_load_cost() {
    let started = Instant::now();
    let workflow = build_failures().unwrap();
    println!("load (compile + analyze): {:?}", started.elapsed());
    let event = serde_json::json!({
        "repo": "owner/repo", "branch": "renovate/x", "sha": "a".repeat(40), "failed": true,
        "finished": true, "patched": false, "bookkeeping": false,
    });
    let event = Script::from_record(&event).unwrap();

    let rounds = 2000;
    let started = Instant::now();
    for _ in 0..rounds {
        drop(Vm::new().unwrap());
    }
    println!("empty VM: {:?}", started.elapsed() / rounds);

    let started = Instant::now();
    for _ in 0..rounds {
        let vm = Vm::new().unwrap();
        vm.env.set("taint", vm.lua.create_table().unwrap()).unwrap();
        let _: Table = vm.load("chunk", workflow.lua()).unwrap();
    }
    println!(
        "VM plus the compiled chunk: {:?}",
        started.elapsed() / rounds
    );

    let started = Instant::now();
    for _ in 0..rounds {
        pure::wanted(&workflow, &event).unwrap();
    }
    println!("`when` on one event: {:?}", started.elapsed() / rounds);
    println!("compiled Lua: {} bytes", workflow.lua().len());

    let vm = Vm::new().unwrap();
    let started = Instant::now();
    let stopped = vm.load::<LuaValue>("spin", "while true do end");
    println!("an endless loop is stopped after {:?}", started.elapsed());
    assert!(stopped.is_err());
}
