# Authoring API

The parts of the library a workflow uses. All are in `almanac::prelude`. `cargo doc --open` documents the rest.

## `#[workflow]`

On an async function taking `&Cx` and the event, returning `Result<Facts, Halt>`.

| Attribute | Required | Meaning |
|---|---|---|
| `on` | Yes | The event type that triggers it. |
| `key` | Yes | Correlates events, such as `"{repo}:{branch}"`, from validated fields of the event. One run is active per key. |
| `intent` | Yes | One sentence, shown in the manifest. |
| `may` | Yes | The effects it may produce. Anything else is denied at run time. |
| `runs_per_hour` | Yes | Its budget. |
| `decide` | Yes | The policy: `fn(&Facts) -> Vec<Effect>`. |
| `when` | No | `fn(&Event) -> bool`, to ignore an event before a run starts. |
| `scan` | No | A `#[scan]` function, for catching up. |
| `subjects` | No | What runs concern, such as `[repo(repo)]`, which connects workflows. |
| `links` | No | Other subjects a run relates to. |

## Effects

| Effect | What it does |
|---|---|
| `Attest` | Posts a status on a commit. |
| `Await` | Records that a run is waiting for something, and does nothing else. |
| `File` | Files an issue. Its title is a `Title`, and its body stays `Untrusted`. |
| `Ask` | Asks a person. With `pull: Some(n)` it comments on that pull request, mentioning and assigning them. Without one it files an issue that does the same. |
| `Push` | Commits a checked patch to a pull request's own branch. Only `DEFAULT_POLICY.check` produces what it takes. |
| `Resolve` | Closes tracker work with its evidence attached. `item` is `Item::Named` for the tracker's own name for an entry, or `Item::Filed { key }` for whatever this workflow filed under one of its keys. `evidence` is a `Title`, so only checked values reach it. The GitHub tracker closes only issues labelled `almanac`. |

## `Cx`

| Method | Use |
|---|---|
| `cx.get::<T>()` | A provider. The macro lists it in the manifest. |
| `cx.observe(name, read)` | Runs a read and records it. The result must be an `observed!` type. |
| `cx.record_fact(name, read)` | As `observe`, for a `Clean` value. |
| `cx.agent(SPEC)` | An agent, then `.read`, `.repair`, or `.act` by its profile. |
| `cx.cooled(age, read)` | A `Cooled` proof, if the release is old enough. |
| `cx.age(age, read)` | As `cooled`, also telling a young release from one nothing can date. |
| `cx.attempts()` | How many patches this key has had, and whether a person was asked. |
| `cx.hold(context, repo, sha)` | Posts `pending` straight away, before an agent starts. |
| `cx.screen(...)` | Runs a classifier that can stop the run before any agent starts. |

`Halt::Stop(reason)` ends a run on purpose. `?` on a failed read gives `Halt::Failed`, and the run can be retried.

## Types

| Type | Meaning |
|---|---|
| `Untrusted<T>` | Text an attacker could have written. Use `narrow` to get a `Finite` answer, `map` and `join` to stay tainted, and `zip` to compare two. |
| `Slug`, `Sha` | Validated identifiers. |
| `Title`, `title!` | A title built only from a literal and `Clean` values. |
| `clean!` | Declares a struct or enum that holds no free text. Facts are one of these. |
| `observed!` | Declares a type read from outside. A bare `String` field does not compile. |
| `output!` | Declares what an agent returns. |

## Events

Implement `Event` with a `KIND` and a `parse`. In `parse`, `p.field` reads a clean value, and `p.text`, `p.optional_text`, and `p.text_list` read text as `Untrusted`.

## `#[prompt]`

`#[prompt("file.md")]` on a struct binds a Markdown file in `prompts/`, embedded at compile time. Each `{{ name }}` must match a `Clean` field, and each field must be used.

## Agents

| Constructor | Profile | Can |
|---|---|---|
| `AgentSpec::quarantined` | `Quarantined` | Read tainted input with no tools, and return tainted output. |
| `AgentSpec::fixer` | `Fixer` | Edit a sandboxed checkout with no network, and return a tainted patch. Needs a `Cooled` proof. |
| `AgentSpec::worker` | `Worker` | Take `Clean` input and return `Clean` output. |

## Testing

`almanac::testing` provides `ScriptedHarness`, an agent that replies from a script, and `FakeTracker`. `crates/simforge` is a local GitHub with signed webhooks and failure injection.
