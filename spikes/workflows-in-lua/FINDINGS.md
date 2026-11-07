# Workflows in Lua: findings

**Recommendation: drop it as the way to write workflows, and keep two things it taught.** The port works, and it is safe enough that safety is not the reason to drop it. The reason is that it does not deliver what it was for. The part of `build_failures` that is hard to understand cannot move to Lua, so a reader gains less than the side-by-side suggests and now has two languages to read. See [Recommendation](#recommendation).

The spike runs `build_failures` in Luau on the real runtime, against `crates/simforge` and scripted harnesses. It records the same effects as the compiled workflow in every scenario tried, and each version can replay the other's runs.

## What was built

Rust stays the trusted host, and a workflow is one Luau file embedded in the binary with `include_str!`.

- `workflows/build_failures.luau` is the port: trigger, key, intent, effects, budget, `when`, `gather`, and `decide`.
- `workflows/almanac.luau` holds types for the analyzer. It is never loaded, because `require("./almanac")` returns the host's module at run time.
- `src/` is the host. `LuaWorkflow` implements `Registered` directly, so `Runtime`, the stores, budgets, the outbox, `may`, and replay are all used unchanged.
- `gather` runs as a coroutine. Each `cx` call yields a request, and the driver serves it with the real `&Cx`, so every read, model call, and hold is recorded exactly as a compiled workflow's is.

Nothing in `crates/`, `almanacs/`, or `docs/` changed. `Title::__compose` is the one `#[doc(hidden)]` item the host relies on, because `title!` needs a literal format string and a script's question is not one.

## Lua 5.4 or Luau

**Luau.** Four results decide it. The Lua 5.4 figures come from a throwaway `mlua` probe that is not in this directory.

| Question | Lua 5.4 | Luau |
|---|---|---|
| Does `pairs` over string keys give one order across processes? | No: 8 orders in 8 processes | Yes: the same order in 3 processes |
| Does a CPU budget see inside `string.find`? | No: the count hook fired 0 times during a 29 s match | Yes: a worse pattern stopped at the budget in under 200 ms |
| Can a script print an address? | Yes: `%p`, and `%s` on a table | Only `tostring` and `%*`, which the host wraps |
| Are there types to buy guarantees back? | No | Yes: `luau-analyze` |

The first row alone rules out 5.4 for this project. A policy iterating an ordinary table would decide differently on replay, which breaks principle 7 for code that looks entirely innocent. Lua 5.4 also leaves the string metatable writable, and its base library keeps `load`, `dofile`, and `loadfile`, and `load` accepts binary chunks, so its sandbox would have to be built by hand.

Luau costs two things. Numbers are doubles, so a `u64` is exact only below 2^53, which is fine for pull request numbers and is checked. And Luau is C++, which a project with `unsafe_code = "forbid"` now links through FFI.

## Side by side

The port is close to line for line. This is the end of `gather`:

```rust
let update = pull.text.map(update_of);
let ages = cx.get::<dyn Ages>()?;
let Some(cooled) = cx.cooled(MIN_RELEASE_AGE, ages.newest(&update)).await? else {
    return Ok(Facts::Cooling);
};
cx.hold(TRUST_CONTEXT, &repo, &head).await?;
let logs = cx.observe("logs", read_logs(&client, &repo, &head)).await?;
let input = FixerInput {
    logs: logs.text,
    checkout: Checkout {
        repo: repo.clone(),
        sha: head.clone(),
    },
};
let prompt = FixerPrompt { repo: repo.clone() };
let patch = cx.agent(FIXER).repair(prompt, &input, &cooled).await?;
Ok(Facts::Repaired {
    repo,
    number: pull.number,
    branch: event.branch.clone(),
    head,
    patch,
})
```

```lua
local update = pull.text:map("update_of")
local cooled = cx.cooled(MIN_RELEASE_AGE, update)
if not cooled then
	return { kind = "Cooling" }
end
cx.hold(TRUST, repo, head)
local logs = cx.logs(repo, head)
local input = { logs = logs.text, ["$checkout"] = { repo = repo, sha = head } }
local patch = cx.repair("fixer", { repo = repo }, input, cooled)
return { kind = "Repaired", repo = repo, number = pull.number, branch = event.branch, head = head, patch = patch }
```

And one arm of policy:

```rust
Facts::Repaired { repo, number, branch, head, patch } => match DEFAULT_POLICY.check(patch, head) {
    Ok(patch) => vec![Effect::Push {
        repo: repo.clone(),
        pull: *number,
        branch: branch.clone(),
        message: "fix(deps): repair the build after this update",
        hold: Some(TRUST_CONTEXT),
        patch,
    }],
    Err(_) => stop_and_ask(
        repo,
        head,
        title!("A repair was refused by the patch policy"),
        patch.map(|p| p.diff.clone()),
    ),
},
```

```lua
Repaired = function(facts: Repaired)
	local checked = almanac.check_patch(facts.patch, facts.head)
	if not checked then
		local question = "A repair was refused by the patch policy"
		return stop_and_ask(facts.repo, facts.head, question, facts.patch:map("diff"))
	end
	return {
		{
			kind = "Push",
			repo = facts.repo,
			pull = facts.number,
			branch = facts.branch,
			message = COMMIT,
			hold = TRUST,
			patch = checked,
		},
	}
end,
```

### Which is easier to understand

For the sequence of decisions, **Lua is somewhat easier, and only somewhat.** `?`, `.await`, `.clone()`, and `&` are gone, and those are real noise for someone asking what a workflow does. `decide` as a table keyed by outcome reads well. But the Rust version was already a straight line, and the logic is identical, so what the reader gains is syntax rather than clarity of thought.

For the workflow as a whole, **Lua is not better, and I would call it worse.** The hard parts of `build_failures.rs` are `read_pull`, `read_logs`, and `needing_a_run`: which JSON field is hostile, what makes a pull request Renovate's, and how a delivery ID encodes what a later scan must notice. None of that can move, because reading a payload is where text becomes tainted, and that cannot be done by code that is allowed to read text.

| Lines | Compiled | Lua port |
|---|---|---|
| Declaration, facts, gather, policy | 138 | 133, with types |
| Reads, scan, and other Renovate code | 205, same file | 254 (`src/renovate.rs`), with the scan delegated |
| Types for the analyzer | none | 94 |
| Generic host | none | 1,759 |

The generic host would be shared between workflows, and the Renovate catalog would not.

Three things get harder to read in the port:

- `pull.text:map("update_of")` names a Rust function with a string. A reader cannot follow it, and neither can an editor.
- `literals = { TRUST, COOLING, COMMIT }` exists only because `Effect` wants `&'static str`. It has no meaning in the workflow's own terms.
- `{ kind = "Await", awaiting = COOLING }` is not `until`, because `until` is a reserved word.

The types that recover the guarantees also cost lines. `Facts` becomes five named types plus a `Decide` table that restates them, which is more to read than the Rust enum.

## What each safety rule becomes

The table is the output of `print_the_table` in `tests/safety_rules.rs`. Every row runs: a rule marked load must fail to load, one marked run time must stop the run with nothing applied and no agent reached, and one marked weakened must really let the script through. A test fails if a case is added to `crates/fails/tests/ui/fail/` without a row here.

"Analyzer" says whether `luau-analyze --mode=strict` also refuses it before anything runs, proved by a fixture in `tests/typecheck/fail/`.

| Case | Becomes | Analyzer | Note |
|---|---|---|---|
| `call_model_without_cx` | run time | yes | A script has no harness. The only route to a model is a `cx` operation, served by the host through `Cx`. |
| `cannot_read_without_prompt` | run time | yes | Arity is checked by the host when the call is served. |
| `capturing_narrow` | run time | yes | `narrow` takes a name, so a closure is refused. A Lua function is always a closure, so none can be accepted. |
| `checked_cannot_be_deserialized` | run time | yes | `Push` needs the userdata only `check_patch` makes. A string cannot stand in. |
| `checked_cannot_be_forged` | run time | yes | A table of the right shape is still not the userdata. |
| `cooled_cannot_be_copied` | run time | no | A script holds a ticket, and the proof stays in the driver. A second reference to the ticket is the same as `&Cooled`. |
| `cooled_cannot_be_deserialized` | run time | yes | Only the proof's ticket is accepted. |
| `cooled_cannot_be_forged` | run time | no | A table of the right shape is refused, though it satisfies the analyzer. |
| `effect_has_no_merge` | run time | yes | No branch builds one. An unknown `kind` stops the whole run, on the branch that returns it. |
| `file_title_is_a_string` | **weakened** | no | A Lua string is accepted, because no hostile text is ever a Lua string. The rule now rests on that invariant, not on a type at the call site. Tainted values are still refused. |
| `fixer_needs_cooled` | run time | yes | Arity and proof are checked before the agent runs. |
| `fixer_output_is_not_clean` | run time | yes | Refused before the `Worker` runs. |
| `format_into_string` | run time | yes | `__tostring` raises, so `tostring`, `%*`, and interpolation all stop the run rather than produce a placeholder. |
| `hostile_effect_title` | run time | no | `&'static str` becomes membership of the workflow's `literals`, which are fixed before any event. |
| `key_on_untrusted_text` | load | no | Checked against the event's field list. |
| `key_unknown_field` | load | no | Checked against the event's field list. |
| `manual_clean_impl` | unchanged (Rust) | no | A script cannot declare anything `Clean`: the idea does not exist on its side. |
| `missing_decide` | load | yes | |
| `missing_prompt_field` | run time | yes | Vars are checked against the agent's declaration before the model is called. |
| `narrow_to_number` | run time | yes | Only named Rust narrows exist, and each returns a closed value. |
| `narrow_to_slug` | run time | yes | As above. |
| `narrow_to_string` | run time | yes | As above. |
| `observe_raw_string` | run time | yes | There is no generic `observe`. Reads are named Rust functions, so a script cannot decide what is tainted. |
| `patch_is_not_clean` | run time | yes | Taint is found wherever it is nested. |
| `payload_field_rejects_string` | unchanged (Rust) | no | Events parse in Rust. |
| `payload_text_is_tainted` | unchanged (Rust) | no | Events parse in Rust. A script sees the field as userdata. |
| `plain_string_to_worker` | run time | yes | A `Worker` takes `Clean` fields, and a string must parse as a `Slug`. |
| `policy_cannot_take_cx` | run time | no | Stronger than the type: `decide` runs in a VM where `cx` was never created, so there is nothing to take or stash. |
| `prompt_field_never_used` | load | no | The same template check as `#[prompt]`. |
| `prompt_is_not_a_runtime_value` | **weakened** | no | An expression is accepted, but it is evaluated at load and must name a prompt embedded in the binary, so no event can choose it. The rule that it is a literal is gone. |
| `prompt_malformed_variable` | load | no | |
| `prompt_missing_file` | load | no | |
| `prompt_outside_prompts_dir` | load | no | |
| `prompt_rejects_hostile_text` | load | no | The declared kind must be `Clean`. A tainted value passed anyway is refused when the call runs. |
| `prompt_rejects_plain_string` | load | no | |
| `prompt_typo_in_variable` | load | no | |
| `prompt_unclosed_variable` | load | no | |
| `prompt_variable_has_no_field` | load | no | |
| `raw_text_to_worker` | run time | yes | |
| `reader_cannot_act` | run time | yes | The profile is data here, not a type, so the check moves to the call. |
| `reader_output_to_worker` | run time | yes | |
| `read_private_field` | run time | yes | A field read returns `nil`. Nothing is there to read. |
| `scan_returns_the_wrong_event` | load | no | A catalog scan names its event. |
| `scan_without_attribute` | load | no | A scan is a catalog entry, so a script cannot supply one with unchecked needs. |
| `string_in_clean` | unchanged (Rust) | no | `clean!` types are Rust. |
| `string_in_observed` | unchanged (Rust) | no | `observed!` types are Rust. |
| `subject_on_untrusted_text` | load | no | |
| `title_from_string` | **weakened** | no | As `file_title_is_a_string`. Whatever string a script holds is accepted. |
| `title_from_untrusted` | run time | yes | |
| `title_names_a_variable` | run time | no | There is no `title!`. Luau's own interpolation reaches `__tostring`, which raises. |
| `unknown_effect` | load | yes | `EffectKind` is parsed at load, so `Merge` cannot be declared. |
| `unknown_option` | load | no | |
| `untrusted_in_clean` | unchanged (Rust) | no | `clean!` types are Rust. |
| `untrusted_patch_to_push` | run time | yes | The patch policy cannot be skipped, but only the branch that tries it finds out. |
| `worker_output_must_be_clean` | run time | yes | The output type is fixed in the host, so a reply with free text fails to parse. |
| `workflow_must_be_async` | load | no | Moot: `gather` is always a coroutine. What remains is that it must be a function. |
| `wrong_event_type` | **weakened** | yes | Luau annotations are erased, so a wrong parameter type loads and runs. Reading a field the event lacks stops the run, because host tables are strict, but only on that line. |
| `wrong_prompt_for_agent` | run time | yes | An agent's prompt is bound in its declaration, so a call cannot pass the wrong one. Passing the wrong variables is refused. |

In total: 18 at load, 30 at run time, 6 unchanged in Rust, and 4 weakened.

The important column is timing, not strength. Nothing in this table is lost outright, but "run time" means the rule fires when that line runs. `untrusted_patch_to_push` in a rarely taken branch is a compile error in Rust and an outage on a live pull request in Lua: it fails closed, on the first event to reach it, after the agent has been paid for.

### What Luau's types buy back

A lot, for code that opts in. The analyzer refuses 28 of the 58 cases, including 25 of the 30 that otherwise wait for run time. `tests/typecheck/fail/` proves each one fails on its own line, and `tests/typecheck/missed/` pins what the analyzer accepts.

Three limits matter:

- **It is opt-in, per binding.** `--!strict` does not require annotations, so an unannotated `facts` accepts any field without complaint (`missed/unannotated_facts.luau`). Exhaustiveness over outcomes exists only if the `Decide` type is written and named.
- **It is outside the host.** `mlua` does not embed the analyzer, so it is a CI step that can be skipped, and the binary cannot refuse a file that never passed it.
- **It cannot see strings that mean something.** `key = "{title}"`, a prompt's variables, and `literals` are only strings to it. The host's load check covers those, so the two checks are complements. Neither replaces `rustc`.

Its diagnostics are also weak on large tables: a mismatch in one field of the workflow table is reported as a dozen errors at the table's first line.

## How taint works

A hostile value is userdata. It wraps an `Untrusted<T>` that a script can pass along, and the only things it can do with one are name a `map` or a `narrow` from a closed Rust catalog.

The invariant underneath is that **no host function returns observed text as a Lua string.** So every Lua string a script holds is its own literal or a `Clean` value, which is what lets `title` accept a string. This inverts Rust's arrangement: taint is not a property strings carry, because hostile text is never a string at all. It also means the rule rests on the host's reads being written correctly, and a read that returned a body as `Node::Str` would break everything at once. That code is Rust and reviewed, as `read_pull` is today.

`narrow` and `map` take names, because a Lua function is always a closure. Rust's `fn` pointer stops capture at compile time, and Lua has no equivalent, so the only way to keep the rule is to accept no code at all. A declarative narrow such as `{ contains = "..." }` was rejected on purpose: if a script can choose the string at run time, it can ask "does the text contain this?" one character at a time and read the text back out. This is why every narrow is a Rust `fn`, and it means any new way to narrow is a change to the host.

### Breaking out

`tests/taint.rs` makes 76 attempts, each in the workflow sandbox and again in a VM with Luau's whole standard library, to separate what the userdata holds against from what the allowlist removes. **None read the text in either VM.**

| Route | What happens |
|---|---|
| `tostring`, `%*`, interpolation | `__tostring` raises, so the run stops rather than printing a placeholder |
| `..`, `#`, arithmetic, comparison, calling, iterating | Refused by the VM: no metamethod exists |
| `string.*`, `table.concat`, `utf8.*`, `tonumber` | Refused or `nil`: none coerces userdata |
| `getmetatable` (raw VM) | Returns a boolean, not the metatable |
| `setmetatable` with `__concat`, `__index`, `__eq`, `__lt` on a table (raw VM) | The handler receives the userdata, which is still opaque |
| `debug.info`, `getfenv`, `loadstring`, `newproxy` (raw VM) | Available and useless: Luau's `debug` has no `getupvalue` or `getlocal`, and nothing holds the text in Lua's reach |
| `narrow` or `map` with a closure | Refused: a name is required |
| `error(h)`, then read the error | The run fails, and the message does not repeat the text. It is also unhelpful: "error in error handling" |
| `h == "the text"` | `false`: equality is identity, so it is not an oracle |

The text is in Rust memory behind a userdata handle, and no Lua-level operation tried reaches it. Taint held in every attempt.

The allowlist is the weaker layer, and this is the finding a future author most needs. `Lua::sandbox(true)` turns on `safeenv`, and under `safeenv` a call to a builtin skips the global lookup. **Deleting `setmetatable` from the environment does not remove it:** the global reads as `nil` and the call still works. `getmetatable` and `rawset` behave the same way. A test checking `setmetatable == nil` passes while the function runs. The host turns `safeenv` off, and `nothing_outside_the_allowlist_can_be_called` calls each one instead of comparing it. This did not expose taint, but it shows how a sandbox can be wrong while every obvious check is green.

## How policy stays pure

`decide` runs in its own VM, built after `gather` finishes, with the file loaded again. Facts cross as data through `Node`, which refuses functions and threads, so nothing `gather` did can be seen except what it returned. `cx` was never created in that VM, so a stashed reference is `nil` rather than a capability (`policy_cannot_use_a_cx_that_gather_stashed`, `policy_cannot_see_what_gather_left_in_the_file`). A fresh VM with the file loaded costs about 1 ms in a debug build.

Every VM starts from an allowlist: no `io`, `os`, `debug`, `coroutine`, `load`, `getfenv`, `setmetatable`, `collectgarbage`, or `math.random`. Globals and libraries are read-only. Interrupts are counted, so a runaway stops at the same place each time rather than at a wall-clock limit, and memory is capped. A failed read stops the coroutine where it stands rather than raising inside it, so `pcall` cannot swallow an outage, and no read's error text ever reaches a script.

**On purity, Lua is stronger than Rust.** `Workflow::decide` is a plain `fn`. `policy_cannot_take_cx` stops it taking a `Cx`, but nothing stops it reading a clock, a file, or a static, and `clippy.toml` has no `disallowed-methods`. In Lua, none of those exist in the VM. The same is true of the documented holes in `crates/fails/tests/ui/pass/`: a Rust author can write `serde_json::to_string(&hostile())` or implement `Sealed` by hand, and a script can do neither. **Rust's guarantees arrive earlier, and Lua's are harder to get around.**

### Replay

Replay needs no new mechanism. `Runtime::replay` calls `execute` again, and `Cx` answers from the log. `a_replay_reuses_what_was_recorded_and_reaches_the_same_effects` replays with the forge returning 500 for everything, and neither the model nor the forge is touched.

Because both versions record under the same names and types, `each_version_can_replay_a_run_the_other_recorded`: a run recorded by Rust replays in Lua, and the reverse. That is the strongest evidence that the port is faithful.

One gap remains, and I could not close it. **A table keyed by tables iterates in address order, giving 11 or 12 distinct orders in 12 processes.** A replay is a later process, so a policy doing this could decide differently with no error. The VM's own `for` reaches this order directly, so no wrapper can intercept it, and the analyzer does not flag it. `iterating_a_table_keyed_by_tables_is_not_replay_safe` keeps it visible. It takes an unusual policy to hit this, and Rust's `Vec` and `BTreeMap` have no such hazard. Addresses from `tostring` and `%*` are closed by wrappers, and string-keyed iteration is stable.

## How a manifest is derived

`cargo run -- synth` prints it through Almanac's own verb, and `the_manifest_matches_the_compiled_workflows` asserts it equals the compiled workflow's, field for field.

**It can be known without running a workflow, but not without running the file.** Loading evaluates the top level to get the table and calls neither `gather` nor `decide`. That evaluation is deterministic, because a file sees no clock, input, or `cx`, and it is bounded by the budget. But the top level is code, so a declaration can be computed (`a_computed_declaration_needs_evaluating`), and no static reader can promise to see it. In Rust, `#[workflow]` reads literal tokens.

The larger difference is what it means. `#[workflow]` *derives* agents and requirements by walking the function body, so they cannot drift. Here `reads` and `agents` are declared by hand:

- Declare too little, and the run stops on that line, because the operation is not on `cx`. This fails safe but late.
- Declare too much, and nothing objects, so the manifest overstates what the workflow does, and a capability change in review may be noise.

`luau-ast` recovers the check: `the_declared_reads_are_the_ones_the_source_uses` walks the syntax tree and compares `cx.*` uses with `reads`. It has the same limit as the macro, in that a `cx` passed to a helper under another name is not seen, and like the analyzer it runs outside the host.

## Recommendation

**Drop it as the authoring language.** Ivy's question was whether a looser language would make software factories more readily understood, with lost compile-time guarantees an acceptable price. The price turned out to be acceptable. What it buys is too small.

1. **The hard part cannot move.** Lua removes punctuation from about 100 lines that were already a straight line. The code that takes effort to understand decides what is tainted, so it must stay in Rust, and every new read, event, map, or narrow is a Rust change. Understanding a workflow now means reading two languages, joined by strings.
2. **The cost is real and permanent.** The host is 2,013 lines of Rust to run 133 lines of Luau, plus a C++ dependency and a second toolchain for types. `docs/vision.md` says "the compiler enforces the rules that matter most". 30 of the 58 rules now fire on the line that breaks them, which the analyzer narrows only for code that opts in. Principle 10 warns against "a framework where a function would do", and this is a framework.
3. **The usual reason for an embedded language is ruled out.** Scripting earns its keep through changing behaviour without a rebuild. Principle 2 forbids that, and rightly, since it is what keeps a workflow reviewed, so the files must be compiled in. What remains is syntax.

Two things are worth keeping regardless:

- **Close the purity gap in Rust.** Lua showed `decide` is pure by convention. A `disallowed-methods` lint, or a check that policy modules take no clock or I/O imports, would get much of Lua's guarantee at compile time. This is the most useful result here, and it costs a few lines.
- **Take the readability gain directly.** Most of what Lua removed is `.clone()` and `&`. If `Effect` constructors and `Facts` borrowed rather than owned, or a small macro built `Effect::Push` from a struct's fields, the Rust might get most of the way there with no second language. This is untested.

**What would change this.** The trade flips if workflows come to be written by agents or by people who do not read Rust, and are reviewed less closely. Then "harder to get around" outweighs "earlier": a script cannot read a clock or a hostile string however it is written, and a Rust workflow can. In that world, adopt it for `gather` and `decide` only, over a Rust catalog, as built here. The spike shows that layer is viable, and this host is a fair starting point. That is not today's situation, with one operator and two workflows.

## What was not finished

- Only `build_failures` is ported. `security_fixes` would show how fast the catalog grows, and that is the number that matters most for the cost argument.
- `scan` is not ported: `scan = "build_failures"` delegates to the compiled scan through `Registered::scan`. It is I/O and needs no judgment, so I expect it would stay in Rust regardless, but that is a judgment and is not tested.
- `read_pull` and `read_logs` are copied into `src/renovate.rs`, because the originals are private and `almanacs/` was not to change.
- `cx.screen`, `Halt::Stop`, `links`, and `record_fact` have no Lua surface, because `build_failures` does not use them.
- `Worker` input and output are fixed host types rather than something a workflow declares.
- The spike ran on `MemoryStore`, not PostgreSQL. It uses the same `Store` trait, and I have no reason to expect a difference, but it was not run.
- The escape attempts are mine alone. Nobody independent has tried to break the sandbox.
- Which version reads better is my judgment. Nobody else has read these side by side, and the Fennel spike's author is the obvious second opinion.

## Reproducing

Run these from this directory:

```bash
mise install luau@0.745
mise exec luau@0.745 -- cargo test
cargo run -- synth
```

Without `luau-analyze` and `luau-ast` on `PATH`, the tests that need them say so and pass, because nothing in the host depends on them.
