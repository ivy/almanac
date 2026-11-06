# 1. The agent loop runs outside the sandbox

## Status

Accepted.

## Context

A `Fixer` reads hostile logs and code, so it must hold no secrets and have no general network access (principle 5, R-19, R-23). It still has to reach a model and build the code. The sandbox runs on rootless Podman today and may move to a hosted service such as Daytona, so nothing may assume a host path or a bind mount (R-52).

## Decision

The agent loop runs on the host with no tools of its own. Its only tools come from an MCP server that Almanac supplies, and each one is a call on the `Sandbox` provider. The sandbox has no network at all, so it reaches neither the internet nor the model.

```text
host process                                   sandbox: no network, no mounts, no capabilities
  claude -p --bare --tools ""  ──MCP──▶  Sandbox::exec / upload / download  ──▶  checkout, build
     │ holds the model credential
     ▼
   model
```

The harness runs as a subprocess with `--bare --tools "" --strict-mcp-config --mcp-config <file> --allowedTools <sandbox tools> --no-session-persistence`, an empty working directory, and an environment built from nothing.

## Evidence

`spikes/sandbox/run.sh` runs this design end to end against a local stub of the Messages API, which replays scripted tool calls and records every request. It needs no credential and spends nothing. All seven checks pass with `claude` 2.1.319 and Podman 5.8.9:

| Check | Observed |
|---|---|
| The model is offered only sandbox tools | `tools` in every request held exactly `mcp__sandbox__exec` and `mcp__sandbox__write_file` |
| A call to a host tool does nothing | Scripted `Bash`, `Read`, and `WebFetch` calls returned `No such tool available`, and a canary file was not created |
| No operator memory reaches the model | Markers in `~/.claude/CLAUDE.md` and in a `CLAUDE.md` in the working directory appeared in no request |
| Host credentials are unreadable | `cat` of a planted credential path gave `No such file or directory`, and no credential-like variable was set |
| No egress | `wget` to `1.1.1.1` gave `Network unreachable`, and the model's hostname did not resolve |
| An agent can edit and build offline | A scripted edit fixed a compile error, and `cargo build --offline --locked` produced a binary that printed `42` |
| The result can be read out | `podman cp` retrieved the edited file |

## How a build gets its dependencies

A separate, agent-free step fetches them first. It runs in its own sandbox with network access, holds no secrets, and uses only commands that run no package code: `cargo fetch --locked`, `go mod download`, or `pnpm fetch --ignore-scripts`. The cache is downloaded from that sandbox and uploaded to the agent's. The spike does this for a real crate.

This keeps fetching deterministic and before inference. A hostile lockfile can make the fetch download from a server of its choosing, but that sandbox has nothing to steal, and whatever it fetches runs only in the agent's sandbox, which has no network.

A repair that needs a dependency absent from the lockfile cannot build. That is reported as `needs-person`, not worked around.

## What each profile receives

| | `Quarantined` | `Fixer` | `Worker` |
|---|---|---|---|
| Input | Prompt and tainted text | Prompt, tainted logs, a checkout, a dependency cache | Prompt and `Clean` values only |
| Tools | None | `exec`, `read_file`, `write_file` in the sandbox | What its grant names |
| Sandbox | None needed: it has no tools | One per run, destroyed after | One per run |
| Network | None | None | As granted |
| Secrets | None | None | As granted |
| Output | Tainted, narrowed by policy | A patch, tainted, checked by the patch policy | Typed, `Clean` |

The model credential stays with the harness process in every case. A sandbox never holds it.

## `Sandbox` on each backend

| Operation | Podman | Daytona |
|---|---|---|
| Create | `podman create --network=none --cap-drop=all --security-opt=no-new-privileges --user 1000:1000`, then `podman start` | `POST /sandbox` with `networkBlockAll: true` |
| Create with egress, for fetching | `podman create` on the default network | `POST /sandbox` with `domainAllowList` |
| Execute | `podman exec` | `POST /process/execute` on the toolbox API |
| Upload | `podman cp` into the container | `POST /files/upload-v2` or `/files/bulk-upload` |
| Download | `podman cp` out of the container | `GET /files/download` or `POST /files/bulk-download` |
| Destroy | `podman rm -f` | `DELETE /sandbox/{id}` |

Adopting Daytona needs one new `Sandbox` implementation, generated from `specs/daytona/`, and a choice of sandbox image with the toolchains preinstalled. The harness, MCP server, and workflows do not change, because no credential or model traffic enters the sandbox. Per-sandbox network rules need Daytona Tier 3 or above, so confirm the account's tier first.

## Options rejected

**`claude` inside the sandbox, through a credential-injecting proxy.** A proxy on a Podman `--internal` network blocked the internet as intended, but any process in the container reached it with no credential of its own. A hostile build script could therefore spend on the operator's account or carry data out through model requests. The harness would also sit where hostile code can tamper with it. On Daytona the proxy would need a public address, which adds a service to run and protect.

**`claude -p` on the host with its built-in tools.** Those tools act on the host, so one successful injection reaches the operator's files. Restricting them with permission rules leaves safety in configuration, whereas `--tools ""` removes them from the request entirely.

**Copying credentials into the sandbox.** Forbidden by principle 5.

**Our own agent loop calling the model API directly.** This would remove the subprocess, and `Harness` leaves it open. It means owning context handling and retries that a harness already provides, so it is deferred until the subprocess proves inadequate.

**`bwrap` or `systemd-nspawn`.** Both isolate well on one host, but neither maps to a hosted provider, and Podman already gives the same boundary.

## Consequences

- `--bare` accepts only `ANTHROPIC_API_KEY` or a key helper, so a deployment needs an API key. It cannot use a subscription login.
- Without `--bare`, `--setting-sources=` with `--disable-slash-commands` passed the same checks except one: requests also carried a `DeferredToolPlaceholder` tool. Treat that mode as for local evaluation only.
- The `claude` process runs on the host and parses hostile text. A bug in it is the remaining path from hostile text to the host, so run it with an empty working directory and a minimal environment, and revisit under M5.
- The MCP server is part of the trust boundary and must expose nothing but sandbox calls.
- Every `Sandbox` needs a wall-clock limit and resource limits, because hostile code runs there by design.
