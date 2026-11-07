//! What each compile-fail case in `crates/fails/tests/ui/fail/` becomes in Fennel. Each case that
//! has a script form is run here. The rest say why not, and a test keeps this list complete.
mod common;
use common::*;
use serde_json::json;
use std::collections::BTreeSet;

enum Expect {
    /// Refused when the workflow loads, with this error code.
    Load(&'static str),
    /// Fails in the run with this text, before the named thing happens.
    Run(&'static str),
}
use Expect::*;

struct Case {
    name: &'static str,
    probe: Probe,
    expect: Expect,
}

fn case(name: &'static str, expect: Expect, probe: Probe) -> Case {
    Case {
        name,
        probe,
        expect,
    }
}

fn gather(gather: &'static str) -> Probe {
    Probe {
        gather,
        ..Probe::default()
    }
}

fn top(top: &'static str) -> Probe {
    Probe {
        top,
        ..Probe::default()
    }
}

fn tainted_into_policy(policy: &'static str) -> Probe {
    Probe {
        gather: "{:t event.title :p event.pull}",
        policy,
        ..Probe::default()
    }
}

fn cases() -> Vec<Case> {
    vec![
        case(
            "call_model_without_cx",
            Load("load/compile"),
            gather("(harness.run {})"),
        ),
        case(
            "cannot_read_without_prompt",
            Run("`:prompt` must be a table"),
            gather("(cx.read :reader {:input {}})"),
        ),
        case(
            "capturing_narrow",
            Load("load/narrower-captures"),
            top("(local stolen []) (narrower steal [t] (table.insert stolen t) true)"),
        ),
        case(
            "checked_cannot_be_deserialized",
            Run("`:patch` must be what"),
            Probe {
                policy: r#"[(effect.push {:repo "a/b" :pull 1 :branch "renovate/x" :message "fix"
                                          :patch {:changes [] :diff ""}})]"#,
                ..Probe::default()
            },
        ),
        case(
            "checked_cannot_be_forged",
            Run("`:patch` must be what"),
            Probe {
                policy: r#"[(effect.push {:repo "a/b" :pull 1 :branch "renovate/x" :message "fix"
                                          :patch "checked"})]"#,
                ..Probe::default()
            },
        ),
        case(
            "cooled_cannot_be_copied",
            Run("cannot leave its VM"),
            Probe {
                top: "(fn prove [cx event]
                        (let [ages (cx.get :ages)]
                          (cx.cooled 0 (ages.newest (taint.map event.pull :update-of)))))",
                gather: "(let [proof (prove cx event)] (cx.observe :copy #proof))",
                ..Probe::default()
            },
        ),
        case(
            "cooled_cannot_be_deserialized",
            Run("`:cooled` must be the proof"),
            gather(
                "(cx.repair :fixer {:prompt {:repo event.repo} :input {}
                                    :cooled (cx.observe :stored #{:cooled true})})",
            ),
        ),
        case(
            "cooled_cannot_be_forged",
            Run("`:cooled` must be the proof"),
            gather("(cx.repair :fixer {:prompt {:repo event.repo} :input {} :cooled {}})"),
        ),
        case(
            "effect_has_no_merge",
            Load("load/not-in-host"),
            Probe {
                policy: "[(effect.merge {})]",
                ..Probe::default()
            },
        ),
        case(
            "file_title_is_a_string",
            Run("`:title` must be a string"),
            tainted_into_policy(
                r#"[(effect.file {:title facts.t :target "a/b" :body (taint.wrap "")})]"#,
            ),
        ),
        case(
            "fixer_needs_cooled",
            Load("load/fixer-needs-cooled"),
            gather("(cx.repair :fixer {:prompt {:repo event.repo} :input {}})"),
        ),
        case(
            "fixer_output_is_not_clean",
            Run("hold no free text"),
            gather("(cx.act :writer {:prompt {} :input {:fixed event.pull}})"),
        ),
        case(
            "format_into_string",
            Run("attempt to concatenate"),
            gather(r#"{:text (.. "x" event.title)}"#),
        ),
        case(
            "hostile_effect_title",
            Run("`:context` must be a string"),
            tainted_into_policy(
                r#"[(effect.attest {:context facts.t :state :success :repo "a/b"
                                    :sha (string.rep "0" 40)})]"#,
            ),
        ),
        case(
            "key_on_untrusted_text",
            Load("load/tainted-field"),
            Probe {
                key: "{title}",
                ..Probe::default()
            },
        ),
        case(
            "key_unknown_field",
            Load("load/unknown-field"),
            Probe {
                key: "{nope}",
                ..Probe::default()
            },
        ),
        case(
            "missing_decide",
            Load("load/missing-option"),
            Probe {
                decide: "",
                ..Probe::default()
            },
        ),
        case(
            "missing_prompt_field",
            Load("load/prompt-field"),
            Probe {
                top: r#"(agent :two {:profile :worker :prompt "two.md" :vars [:a :b]})"#,
                gather: "(cx.act :two {:prompt {:a 1} :input {}})",
                ..Probe::default()
            },
        ),
        case(
            "narrow_to_number",
            Run("must return a bool"),
            Probe {
                top: "(narrower size [t] (length t))",
                gather: "{:n (taint.narrow event.title :size)}",
                ..Probe::default()
            },
        ),
        case(
            "narrow_to_slug",
            Run("must return a bool"),
            Probe {
                top: r#"(narrower slug [t] "owner/repo")"#,
                gather: "{:n (taint.narrow event.title :slug)}",
                ..Probe::default()
            },
        ),
        case(
            "narrow_to_string",
            Run("must return a bool"),
            Probe {
                top: "(narrower same [t] t)",
                gather: "{:n (taint.narrow event.title :same)}",
                ..Probe::default()
            },
        ),
        case(
            "patch_is_not_clean",
            Run("hold no free text"),
            gather("(cx.act :writer {:prompt {} :input event.pull})"),
        ),
        case(
            "plain_string_to_worker",
            Run("hold no free text"),
            gather(r#"(cx.act :writer {:prompt {} :input "free text"})"#),
        ),
        case(
            "policy_cannot_take_cx",
            Load("load/impure-policy"),
            Probe {
                policy_args: "[facts cx]",
                ..Probe::default()
            },
        ),
        case(
            "prompt_field_never_used",
            Load("load/prompt-var"),
            top(r#"(agent :p {:profile :worker :prompt "plain.md" :vars [:repo]})"#),
        ),
        case(
            "prompt_is_not_a_runtime_value",
            Load("load/not-literal"),
            top(r#"(local name "plain.md") (agent :p {:profile :worker :prompt name :vars []})"#),
        ),
        case(
            "prompt_malformed_variable",
            Load("load/prompt-template"),
            top(r#"(agent :p {:profile :worker :prompt "malformed.md" :vars [:repo_name]})"#),
        ),
        case(
            "prompt_missing_file",
            Load("load/prompt-missing"),
            top(r#"(agent :p {:profile :worker :prompt "does-not-exist.md" :vars []})"#),
        ),
        case(
            "prompt_outside_prompts_dir",
            Load("load/prompt-path"),
            top(r#"(agent :p {:profile :worker :prompt "../Cargo.toml" :vars []})"#),
        ),
        case(
            "prompt_rejects_hostile_text",
            Run("never reaches a prompt"),
            gather("(cx.repair :fixer {:prompt {:repo event.title} :input {} :cooled true})"),
        ),
        case(
            "prompt_rejects_plain_string",
            Run("must be a slug"),
            gather(r#"(cx.repair :fixer {:prompt {:repo "free text"} :input {} :cooled true})"#),
        ),
        case(
            "prompt_typo_in_variable",
            Load("load/prompt-var"),
            top(r#"(agent :p {:profile :worker :prompt "typo.md" :vars [:repo]})"#),
        ),
        case(
            "prompt_unclosed_variable",
            Load("load/prompt-template"),
            top(r#"(agent :p {:profile :worker :prompt "unclosed.md" :vars [:repo]})"#),
        ),
        case(
            "prompt_variable_has_no_field",
            Load("load/prompt-var"),
            top(r#"(agent :p {:profile :worker :prompt "fix.md" :vars []})"#),
        ),
        case(
            "raw_text_to_worker",
            Run("hold no free text"),
            gather("(cx.act :writer {:prompt {} :input event.title})"),
        ),
        case(
            "read_private_field",
            Run("attempt to index"),
            gather("{:text event.title.inner}"),
        ),
        case(
            "reader_cannot_act",
            Load("load/wrong-verb"),
            gather("(cx.act :reader {:prompt {} :input {}})"),
        ),
        case(
            "reader_output_to_worker",
            Run("hold no free text"),
            gather(
                "(let [out (cx.read :reader {:prompt {} :input {}})]
                   (cx.act :writer {:prompt {} :input out}))",
            ),
        ),
        case(
            "subject_on_untrusted_text",
            Load("load/tainted-field"),
            Probe {
                options: ":subjects [(repo title)]",
                ..Probe::default()
            },
        ),
        case(
            "title_from_untrusted",
            Run("`:question` must be a string"),
            tainted_into_policy(
                r#"[(effect.ask {:question facts.t :target "a/b" :body (taint.wrap "")})]"#,
            ),
        ),
        case(
            "unknown_effect",
            Load("load/unknown-effect"),
            Probe {
                options: ":may [:Merge]",
                ..Probe::default()
            },
        ),
        case(
            "unknown_option",
            Load("load/unknown-option"),
            Probe {
                options: ":retries 3",
                ..Probe::default()
            },
        ),
        case(
            "untrusted_patch_to_push",
            Run("`:patch` must be what"),
            tainted_into_policy(
                r#"[(effect.push {:repo "a/b" :pull 1 :branch "renovate/x" :message "fix"
                                  :patch facts.p})]"#,
            ),
        ),
        case(
            "wrong_event_type",
            Run("has no field `nope`"),
            gather("{:x event.nope}"),
        ),
        case(
            "wrong_prompt_for_agent",
            Load("load/prompt-field"),
            gather(r#"(cx.read :reader {:prompt {:repo "a/b"} :input {}})"#),
        ),
    ]
}

/// Cases with no script form, and why. `worker_output_must_be_clean` has a test of its own.
const OTHERWISE: &[(&str, &str)] = &[
    ("manual_clean_impl", "scripts declare no types"),
    (
        "observe_raw_string",
        "a Lua string cannot be hostile, so the rule has nothing to refuse",
    ),
    (
        "payload_field_rejects_string",
        "events are parsed by Rust, and this still fails to compile",
    ),
    (
        "payload_text_is_tainted",
        "events are parsed by Rust, and this still fails to compile",
    ),
    (
        "scan_returns_the_wrong_event",
        "this spike does not port scan",
    ),
    ("scan_without_attribute", "this spike does not port scan"),
    ("string_in_clean", "scripts declare no types"),
    (
        "string_in_observed",
        "events are declared in Rust, and this still fails to compile",
    ),
    (
        "title_from_string",
        "a Lua string cannot be hostile, so the rule has nothing to refuse",
    ),
    (
        "title_names_a_variable",
        "Fennel has no format string to smuggle a name through",
    ),
    ("untrusted_in_clean", "scripts declare no types"),
    (
        "worker_output_must_be_clean",
        "see its own test, because it needs an agent's reply",
    ),
    (
        "workflow_must_be_async",
        "every gather can yield, so there is no sync form to refuse",
    ),
];

#[tokio::test]
async fn every_rule_with_a_script_form_is_refused() {
    for Case {
        name,
        probe,
        expect,
    } in cases()
    {
        match expect {
            Load(code) => {
                let error = match probe.load() {
                    Err(error) => error,
                    Ok(_) => panic!("{name}: loaded"),
                };
                assert_eq!(error.code, code, "{name}: {error}");
            }
            Run(text) => {
                let workflow = probe.load().unwrap_or_else(|e| panic!("{name}: {e}"));
                let ran = run_workflow(workflow, json!({})).await;
                let error = ran.error();
                assert!(error.contains(text), "{name}: {error}");
                assert!(!error.contains(HOSTILE), "{name}: {error}");
                let prompts = ran.harness.prompts.lock().unwrap();
                assert!(
                    !prompts.iter().any(|p| p == "write"),
                    "{name}: the worker ran"
                );
                assert!(
                    !prompts.iter().any(|p| p.starts_with("fix")),
                    "{name}: the fixer ran"
                );
            }
        }
    }
}

#[tokio::test]
async fn a_proof_from_cx_cooled_is_accepted() {
    let workflow = Probe {
        top: "(fn prove [cx event]
                (let [ages (cx.get :ages)]
                  (cx.cooled 0 (ages.newest (taint.map event.pull :update-of)))))",
        gather: "(cx.repair :fixer {:prompt {:repo event.repo} :input {} :cooled (prove cx event)})
                 {}",
        ..Probe::default()
    };
    let reply = json!({"diff": "", "changes": []});
    let ran = run_workflow(workflow.load().unwrap(), reply).await;
    ran.effects();
    assert_eq!(ran.harness.calls(), 1);
}

#[tokio::test]
async fn worker_output_must_be_clean() {
    let workflow = gather("(cx.act :writer {:prompt {} :input {}})")
        .load()
        .unwrap();
    let ran = run_workflow(workflow, json!("free text")).await;
    assert!(ran.error().contains("hold no free text"), "{}", ran.error());

    let workflow = gather("(cx.act :writer {:prompt {} :input {}}) {}")
        .load()
        .unwrap();
    let ran = run_workflow(workflow, json!({"done": true, "repo": "a/b"})).await;
    ran.effects();
}

#[tokio::test]
async fn policy_that_reaches_cx_through_a_helper_is_refused_at_load() {
    let source = Probe {
        top: "(fn sneaky [cx] (cx.attempts))",
        policy: "[(sneaky facts)]",
        ..Probe::default()
    };
    assert_eq!(source.load().err().unwrap().code, "load/impure-policy");
}

#[tokio::test]
async fn a_slug_shaped_string_passes_as_a_worker_input() {
    // A limit worth seeing: a `Clean` Rust type would be declared, and here any slug-shaped text
    // is accepted, because a script's value has no declared type to check it against.
    let probe = gather(r#"(cx.act :writer {:prompt {} :input "not-really-a-repo"}) {}"#);
    let ran = run_workflow(probe.load().unwrap(), json!({})).await;
    ran.effects();
    assert_eq!(ran.harness.calls(), 1);
}

/// A `case` that matches nothing returns nil. Rust refuses this at compile time.
#[tokio::test]
async fn a_policy_with_no_matching_pattern_fails_the_run() {
    let ran = run(Probe {
        gather: "{:kind :repaird}",
        policy: "(case facts {:kind :repaired} [])",
        ..Probe::default()
    })
    .await;
    assert!(
        ran.error().contains("no pattern matched"),
        "{}",
        ran.error()
    );
}

#[tokio::test]
async fn an_effect_a_workflow_did_not_declare_is_denied_by_the_runtime() {
    let source = Probe {
        options: ":may [:Await]",
        policy: r#"[(effect.await "a") (effect.ask {:question "q" :target "a/b" :body (taint.wrap "")})]"#,
        ..Probe::default()
    };
    let ran = run_workflow(source.load().unwrap(), json!({})).await;
    assert_eq!(ran.effects().len(), 1, "{:?}", ran.outcomes);
    assert!(
        ran.visible().contains("denied: [ActionSummary"),
        "{}",
        ran.visible()
    );
}

#[test]
fn every_compile_fail_case_is_accounted_for() {
    let dir = concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../crates/fails/tests/ui/fail"
    );
    let on_disk: BTreeSet<String> = std::fs::read_dir(dir)
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .filter(|path| path.extension().is_some_and(|e| e == "rs"))
        .map(|path| path.file_stem().unwrap().to_str().unwrap().to_string())
        .collect();
    let cases = cases();
    let covered = cases
        .iter()
        .map(|case| case.name)
        .chain(OTHERWISE.iter().map(|(n, _)| *n));
    let covered: BTreeSet<String> = covered.map(str::to_string).collect();
    assert_eq!(on_disk, covered);
}
