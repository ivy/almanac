//! Every case in `crates/fails/tests/ui/fail/`, and what it becomes for a Lua workflow. Each row
//! runs: a rule said to hold at load must fail to load, one said to hold at run time must stop the
//! run before a model or the world is touched, and one said to be weakened must really let the
//! script through. `print_the_table` prints all of this as Markdown.
mod common;

use common::script::*;
use serde_json::{Value, json};
use std::collections::BTreeSet;

#[derive(Clone, Copy, PartialEq)]
enum Becomes {
    /// Refused when the file is loaded, before any event.
    Load,
    /// Refused when the offending line runs, and only then.
    Run,
    /// A Rust rule on Rust code that a script never touches, so the compile-fail case still holds.
    Rust,
    /// The specific check is gone. The row says what remains.
    Weakened,
}

impl Becomes {
    fn label(self) -> &'static str {
        match self {
            Self::Load => "load",
            Self::Run => "run time",
            Self::Rust => "unchanged (Rust)",
            Self::Weakened => "**weakened**",
        }
    }
}

enum Probe {
    /// These options must fail to load, saying this.
    Refused(&'static [(&'static str, &'static str)], &'static str),
    /// These options load, which is the weakening.
    Loads(&'static [(&'static str, &'static str)]),
    /// This `gather` body must fail the run, saying this.
    Gathers(&'static str, &'static str),
    /// A run whose `decide` body must fail, saying this.
    Decides(&'static str, &'static str),
    /// A `gather` and `decide` that must finish.
    Finishes(&'static str, &'static str),
    /// A `gather` body must fail for an agent's reply that breaks the rule.
    Replies(&'static str, Value, &'static str),
    /// Nothing to run: the Rust compile-fail case is still what proves this.
    StaysInRust,
}

struct Case {
    name: &'static str,
    becomes: Becomes,
    note: &'static str,
    probe: fn() -> Probe,
}

const fn case(
    name: &'static str,
    becomes: Becomes,
    note: &'static str,
    probe: fn() -> Probe,
) -> Case {
    Case {
        name,
        becomes,
        note,
        probe,
    }
}

const DONE: &str = r#"return { kind = "Done" }"#;

use Becomes::{Load, Run, Rust, Weakened};
use Probe::*;

const CASES: &[Case] = &[
    case(
        "call_model_without_cx",
        Run,
        "A script has no harness. The only route to a model is a `cx` operation, served by the host through `Cx`.",
        || Gathers("return cx.harness", "no field `harness`"),
    ),
    case(
        "cannot_read_without_prompt",
        Run,
        "Arity is checked by the host when the call is served.",
        || {
            Gathers(
                r#"cx.read("reader", {})"#,
                "read takes an agent, its vars, and its input",
            )
        },
    ),
    case(
        "capturing_narrow",
        Run,
        "`narrow` takes a name, so a closure is refused. A Lua function is always a closure, so none can be accepted.",
        || {
            Gathers(
                r#"local t, stolen = cx.hostile(), nil
               t:narrow(function(x) stolen = x; return true end)"#,
                "error converting Lua function to String",
            )
        },
    ),
    case(
        "checked_cannot_be_deserialized",
        Run,
        "`Push` needs the userdata only `check_patch` makes. A string cannot stand in.",
        || {
            Decides(
                r#"return { { kind = "Push", repo = "a/b", pull = 1, branch = "b", message = "commit", patch = "null" } }"#,
                "Push needs what check_patch returned",
            )
        },
    ),
    case(
        "checked_cannot_be_forged",
        Run,
        "A table of the right shape is still not the userdata.",
        || {
            Decides(
                r#"return { { kind = "Push", repo = "a/b", pull = 1, branch = "b", message = "commit", patch = { patch = {}, base = "x" } } }"#,
                "Push needs what check_patch returned",
            )
        },
    ),
    case(
        "cooled_cannot_be_copied",
        Run,
        "A script holds a ticket, and the proof stays in the driver. A second reference to the ticket is the same as `&Cooled`.",
        || {
            Gathers(
                "return table.clone(cx.proof())",
                "invalid argument #1 to 'clone'",
            )
        },
    ),
    case(
        "cooled_cannot_be_deserialized",
        Run,
        "Only the proof's ticket is accepted.",
        || {
            Gathers(
                r#"cx.repair("fixer", { repo = "a/b" }, {}, "null")"#,
                "repair needs a Cooled proof",
            )
        },
    ),
    case(
        "cooled_cannot_be_forged",
        Run,
        "A table of the right shape is refused, though it satisfies the analyzer.",
        || {
            Gathers(
                r#"cx.repair("fixer", { repo = "a/b" }, {}, { __cooled = "host only" })"#,
                "repair needs a Cooled proof",
            )
        },
    ),
    case(
        "effect_has_no_merge",
        Run,
        "No branch builds one. An unknown `kind` stops the whole run, on the branch that returns it.",
        || {
            Decides(
                r#"return { { kind = "Merge", repo = "a/b" } }"#,
                "not an effect",
            )
        },
    ),
    case(
        "file_title_is_a_string",
        Weakened,
        "A Lua string is accepted, because no hostile text is ever a Lua string. The rule now rests on that invariant, not on a type at the call site. Tainted values are still refused.",
        || {
            Finishes(
                DONE,
                r#"return { { kind = "File", title = "built " .. "at run time", target = "a/b" } }"#,
            )
        },
    ),
    case(
        "fixer_needs_cooled",
        Run,
        "Arity and proof are checked before the agent runs.",
        || {
            Gathers(
                r#"cx.repair("fixer", { repo = "a/b" }, {})"#,
                "a Cooled proof",
            )
        },
    ),
    case(
        "fixer_output_is_not_clean",
        Run,
        "Refused before the `Worker` runs.",
        || Gathers(r#"cx.act("writer", {}, cx.a_patch())"#, "only Clean input"),
    ),
    case(
        "format_into_string",
        Run,
        "`__tostring` raises, so `tostring`, `%*`, and interpolation all stop the run rather than produce a placeholder.",
        || {
            Gathers(
                r#"return string.format("%*", cx.hostile())"#,
                "no string form",
            )
        },
    ),
    case(
        "hostile_effect_title",
        Run,
        "`&'static str` becomes membership of the workflow's `literals`, which are fixed before any event.",
        || {
            Decides(
                r#"return { { kind = "Attest", context = "made" .. "-up", state = "Failure", repo = "a/b", sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" } }"#,
                "must be listed in the workflow's `literals`",
            )
        },
    ),
    case(
        "key_on_untrusted_text",
        Load,
        "Checked against the event's field list.",
        || Refused(&[("key", r#""{title}""#)], "which is Untrusted"),
    ),
    case(
        "key_unknown_field",
        Load,
        "Checked against the event's field list.",
        || Refused(&[("key", r#""{nope}""#)], "does not have"),
    ),
    case(
        "manual_clean_impl",
        Rust,
        "A script cannot declare anything `Clean`: the idea does not exist on its side.",
        || StaysInRust,
    ),
    case("missing_decide", Load, "", || {
        Refused(&[("decide", "nil")], "needs `decide")
    }),
    case(
        "missing_prompt_field",
        Run,
        "Vars are checked against the agent's declaration before the model is called.",
        || {
            Gathers(
                r#"cx.repair("fixer", {}, {}, cx.proof())"#,
                "`repo` is missing",
            )
        },
    ),
    case(
        "narrow_to_number",
        Run,
        "Only named Rust narrows exist, and each returns a closed value.",
        || {
            Gathers(
                r#"return cx.hostile():narrow("length")"#,
                "no narrow named `length`",
            )
        },
    ),
    case("narrow_to_slug", Run, "As above.", || {
        Gathers(
            r#"return cx.hostile():narrow("to_slug")"#,
            "no narrow named `to_slug`",
        )
    }),
    case("narrow_to_string", Run, "As above.", || {
        Gathers(
            r#"return cx.hostile():narrow("to_string")"#,
            "no narrow named `to_string`",
        )
    }),
    case(
        "observe_raw_string",
        Run,
        "There is no generic `observe`. Reads are named Rust functions, so a script cannot decide what is tainted.",
        || Gathers("return cx.observe", "no field `observe`"),
    ),
    case(
        "patch_is_not_clean",
        Run,
        "Taint is found wherever it is nested.",
        || {
            Gathers(
                r#"cx.act("writer", {}, { name = cx.a_patch() })"#,
                "only Clean input",
            )
        },
    ),
    case(
        "payload_field_rejects_string",
        Rust,
        "Events parse in Rust.",
        || StaysInRust,
    ),
    case(
        "payload_text_is_tainted",
        Rust,
        "Events parse in Rust. A script sees the field as userdata.",
        || StaysInRust,
    ),
    case(
        "plain_string_to_worker",
        Run,
        "A `Worker` takes `Clean` fields, and a string must parse as a `Slug`.",
        || {
            Gathers(
                r#"cx.act("writer", {}, { name = "free text!" })"#,
                "not a slug",
            )
        },
    ),
    case(
        "policy_cannot_take_cx",
        Run,
        "Stronger than the type: `decide` runs in a VM where `cx` was never created, so there is nothing to take or stash.",
        || Decides("return facts.attempts()", "no field `attempts`"),
    ),
    case(
        "prompt_field_never_used",
        Load,
        "The same template check as `#[prompt]`.",
        || {
            Refused(
                &[(
                    "agents",
                    r#"{ a = { profile = "worker", prompt = "write.md", vars = { repo = "Slug" } } }"#,
                )],
                "never uses",
            )
        },
    ),
    case(
        "prompt_is_not_a_runtime_value",
        Weakened,
        "An expression is accepted, but it is evaluated at load and must name a prompt embedded in the binary, so no event can choose it. The rule that it is a literal is gone.",
        || {
            Loads(&[(
                "agents",
                r#"{ a = { profile = "worker", prompt = "wri" .. "te.md" } }"#,
            )])
        },
    ),
    case("prompt_malformed_variable", Load, "", || {
        Refused(
            &[(
                "agents",
                r#"{ a = { profile = "worker", prompt = "malformed.md" } }"#,
            )],
            "is not a variable name",
        )
    }),
    case("prompt_missing_file", Load, "", || {
        Refused(
            &[(
                "agents",
                r#"{ a = { profile = "worker", prompt = "does-not-exist.md" } }"#,
            )],
            "no prompt named",
        )
    }),
    case("prompt_outside_prompts_dir", Load, "", || {
        Refused(
            &[(
                "agents",
                r#"{ a = { profile = "worker", prompt = "../Cargo.toml" } }"#,
            )],
            "a prompt is a name",
        )
    }),
    case(
        "prompt_rejects_hostile_text",
        Load,
        "The declared kind must be `Clean`. A tainted value passed anyway is refused when the call runs.",
        || {
            Refused(
                &[(
                    "agents",
                    r#"{ a = { profile = "worker", prompt = "fix.md", vars = { repo = "Untrusted" } } }"#,
                )],
                "must be Clean",
            )
        },
    ),
    case("prompt_rejects_plain_string", Load, "", || {
        Refused(
            &[(
                "agents",
                r#"{ a = { profile = "worker", prompt = "fix.md", vars = { repo = "string" } } }"#,
            )],
            "must be Clean",
        )
    }),
    case("prompt_typo_in_variable", Load, "", || {
        Refused(
            &[(
                "agents",
                r#"{ a = { profile = "worker", prompt = "typo.md", vars = { repo = "Slug" } } }"#,
            )],
            "has no such var",
        )
    }),
    case("prompt_unclosed_variable", Load, "", || {
        Refused(
            &[(
                "agents",
                r#"{ a = { profile = "worker", prompt = "unclosed.md", vars = { repo = "Slug" } } }"#,
            )],
            "unclosed",
        )
    }),
    case("prompt_variable_has_no_field", Load, "", || {
        Refused(
            &[(
                "agents",
                r#"{ a = { profile = "worker", prompt = "fix.md" } }"#,
            )],
            "has no such var",
        )
    }),
    case("raw_text_to_worker", Run, "", || {
        Gathers(r#"cx.act("writer", {}, cx.hostile())"#, "only Clean input")
    }),
    case(
        "reader_cannot_act",
        Run,
        "The profile is data here, not a type, so the check moves to the call.",
        || Gathers(r#"cx.act("reader", {}, {})"#, "cannot `act`"),
    ),
    case("reader_output_to_worker", Run, "", || {
        Gathers(
            r#"local out = cx.read("reader", {}, {}); cx.act("writer", {}, out)"#,
            "only Clean input",
        )
    }),
    case(
        "read_private_field",
        Run,
        "A field read returns `nil`. Nothing is there to read.",
        || {
            Finishes(
                r#"local t = cx.hostile(); assert(t.inner == nil); return { kind = "Done" }"#,
                "return {}",
            )
        },
    ),
    case(
        "scan_returns_the_wrong_event",
        Load,
        "A catalog scan names its event.",
        || Refused(&[("scan", r#""others""#)], "finds `Other`"),
    ),
    case(
        "scan_without_attribute",
        Load,
        "A scan is a catalog entry, so a script cannot supply one with unchecked needs.",
        || Refused(&[("scan", r#""plain""#)], "not in the catalog"),
    ),
    case("string_in_clean", Rust, "`clean!` types are Rust.", || {
        StaysInRust
    }),
    case(
        "string_in_observed",
        Rust,
        "`observed!` types are Rust.",
        || StaysInRust,
    ),
    case("subject_on_untrusted_text", Load, "", || {
        Refused(&[("subjects", r#"{ { repo = "title" } }"#)], "not a Slug")
    }),
    case(
        "title_from_string",
        Weakened,
        "As `file_title_is_a_string`. Whatever string a script holds is accepted.",
        || {
            Finishes(
                DONE,
                r#"return { { kind = "Ask", question = string.upper("any") .. " text", target = "a/b" } }"#,
            )
        },
    ),
    case("title_from_untrusted", Run, "", || {
        Decides(
            r#"return { { kind = "Ask", question = facts.t, target = "a/b" } }"#,
            "must be a string built from clean values",
        )
    }),
    case(
        "title_names_a_variable",
        Run,
        "There is no `title!`. Luau's own interpolation reaches `__tostring`, which raises.",
        || Gathers("local t = cx.hostile(); return `{t}`", "no string form"),
    ),
    case(
        "unknown_effect",
        Load,
        "`EffectKind` is parsed at load, so `Merge` cannot be declared.",
        || Refused(&[("may", r#"{ "Merge" }"#)], "not an effect"),
    ),
    case("unknown_option", Load, "", || {
        Refused(&[("retries", "3")], "unknown workflow option `retries`")
    }),
    case(
        "untrusted_in_clean",
        Rust,
        "`clean!` types are Rust.",
        || StaysInRust,
    ),
    case(
        "untrusted_patch_to_push",
        Run,
        "The patch policy cannot be skipped, but only the branch that tries it finds out.",
        || {
            Decides(
                r#"return { { kind = "Push", repo = "a/b", pull = 1, branch = "b", message = "commit", patch = facts.p } }"#,
                "Push needs what check_patch returned",
            )
        },
    ),
    case(
        "worker_output_must_be_clean",
        Run,
        "The output type is fixed in the host, so a reply with free text fails to parse.",
        || {
            Replies(
                r#"cx.act("writer", {}, {})"#,
                json!({"ok": "free text"}),
                "invalid type",
            )
        },
    ),
    case(
        "workflow_must_be_async",
        Load,
        "Moot: `gather` is always a coroutine. What remains is that it must be a function.",
        || Refused(&[("gather", "true")], "needs `gather"),
    ),
    case(
        "wrong_event_type",
        Weakened,
        "Luau annotations are erased, so a wrong parameter type loads and runs. Reading a field the event lacks stops the run, because host tables are strict, but only on that line.",
        || {
            Loads(&[(
                "gather",
                r#"function(cx, event: { nothing: string }) return { kind = "Done" } end"#,
            )])
        },
    ),
    case(
        "wrong_prompt_for_agent",
        Run,
        "An agent's prompt is bound in its declaration, so a call cannot pass the wrong one. Passing the wrong variables is refused.",
        || {
            Gathers(
                r#"cx.read("reader", { repo = "a/b" }, {})"#,
                "no variable `repo`",
            )
        },
    ),
];

/// Facts every `Decides` case can use: a tainted patch and tainted text.
const FACTS: &str = r#"return { kind = "Done", p = cx.a_patch(), t = cx.hostile() }"#;

async fn check(case: &Case) {
    let name = case.name;
    let probe = (case.probe)();
    let label = match &probe {
        Refused(..) => case.becomes == Load,
        Loads(_) => case.becomes == Weakened,
        Gathers(..) | Decides(..) | Replies(..) => case.becomes == Run,
        Finishes(..) => case.becomes == Run || case.becomes == Weakened,
        StaysInRust => case.becomes == Rust,
    };
    assert!(
        label,
        "{name}: its label does not match what its probe shows"
    );
    match probe {
        Refused(options, expected) => {
            let error = load("", options).err();
            let error = error.unwrap_or_else(|| panic!("{name} loaded")).to_string();
            assert!(error.contains(expected), "{name}: {error}");
        }
        Loads(options) => {
            load("", options).unwrap_or_else(|e| panic!("{name}: {e}"));
        }
        Gathers(body, expected) => {
            let ran = script("", body, "return {}").await;
            assert_stopped(name, &ran, expected);
        }
        Decides(body, expected) => {
            let ran = script("", FACTS, body).await;
            assert_stopped(name, &ran, expected);
        }
        Finishes(gather, decide) => {
            script("", gather, decide).await.effects();
        }
        Replies(body, reply, expected) => {
            let ran = script_replying("", body, "return {}", reply).await;
            let error = ran.error();
            assert!(error.contains(expected), "{name}: {error}");
        }
        StaysInRust => {}
    }
}

/// A refused run must have changed nothing: no effect applied, and, for a call that broke a rule
/// on its way to an agent, no agent started.
fn assert_stopped(name: &str, ran: &Ran, expected: &str) {
    let error = ran.error();
    assert!(error.contains(expected), "{name}: {error}");
    assert!(!error.contains(SENTINEL), "{name} repeated hostile text");
    assert_eq!(ran.forge.calls.lock().unwrap().len(), 0, "{name}");
    assert_eq!(ran.tracker.calls.lock().unwrap().len(), 0, "{name}");
    if name != "reader_output_to_worker" {
        assert_eq!(ran.harness.calls(), 0, "{name} reached an agent");
    }
}

#[test]
fn every_compile_fail_case_has_a_row() {
    let directory = concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../crates/fails/tests/ui/fail"
    );
    let on_disk: BTreeSet<String> = std::fs::read_dir(directory)
        .unwrap()
        .filter_map(|entry| {
            let path = entry.unwrap().path();
            let rust = path.extension().is_some_and(|e| e == "rs");
            rust.then(|| path.file_stem().unwrap().to_string_lossy().to_string())
        })
        .collect();
    let rows: BTreeSet<String> = CASES.iter().map(|c| c.name.to_string()).collect();
    assert_eq!(on_disk, rows);
    assert_eq!(CASES.len(), rows.len(), "a row is repeated");
}

#[tokio::test]
async fn every_row_holds() {
    for case in CASES {
        check(case).await;
    }
}

#[test]
fn print_the_table() {
    let analyzer = concat!(env!("CARGO_MANIFEST_DIR"), "/tests/typecheck/fail");
    for case in CASES {
        let caught = std::path::Path::new(analyzer).join(format!("{}.luau", case.name));
        let checked = if caught.exists() { "yes" } else { "no" };
        println!(
            "| `{}` | {} | {} | {} |",
            case.name,
            case.becomes.label(),
            checked,
            case.note
        );
    }
    let count = |b: Becomes| CASES.iter().filter(|c| c.becomes == b).count();
    println!(
        "load {}, run time {}, unchanged {}, weakened {}",
        count(Load),
        count(Run),
        count(Rust),
        count(Weakened)
    );
}
