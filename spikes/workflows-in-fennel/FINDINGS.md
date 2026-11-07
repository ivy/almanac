# Workflows in Fennel: findings

The question: would workflows written in Fennel be easier to understand than Rust, at an acceptable cost in guarantees?

**Recommendation: drop Fennel as a way to write workflows, and take four things this spike found back into the Rust.** The guarantees held up better than expected, so safety is not the reason. Fennel reads better for the policy and the declaration, but that gain is small next to a second language, a trusted host three times the size of what it replaces, and rules that fail in a run instead of a build. See [Recommendation](#recommendation).

`build_failures` runs for real here: the same runtime, store, outbox, and replay as a Rust workflow, against `crates/simforge`, the GitHub forge, and a scripted agent. Each end-to-end scenario also runs against the Rust workflow in the same test, and both produce the same effects, statuses, and agent request. Nothing under `crates/`, `almanacs/`, or `docs/` changed. Run `cargo test` here, and `cargo run --example synth` to see the manifests.

## How it is built

- **A compile VM holds Fennel.** It has no `io`, `os`, or `debug`, and Fennel 1.6.1 loads without them. It runs once, at load, and produces Lua text and a declaration.
- **Each run gets new VMs holding only that Lua.** `gather` runs in one and policy in another, and each is dropped afterwards. They share nothing but the facts, which cross as host-owned data.
- **`gather` is a coroutine.** Each step yields a request, and Rust performs it through `Cx` and resumes the script. `Cx` never enters Lua. That is also why `Cx::record` being `pub(crate)` did no harm: `cx.observe` takes a future, so the script's read closure runs on its own coroutine inside that future, and a replay skips it.
- **`Registered` is the seam.** It is public and object-safe, so a script-backed workflow slots into `Runtime::new` with no change to the library.
- **Rust keeps everything that touches hostile text.** Events are parsed by the existing Rust types, and `src/github.rs` projects GitHub's JSON down to validated fields and tainted text.

## Side by side

`almanacs/renovate/src/workflows/build_failures.rs` against `workflows/build_failures.fnl`, counting lines of code without comments or blanks, and leaving out `scan`, which is not ported:

| Part | Rust | Fennel |
|------|-----:|-------:|
| Types, constants, prompt, agent | 67 | 7 |
| `needs_attention` | 4 | 5 |
| `read_pull`, `read_logs` | 69 | 20 |
| Workflow declaration and `gather` | 56 | 40 |
| Policy | 57 | 22 |
| **Total** | **253** | **94** |

The total flatters Fennel. `src/github.rs` is 117 more lines that exist only so `read_pull` can be 20, and it is per-almanac work, so the honest figure for this workflow is 211 against 253. The 60 lines of types did not just go away either: they were also documentation.

Policy is where Fennel is plainly better. The Rust:

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

The Fennel:

```fennel
{:kind :repaired : repo : number : branch : head : patch}
(case (patches.check patch head)
  checked [(effect.push {: repo
                         :pull number
                         : branch
                         :message "fix(deps): repair the build after this update"
                         :hold trust-context
                         :patch checked})]
  _ (stop-and-ask repo head "A repair was refused by the patch policy"
                  (taint.map patch :diff)))
```

`gather` is where it is not. Rust's early `return`s keep it a flat list of guards. Fennel has no `return`, so the same guards nest, and `repair` had to be split out so that it stayed readable:

```rust
if !pull.found || pull.conflicted || pull.running {
    return Ok(Facts::Nothing);
}
```

```fennel
(if (or (not pull.found) pull.conflicted pull.running)
    {:kind :nothing}

    (not pull.failed)
    (if pull.trust-pending {:kind :passed : repo : head} {:kind :nothing})

    (let [attempts (cx.attempts)]
      (if (>= attempts.used attempt-cap)
          {:kind :exhausted : repo : head :asked attempts.asked}
          (let [update (taint.map pull.text :update-of)
                ages (cx.get :ages)]
            (case (cx.cooled min-release-age (ages.newest update))
              cooled (repair cx github event pull cooled)
              _ {:kind :cooling})))))
```

**Which is easier to understand?** This is my judgement from writing both, and no one else has read them. For reading top to bottom, Fennel wins by a modest margin, and only for someone comfortable with Lisp: there is no `.clone()`, no `.await?`, no `Ok(...)`, and the policy reads as a decision table. For understanding what the code is allowed to do, or changing it safely, Rust wins, and that is the understanding that matters in a factory:

- In Rust, `Facts` is one list of every way a run can end. In Fennel you reconstruct that list by reading `gather` and policy.
- In Rust, a signature explains a rule: `repair` taking `&Cooled` says no agent reads an uncooled update. In Fennel the rule lives in the host, so a reader of the workflow file cannot see why `:cooled` is there or what `taint.map` will accept.
- A Fennel reader needs two extra models that Rust does not have: which VM a line runs in, and the closed `cx`, `taint`, `effect`, and `patches` tables.
- Fennel-only syntax costs something for anyone who does not already read it: `{: repo}`, `#(...)`, and that a bare `case` pattern never matches `nil`.

So Fennel is not clearly better. It is better at the part that was already easy to review, and no better at the part that needed help.

## What each safety rule became

All 58 cases in `crates/fails/tests/ui/fail/` are accounted for, and `tests/rules.rs` fails if one is missing from its lists. Every row marked Load or Run is exercised by that test.

- **Load:** refused when the workflow loads, before any event. The binary fails `check`, so this is close to a compile error.
- **Run:** refused in a run, before the effect or agent call. It fails only when that line executes, so a branch no test reaches stays unchecked.
- **Rust:** that part is still Rust, so it still fails to compile.
- **Lost:** nothing checks this, and the table says what still holds.
- **Not applicable:** the mistake has no form in a script.

| Case | Status | What happens in Fennel |
|------|--------|------------------------|
| `call_model_without_cx` | Load | No harness is reachable. Naming one is an unknown identifier |
| `cannot_read_without_prompt` | Run | `:prompt` must be a table |
| `capturing_narrow` | Load | A narrower compiles alone, so a captured name is unknown |
| `checked_cannot_be_deserialized` | Run | `effect.push` takes only the `patches.check` userdata |
| `checked_cannot_be_forged` | Run | Same |
| `cooled_cannot_be_copied` | Run | A proof cannot be recorded or leave its VM. A script can reuse the handle within a run, as Rust can reuse `&Cooled` |
| `cooled_cannot_be_deserialized` | Run | `:cooled` must be the proof userdata |
| `cooled_cannot_be_forged` | Run | Same |
| `effect_has_no_merge` | Load | `effect` is a closed table, and the reader checks every `effect.*` call. There is no constructor at run time either |
| `file_title_is_a_string` | Run, weakened | Tainted content is refused. Any clean string is accepted, so `Title` as a distinct type is lost |
| `fixer_needs_cooled` | Load | The reader requires `:cooled` on `cx.repair`, and a run checks the proof |
| `fixer_output_is_not_clean` | Run | A worker's input may hold no tainted value |
| `format_into_string` | Run | `..` raises an error. `string.format` prints `<userdata>` |
| `hostile_effect_title` | Run, weakened | Tainted content is refused. The requirement that a context be a literal is lost |
| `key_on_untrusted_text` | Load | The host knows which event fields are tainted |
| `key_unknown_field` | Load | Same |
| `manual_clean_impl` | Not applicable | Scripts declare no types. The host itself hand-writes this seal for `CleanValue`, because a script's value has no static type |
| `missing_decide` | Load | Required options are checked |
| `missing_prompt_field` | Load | When the `:prompt` table is literal, otherwise Run |
| `narrow_to_number` | Run | A narrower must return a bool |
| `narrow_to_slug` | Run | Same |
| `narrow_to_string` | Run | Same |
| `observe_raw_string` | Lost | `cx.observe` will return a plain string. It holds only because no read produces hostile text as a string |
| `patch_is_not_clean` | Run | A worker's input may hold no tainted value |
| `payload_field_rejects_string` | Rust | Events are parsed by Rust |
| `payload_text_is_tainted` | Rust | Same |
| `plain_string_to_worker` | Run, weakened | Free text is refused, and slug-shaped text passes, because there is no declared type to check against |
| `policy_cannot_take_cx` | Load | The reader follows policy through helpers. If that is evaded, a pure VM has no `cx` |
| `prompt_field_never_used` | Load | The template is checked against `:vars` |
| `prompt_is_not_a_runtime_value` | Load | Declarations must be literal |
| `prompt_malformed_variable` | Load | Same grammar as `#[prompt]` |
| `prompt_missing_file` | Load | Prompts are built into the binary |
| `prompt_outside_prompts_dir` | Load | Refused by path |
| `prompt_rejects_hostile_text` | Run | Tainted content is refused |
| `prompt_rejects_plain_string` | Run, weakened | Free text is refused, and slug-shaped text passes |
| `prompt_typo_in_variable` | Load | The template is checked against `:vars` |
| `prompt_unclosed_variable` | Load | Same grammar as `#[prompt]` |
| `prompt_variable_has_no_field` | Load | The template is checked against `:vars` |
| `raw_text_to_worker` | Run | A worker's input may hold no tainted value |
| `read_private_field` | Run | A userdata has no fields |
| `reader_cannot_act` | Load | The verb is checked against the profile |
| `reader_output_to_worker` | Run | A worker's input may hold no tainted value |
| `scan_returns_the_wrong_event` | Not ported | `scan` is not ported. It would become Run |
| `scan_without_attribute` | Not ported | Same. The reader would make the attribute unnecessary |
| `string_in_clean` | Not applicable | Scripts declare no types |
| `string_in_observed` | Rust | Events are declared in Rust |
| `subject_on_untrusted_text` | Load | The host knows which event fields are tainted |
| `title_from_string` | Lost | Any clean string makes a title. It holds only because no Lua string is hostile |
| `title_from_untrusted` | Run | Tainted content is refused |
| `title_names_a_variable` | Not applicable | Fennel has no format string to smuggle a name through |
| `unknown_effect` | Load | `:may` is checked against the closed set |
| `unknown_option` | Load | Unknown options are refused |
| `untrusted_in_clean` | Not applicable | Scripts declare no types |
| `untrusted_patch_to_push` | Run | `effect.push` takes only the `patches.check` userdata |
| `worker_output_must_be_clean` | Run | Checked when the reply is decoded, after the agent has run and been paid for |
| `workflow_must_be_async` | Not applicable | Every `gather` can yield |
| `wrong_event_type` | Run, weakened | Reading a field an event lacks is an error. A wrong type elsewhere goes unnoticed |
| `wrong_prompt_for_agent` | Load | A call names no prompt, and wrong variables are refused |

In total: 22 Load, 24 Run (5 of them weakened), 3 still Rust, 2 Lost, 5 not applicable, and 2 not ported. Nothing that protects against hostile text was lost outright. The two Lost rows and the weakened rows all rest on one invariant, which is the next section.

Some guarantees are not in that directory, and they are lost too:

- **Exhaustive `match`.** A fact policy has no pattern for fails the run, and a misspelt `:kind` fails the same way. See `a_policy_with_no_matching_pattern_fails_the_run`. Rust refuses this at compile time.
- **Field and argument types.** A misspelt field in a fact reads as `nil`.
- **Refactoring.** Renaming a fact has no compiler behind it.

## How taint works

The rule is that **hostile text is never a Lua string**, so every Lua string in a VM is clean by construction. Hostile content lives in `Tainted`, a userdata with no methods and no metamethods. The whole invariant rests on a short list in `src/value.rs` and `src/github.rs`, and that list is what a reviewer must trust.

Scripts get a closed `taint` table: `wrap`, `concat`, and `tail` stay tainted, `map` takes a host-defined name and never a function, and `narrow` takes the name of a declared narrower. A narrower is the Fennel form of a `fn` passed to `Untrusted::narrow`: it compiles alone so it cannot capture anything, runs in its own VM on the raw text, may return only a bool, and has its errors replaced so it cannot write the text into one.

**Attacks that failed.** `tests/breakout.rs` runs them as raw Lua, not Fennel, because Lua is everything Fennel can compile to and raw Lua skips every load-time check. Each ran against the real sandbox and none produced the text:

- **`tostring`** returns `<userdata>`, with no address, through `_G.tostring` too.
- **String coercion** fails: `..`, `string.len`, `sub`, `upper`, `rep`, `byte`, `gsub`, `find`, `match`, `pack`, `table.concat`, `tonumber`, `math.floor`, and `utf8.*`. `string.format` with `%s` or `%q` gives `<userdata>`, and `%p` is refused.
- **Metamethods** fail: indexing, calling, arithmetic, ordering, `#`, `pairs`, and `ipairs` all raise errors, and `==` compares identity only.
- **`debug`, `load`, `loadstring`, `dofile`, `require`, `io`, `os`, `coroutine`, `collectgarbage`, `getmetatable`, `setmetatable`, `rawget`, `pcall`, `next`, `string.dump`, and `math.random`** are all absent, as globals and through `_G`, which is the sandbox and not the real globals. Fennel's `case` expands to `_G.type`, so `_G` has to exist.
- **The host's own API** fails: `taint.map` and `taint.narrow` refuse a function, a narrower that returns text or raises it gets nowhere, and a value kept in a narrower's VM is gone with that VM.
- **Errors** carry nothing: `error(u)` reaches the host as `Tainted: 0x...`.
- **Binary chunks** are refused.

With the sandbox removed entirely, so that a script has `debug` and the raw globals, walking the userdata's metatable, its upvalues, and the registry still found nothing, because the text is in Rust's heap and no Lua-visible function reads it. That test does not cover crafted bytecode, which is why refusing binary chunks matters.

**Holes found.** Three turned up, and each is closed here, with a test:

1. **Reshaping turned a fixed narrower into a reader. Demonstrated.** With eight narrowers that each read one bit of the first byte, and `taint.tail` to shift the text, a script recovers all 28 bytes of hostile text into a clean Lua string, then an effect. `Tainted::Reshaped` closes it: a narrower refuses text a script reshaped. See `shifted_text_does_not_turn_a_fixed_narrower_into_a_reader`.
2. **The run log's markers could relabel content. Found by reasoning, not demonstrated.** A script writing `$kind` and `$untrusted` keys looked able to rebuild reshaped text as something a narrower accepts. Keys starting with `$` are refused when a value leaves Lua. See `a_script_cannot_relabel_tainted_content_with_the_logs_markers`.
3. **A pattern over hostile text could not be stopped. Measured.** The instruction limit counts VM instructions, and a pattern match is one C call. `.-.-.-x` on 500 bytes took 7 to 10 seconds, and on 1,000 bytes 114 seconds, roughly the fourth power of the size. Logs go up to 256 KiB. A narrower now gets literal search only.

All three need a mistaken or malicious workflow author, not hostile data alone: hostile text cannot write a narrower. They still count, because the first is exactly the way out of `Untrusted` the rules forbid, and it was in code written carefully for this purpose. Every host function is a possible way out. That is the real cost of doing taint at run time.

**What still leaks, on purpose.** A narrower leaks what it is written to leak: one bit per narrower per observed value, the same bound as a Rust `fn`, and visible in review. A quarantined agent's answer can also be narrowed, so a script could ask an agent to echo a chosen byte. That is true in Rust too.

**Rust has the same holes, and more.** `rust_join_and_narrow_read_the_text_too` proves the first attack against the real `Untrusted`, compiling with no `unsafe`: `join` carries a chosen index in the clean prefix, and a non-capturing `fn` parses it. `Untrusted` also derives `Serialize`, so `serde_json::to_value` is a public way out, which `update::opened` uses on purpose and which principle 3 says should not exist. In this respect a script is held more tightly than a Rust author is. A correction from review: `join` is one route, not the cause. A Rust `narrow` function gets the raw text, so it can choose a byte from a `static`, or write the text to a file. Rust trusts that function and its review, which `crates/fails/tests/ui/pass/hole_narrow_reads_by_index.rs` now records.

## Policy purity and replay

Policy runs in a pure VM with no `io`, no `os`, no clock, no `cx`, and no provider. Naming the clock is a load error, and `(. _G :cx)`, which gets past the reader, finds nothing at run time. The VM is new for every call, so state a policy keeps does not reach its next call, and nothing `gather` keeps reaches policy except the facts. See `tests/determinism.rs`. **This is stronger than Rust**, where `policy` is an ordinary function that can call `SystemTime::now()` or read a file, and only its signature keeps `Cx` out.

Reads are stricter too. A read outside `cx.observe` is refused, so a replay can always repeat it, and a step inside one is refused because a replay skips it. In Rust, nothing stops `client.pull()` outside `cx.observe`. `cx.cooled` takes an opaque read the host performs, so a script cannot supply a publish time, while `cx.cooled(age, async { Ok(Published::At(old)) })` compiles in Rust.

Replay uses `Runtime::replay` unchanged. Recorded values return by the same path as the first run, so a first run and a replay see the same thing. A replay of a repair changes nothing and does not call the model. See `a_replay_repeats_the_decision_without_calling_anything_again`.

Lua is less deterministic than it looks, and these had to be closed by hand:

- **Iteration order.** Hashes are seeded per VM, and the same table built the same way iterated in 2 different orders across 50 VMs. `pairs` is replaced with a sorted walk, and `next` is removed.
- **Addresses.** `tostring` of a table and `string.format` with `%s` or `%p` print addresses, so both are replaced. The replacement has to change the real `string` table, because `("x"):format()` reaches it through the string metatable.
- **`math.random`, `collectgarbage`, weak tables, and finalizers** are removed, the last two by removing `setmetatable`.

One gap remains. `error(u)` puts an address in a run's error text. That reveals no content and does not affect effects, but a failed run's message can differ between replays.

## The manifest

**Yes, it is known without running anything.** `analyze.fnl` reads the source with Fennel's reader and walks the tree as data. `FennelWorkflow::load` never executes the chunk, and `a_manifest_is_derived_without_running_the_workflow` proves it with code that raises an error at every level, including the top. `the_ported_manifest_matches_the_rust_one_except_for_scan` compares the output with `BuildFailures.manifest()` field by field, down to the requirement type names, and only `scans` differs.

This is the best argument for a Lisp, and it is more than the Rust does: the reader follows a workflow's calls through the file's own helpers, so `a_step_in_a_helper_reaches_the_manifest` finds a `cx.get` two calls deep. `#[workflow]`'s `Uses` visitor sees one function body and says so in its comment.

It holds only because some things are refused. Declarations must be literal. The target of `cx.get`, `cx.observe`, and an agent step must be a literal. The top level may only declare. `macro`, `macros`, `eval-compiler`, `lua`, `require`, and `include` are refused, because anything that generates code would make the tree differ from the program. **So the feature Fennel is known for, user macros, had to be turned off.** The macros that remain are 5 lines, and what paid off was code as data, not macros. Plain Lua would need a parser to do the same.

The reader cannot see everything, such as `(let [get cx.get] (get :github))`. It does not have to, because a run refuses any provider, agent, or observation the manifest does not list, so a manifest can never understate a workflow. The cost is that such a workflow fails in a run instead of at load.

## Costs

- **Trusted code.** 2,296 lines of host: 1,931 of Rust and 365 of Fennel and Lua. They stand in for 749 lines of `taint.rs` and `crates/macros`, and every one of them is on the security boundary. `src/github.rs` and each new event, map, or read grow with the almanac, and they are Rust, so an author still has to write Rust.
- **Time.** Measured in a release build, loading takes about 40 ms once, and a VM with its chunk takes about 100 µs. That means about 100 µs per `when`, and two VMs per run, which is nothing next to a GitHub call.
- **Blocking.** A script runs on the async executor. An endless loop holds a worker thread for about 430 ms before the instruction limit stops it. A real version would run scripts off the executor.
- **Feedback.** Agents write these workflows too. A compile error reaches them on every build. About half of the rules here reach them only when a test drives that exact branch.
- **Hidden APIs.** The host needs `Title::__compose` and a hand-written `__private::Sealed`, because `title!` and `clean!` assume literals and static types.
- **`intern`.** `Cx` and `Effect` take `&'static str`, so strings from a script are interned for the life of the process. That is bounded by what a script can build, and a workflow that passed `event.sha` as a context would grow it without limit.

## Recommendation

**Drop it.** Keep the spike as the record.

The owner's question allowed for weaker guarantees, so the case is not that Fennel is unsafe. Nothing protecting against hostile text was lost, and purity, read recording, and manifests came out stronger than Rust. It is dropped because the trade does not pay:

1. **The readability gain is real and small.** It is concentrated in policy, 35 lines in this workflow, and much of the rest moved into the host rather than going away. `gather`, the part with the sequencing, got no easier, and `Facts` as a single list of outcomes is gone.
2. **Understanding moved out of the workflow file.** A reader now has to know the VM split and the host's closed tables, and the rules live in 2,296 lines of trusted host instead of signatures. Almanac is built on rules being visible in types, and the vision says the compiler enforces the rules that matter most. Taking this up means revisiting that thesis and principle 3, not just adding a language.
3. **Rust does not go away.** Every event, read, and piece of parsing that touches hostile text stays Rust, so an almanac needs both languages, and the host cost is only repaid over many workflows. There are two today.

**Adopting it for one layer** was the alternative, and policy plus declaration is the only layer worth considering. I would still decline: it adds a VM, a vendored compiler, and a bridge to save roughly 35 lines per workflow, and it gives up exhaustive `match` exactly where it earns the most, on `Facts`.

Reconsider if workflows grow past a dozen or so, or if people who will not write Rust need to change policy. Luau is also worth reading in the Lua spike for this reason: it has a type checker, and its suite has a `decide_branch_is_missing` case. Fennel has no static type layer, so exhaustiveness cannot be recovered here.

**Worth taking back into the Rust, whatever is decided:**

1. **Decide what bounds a `narrow` function,** and what `Serialize` on `Untrusted` should mean under principle 3. A `fn` stops capture, not misuse: it reads the raw text and is trusted code. `main` records this as a known hole with a limit deferred to M5, because closing `join` alone would leave the `static` route open.
2. **Stop `Cx::cooled` taking a caller's `Published`,** so a publish time cannot be supplied by a workflow.
3. **Make `#[workflow]` follow helpers,** or at least fail when a step is out of its sight.
4. **Enforce more of principle 7:** a read outside `cx.observe` and a clock in `policy` both compile today.

## Not finished

- **`scan` is not ported,** so backfill and catch-up do not run, and the manifest says `scans: false`. Its two compile-fail rows are unproven.
- **Parts of `Cx` are not ported:** `links`, `cx.screen`, `cx.record_fact`, and `Halt::Stop`. `build_failures` uses none of them.
- **`security_fixes` is not ported,** so this is evidence from one workflow.
- **Tests use `MemoryStore`.** The PostgreSQL store was not exercised, and this was never run as a daemon.
- **The readability verdict is one author's judgement.** Nobody else has read the two versions, and I did not test whether agents write Fennel workflows more or less reliably.
- **The Lua spike was not compared in depth.** It had no findings when these were written, so what is said about it comes from its file list.
- **Fennel's signature was not verified.** `vendor/fennel-1.6.1.lua` is from fennel-lang.org with sha256 `c3d45602041e7d8ef8a212563573df040c48a85c648a29fb4597ebed4bc38ec2`, and its detached signature was downloaded but not checked.
- **Binary size was not measured.**
