# Safety rules in Go

The Rust almanac proves its safety rules with compile-fail cases in [`crates/fails/tests/ui/`](../crates/fails/tests/ui/): each file is a workflow, or a part of one, that `rustc` must refuse. This page says what each case became in the Go almanac, when it is refused, and which Go spec proves it. [ADR 2](adrs/0002-go-and-a-workflow-model.md) explains why: a workflow is a model built by a Go DSL, so a rule is now a Go type error, a finding of the `hostilevet` analyzer, or a refusal by the checker or the vocabulary when the binary starts.

`docs/safety_rules_test.go` fails if a case in `crates/fails/tests/ui/` has no row here, if a row names a spec file that does not exist or does not mention the case, or if a row that fires at run time or is weakened does not say why.

## What a case can become

- **type error**: the Go compiler refuses it, so `make go-lint` does.
- **hostilevet**: [`tools/hostilevet`](../tools/hostilevet/taint/taint.go) refuses it in `make go-lint`.
- **checker**: [`check.Check`](../check/check.go) refuses the model with a code from [`docs/errors-go/`](errors-go/). Every almanac runs it on every model before it starts, and a workflow's spec runs it in `make go-check`.
- **start-up**: the vocabulary refuses the part, or the prompt fails to parse, when the binary starts, before any event.
- **run time**: refused when a run reaches it. Each such row says why it is not refused earlier.
- **weakened**: the specific check is gone. The row says what still holds.
- **not applicable**: Go has nothing the case could be written in.

"Fires" is the earliest point the usual way of writing the case is refused: `build`, `start-up`, `run time`, or `never` for a case that cannot be written.

## Compile-fail cases

| Case | Becomes | Fires | Proved by | Note |
|---|---|---|---|---|
| `call_model_without_cx` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | `agent.Pass` has an unexported field, so only package agent issues one, through `agent.Call.Run`, which the run's context calls. A zero `Pass` compiles, and `harness/claude` refuses it. |
| `cannot_read_without_prompt` | start-up | start-up | [`vocab/safety_test.go`](../vocab/safety_test.go) | A prompt is part of an agent's declaration, so no call can leave it out. The vocabulary refuses an agent whose prompt was never parsed. |
| `capturing_narrow` | hostilevet | build | [`narrowing.go`](../tools/hostilevet/taint/testdata/narrowing/narrowing.go) | `hostile.Narrow` takes a declared function, never a function literal, a function value or a bound method. |
| `checked_cannot_be_deserialized` | weakened | build | [`effect/safety_test.go`](../effect/safety_test.go) | Decoding straight into a `patch.Checked` is refused by staticcheck (SA9005) in `make go-lint`. A record holding one decodes it as the zero value, and `effect.Plan` refuses that at run time, so nothing is pushed: Go cannot make a field undecodable. A model never holds one: the engine keeps the checked patch in memory. |
| `checked_cannot_be_forged` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | `patch.Checked` has only unexported fields, so only `patch.Policy.Check` makes one. |
| `cooled_cannot_be_copied` | weakened | never | [`cooled/safety_test.go`](../cooled/safety_test.go) | Go copies any value. A copy proves what the check proved, about the same release, and nothing makes a proof a check did not. A model never holds the proof: the engine keeps it in memory for the fixer step. |
| `cooled_cannot_be_deserialized` | weakened | build | [`cooled/safety_test.go`](../cooled/safety_test.go) | As `checked_cannot_be_deserialized`: staticcheck refuses decoding straight into a `cooled.Cooled`, a record holding one decodes it as the zero value, and a fixer refuses that at run time. |
| `cooled_cannot_be_forged` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | `cooled.Cooled` has only an unexported field, so only `cooled.Check` and `cooled.Measure` make one. |
| `effect_has_no_merge` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go), [`effect/effect_test.go`](../effect/effect_test.go) | There is no `effect.Merge`. `effect.Effect` has an unexported method, so no other package can add an effect, and a stored kind named `Merge` does not decode. |
| `file_title_is_a_string` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | An issue's title is a `hostile.Title`, and the DSL takes a `hostile.Format` made only from a string constant. |
| `fixer_needs_cooled` | checker | start-up | [`check/safety_test.go`](../check/safety_test.go), [`dsl/safety_test.go`](../dsl/safety_test.go) | `almanac::model::fixer_before_measure` and `almanac::model::fixer_uncooled` refuse a fixer a run could reach before the release has cooled. In Go code, `Fixer.Repair` takes the proof as an argument and refuses a zero one. |
| `fixer_output_is_not_clean` | checker | start-up | [`check/safety_test.go`](../check/safety_test.go), [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go) | A fixer's answer is a hostile fact, and `almanac::model::agent_input_hostile` refuses one as a worker's input. hostilevet refuses the same in Go code. |
| `format_into_string` | hostilevet | build | [`format.go`](../tools/hostilevet/taint/testdata/format/format.go) | fmt and log refuse a hostile value, or one that holds one. At run time a `hostile.Of` prints, and slog logs it, as `hostile(..)` whatever the verb. |
| `hostile_effect_title` | weakened | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | A status context is any Go string, not only a literal. What holds: a hostile fact cannot be one (a type error), hostile text is never a plain string outside the boundary packages, and a model is built before any event and never fills a context from facts. |
| `key_on_untrusted_text` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go), [`check/safety_test.go`](../check/safety_test.go) | `dsl.Key` takes clean handles only. A model edited to key on hostile text is refused with `almanac::model::hostile_fact`. |
| `key_unknown_field` | checker | start-up | [`check/safety_test.go`](../check/safety_test.go) | A field is named by its JSON name, so the compiler cannot see a wrong one, and `almanac::model::unknown_name` refuses it. |
| `manual_clean_impl` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go), [`closed.go`](../tools/hostilevet/taint/testdata/closed/closed.go) | `hostile.Clean` is a type set, so nothing implements it. Whether a type is clean is decided by its shape, never declared, and hostilevet refuses a closed type with an exported field. |
| `missing_decide` | checker | start-up | [`check/safety_test.go`](../check/safety_test.go) | `almanac::model::no_ending`. |
| `missing_prompt_field` | checker | start-up | [`check/safety_test.go`](../check/safety_test.go) | `almanac::model::prompt_vars` refuses a prompt variable the model does not fill. |
| `narrow_to_number` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | `hostile.Classify` answers only a closed set, and a number is not one. |
| `narrow_to_slug` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | As `narrow_to_number`: validated text is not a closed set. |
| `narrow_to_string` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | `hostile.Narrow` answers a bool, and a string is not a closed set. |
| `observe_raw_string` | hostilevet | build | [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go), [`vocab/safety_test.go`](../vocab/safety_test.go) | An observer whose reading could hold free text is refused at build, and by the vocabulary at start-up. `workflow.Observe` refuses one before it reads, where the type is known only at run time. |
| `patch_is_not_clean` | hostilevet | build | [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go), [`vocab/safety_test.go`](../vocab/safety_test.go) | A patch is only ever hostile: an observer reading one as clean, and a worker given one, are refused. In a model the patch is a hostile fact, which the checker refuses as a worker's input. |
| `payload_field_rejects_string` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | `wire.Field` reads only types in `hostile.Clean`. |
| `payload_text_is_tainted` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | `Payload.Text` returns a `hostile.Of[string]`. |
| `plain_string_to_worker` | hostilevet | build | [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go), [`vocab/safety_test.go`](../vocab/safety_test.go) | A worker's input whose type could hold free text is refused at build. No fact in a model is a plain string, because every part that produces one refuses it. An input typed as an interface is checked when the call is made. |
| `policy_cannot_take_cx` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | A decide holds no code: its rules name predicates, each a function of facts alone. A predicate that takes the run's context does not register. |
| `prompt_field_never_used` | start-up | start-up | [`agent/safety_test.go`](../agent/safety_test.go) | Checked when the prompt is parsed. An almanac declares its prompts with `agent.MustPrompt`, so a bad one stops the binary as it starts. |
| `prompt_is_not_a_runtime_value` | weakened | start-up | [`agent/safety_test.go`](../agent/safety_test.go) | A prompt's name can be any expression. What holds: it is read from the embedded `prompts/` when the agent is declared, before any event, and a model cannot choose a prompt. |
| `prompt_malformed_variable` | start-up | start-up | [`agent/safety_test.go`](../agent/safety_test.go) | Checked when the prompt is parsed. |
| `prompt_missing_file` | start-up | start-up | [`agent/safety_test.go`](../agent/safety_test.go) | Checked when the prompt is loaded. |
| `prompt_outside_prompts_dir` | start-up | start-up | [`agent/safety_test.go`](../agent/safety_test.go) | A prompt is read from an `fs.FS`, the embedded `prompts/`, which holds no path outside itself. |
| `prompt_rejects_hostile_text` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go), [`check/safety_test.go`](../check/safety_test.go), [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go) | `dsl.Var` takes clean handles only. An edited model is refused with `almanac::model::prompt_vars`, and hostilevet refuses hostile `agent.Vars` in Go code. |
| `prompt_rejects_plain_string` | hostilevet | build | [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go), [`check/safety_test.go`](../check/safety_test.go) | hostilevet refuses a string in `agent.Vars`. In a model, `almanac::model::prompt_vars` refuses a variable filled from a fact that could hold text. |
| `prompt_typo_in_variable` | start-up | start-up | [`agent/safety_test.go`](../agent/safety_test.go), [`check/safety_test.go`](../check/safety_test.go) | The prompt's parse refuses a variable it does not declare, and the checker a model filling one the prompt does not have. |
| `prompt_unclosed_variable` | start-up | start-up | [`agent/safety_test.go`](../agent/safety_test.go) | Checked when the prompt is parsed. |
| `prompt_variable_has_no_field` | start-up | start-up | [`agent/safety_test.go`](../agent/safety_test.go) | Checked when the prompt is parsed. |
| `raw_text_to_worker` | checker | start-up | [`check/safety_test.go`](../check/safety_test.go), [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go) | `almanac::model::agent_input_hostile`. hostilevet refuses the same in Go code. |
| `reader_cannot_act` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | `dsl.Act` takes a worker part, and `agent.Quarantined` has no `Act`. |
| `reader_output_to_worker` | checker | start-up | [`check/safety_test.go`](../check/safety_test.go), [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go) | A reader's answer is a hostile fact. `almanac::model::agent_input_hostile`. |
| `read_private_field` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | `hostile.Of` has no exported field and no accessor. |
| `scan_returns_the_wrong_event` | weakened | run time | [`engine/safety_test.go`](../engine/safety_test.go) | A scan returns untyped `workflow.Found`. The engine stamps its workflow's own kind on everything the scan finds, so a found event reaches only that workflow, which refuses one of another shape as malformed when it identifies it, before any run starts. It is not refused earlier because a scan is not typed by its event; typing it changes `vocab.Scan`, which the Renovate vocabulary is written against. |
| `scan_without_attribute` | checker | start-up | [`check/safety_test.go`](../check/safety_test.go) | A scan is a registered part that declares its needs, which `workflow.Validate` checks at start-up. A model can only name a registered scan, and `almanac::model::unknown_name` refuses any other. |
| `string_in_clean` | hostilevet | build | [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go), [`vocab/safety_test.go`](../vocab/safety_test.go) | A clean type is one whose shape holds no free text. hostilevet refuses a worker's answer or a recorded fact that holds a string, and the vocabulary refuses the worker at start-up. |
| `string_in_observed` | hostilevet | build | [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go), [`vocab/safety_test.go`](../vocab/safety_test.go) | hostilevet refuses an event type, one whose pointer is a `wire.Event`, that holds a plain string. The vocabulary refuses it at start-up too. |
| `subject_on_untrusted_text` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go), [`check/safety_test.go`](../check/safety_test.go) | `dsl.RepoSubject` takes a clean slug. An edited model is refused with `almanac::model::hostile_fact`. |
| `title_from_string` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go), [`title.go`](../tools/hostilevet/taint/testdata/title/title.go) | A DSL title takes clean handles, and `hostile.Titlef` a constant format; hostilevet refuses a string value given to `Titlef`. |
| `title_from_untrusted` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go), [`check/safety_test.go`](../check/safety_test.go), [`title.go`](../tools/hostilevet/taint/testdata/title/title.go) | A hostile fact is not a clean handle. An edited model is refused with `almanac::model::title_not_clean`. |
| `title_names_a_variable` | not applicable | never | [`check/safety_test.go`](../check/safety_test.go) | A Go format string cannot name a variable, so every value is an argument the type rules check. The checker refuses a verb with no value for it. |
| `unknown_effect` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go), [`check/safety_test.go`](../check/safety_test.go) | There is no kind to name. A model edited to hold one is refused with `almanac::model::declaration`. |
| `unknown_option` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go) | A workflow is made of DSL parts, and there is no part that is not declared. |
| `untrusted_in_clean` | hostilevet | build | [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go), [`vocab/safety_test.go`](../vocab/safety_test.go) | As `string_in_clean`, for a hostile value inside a clean type. |
| `untrusted_patch_to_push` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go), [`check/safety_test.go`](../check/safety_test.go) | `dsl.Push` takes the check_patch outcome, and `effect.Push` a `patch.Checked`. An edited model is refused with `almanac::model::push_unchecked`. |
| `worker_output_must_be_clean` | hostilevet | build | [`shapes.go`](../tools/hostilevet/taint/testdata/shapes/shapes.go), [`vocab/safety_test.go`](../vocab/safety_test.go) | hostilevet refuses a worker whose answer type could hold free text, and the vocabulary refuses it at start-up. |
| `workflow_must_be_async` | not applicable | never | | Go has no async functions, and a workflow is a model with no function to mark. The engine walks it with the run's context. |
| `wrong_event_type` | type error | build | [`dsl/safety_test.go`](../dsl/safety_test.go), [`check/safety_test.go`](../check/safety_test.go) | A step's input is typed by the event handle. A handle naming another event's type is refused with `almanac::model::wrong_type`. |
| `wrong_prompt_for_agent` | not applicable | never | [`dsl/safety_test.go`](../dsl/safety_test.go) | No call takes a prompt: it is bound when the agent is declared, and passing one is a type error. The checker fills only that prompt's variables. |

## Pass cases

| Case | Becomes | Proved by | Note |
|---|---|---|---|
| `effect_is_closed` | type error | [`dsl/safety_test.go`](../dsl/safety_test.go) | A type switch over the five effects compiles, and an effect declared outside package effect does not. |
| `hole_narrow_reads_by_index` | still a hole | [`holes.go`](../tools/hostilevet/taint/testdata/holes/holes.go), [`narrowing.go`](../tools/hostilevet/taint/testdata/narrowing/narrowing.go) | A narrowing function told where to look reads the text a bit at a time. |
| `hole_private_seal` | closed | [`dsl/safety_test.go`](../dsl/safety_test.go) | The seal is `hostile/internal/seal`, which Go refuses to import from outside `hostile`. |
| `hole_serialize_to_text` | still a hole | [`holes.go`](../tools/hostilevet/taint/testdata/holes/holes.go) | `json.Marshal` of a hostile value is text holding the value. |
| `ok_checked_patch_to_push` | accepted | [`dsl/safety_test.go`](../dsl/safety_test.go), [`check/safety_test.go`](../check/safety_test.go) | |
| `ok_fixer_with_cooled` | accepted | [`dsl/safety_test.go`](../dsl/safety_test.go), [`check/safety_test.go`](../check/safety_test.go) | |
| `ok_narrow_to_bool` | accepted | [`dsl/safety_test.go`](../dsl/safety_test.go), [`narrowing.go`](../tools/hostilevet/taint/testdata/narrowing/narrowing.go) | |
| `ok_workflow_macro` | accepted | [`dsl/safety_test.go`](../dsl/safety_test.go) | |

## Holes

The ways out of a rule that Go still allows. Each one hostilevet accepts is in [`testdata/holes`](../tools/hostilevet/taint/testdata/holes/holes.go), which fails if hostilevet starts to refuse it.

- **Serialized to text.** `json.Marshal` of a hostile value, then `string(...)`, is text holding the value, as Rust's `hole_serialize_to_text`.
- **Read by index.** A narrowing function told where to look, by a package variable or by text joined in front, reads the text a bit at a time, as Rust's `hole_narrow_reads_by_index`.
- **Passed on.** A narrowing function may hand the raw text to a function hostilevet does not analyze, such as a helper that stores it, or a method such as `sync.Map.Store`.
- **Declared elsewhere.** hostilevet checks the body of a narrowing function only in the package that calls `Narrow` or `Classify` with it.
- **Formatted as `any`.** A hostile value held in an `any` is not refused when it is formatted. It prints as `hostile(..)` at run time.
- **Reflection and unsafe.** `reflect` reaches the unexported field that holds the text.
- **Values typed as interfaces.** hostilevet cannot see what an `any` holds, so a worker's input or a prompt variable typed as one is checked only when the call is made.
- **Copied and decoded proofs.** A `cooled.Cooled` copies, and a record holding a proof decodes it as a zero value, refused at run time (see the rows above).
- **A zero `agent.Pass`.** It compiles; `harness/claude` refuses it when called.
- **A scan's providers.** The engine limits an observer to the providers its registration names, but hands a scan the whole scanner, so a scan can read through a provider it does not declare.
- **Status contexts.** An `effect.Attest` context is any Go string, as `hostile_effect_title` says.
