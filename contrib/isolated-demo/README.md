# Isolated synthetic demo

This optional development harness runs the real broker, Renovate daemon, PostgreSQL, Claude CLI, and sandbox against SimForge and StubModel. It never uses a real GitHub account or paid model. Default launch is read-only (`NoAgent`, dry-run); `--fixture-live` enables actual workflow execution and simulated writes only after local fixture validation. This is not production configuration or a web dashboard.

## Build and run

From the repository root, review `mise.toml`, install its locked tools (`mise install --locked`), and ensure a working private Podman runtime. See [runtime/README.md](runtime/README.md) for an optional nested development image and the runtime exceptions used by it. Native Linux Podman is also supported; do not mount a host engine socket into a workspace.

```sh
mise exec -- cargo build --locked --package broker
mise exec -- cargo build --locked --manifest-path almanacs/renovate/Cargo.toml
mise exec -- cargo build --locked --manifest-path contrib/isolated-demo/Cargo.toml
# Explicit setup downloads/builds; launch itself never pulls an image.
podman pull docker.io/library/postgres:17-alpine
podman build -t localhost/almanac-fixer:test images/fixer
mise exec -- bash contrib/isolated-demo/launch-demo.sh
bash contrib/isolated-demo/show-status.sh
bash contrib/isolated-demo/stop-demo.sh
# Wait for stopped, then opt in to local synthetic repairs:
mise exec -- bash contrib/isolated-demo/launch-demo.sh --fixture-live
```

Do not set `CARGO_TARGET_DIR`: binaries use their normal workspace target directories. Fixture-live needs the real supported `claude` CLI; its model endpoint is StubModel, not a paid API. Source, lockfile, controller, wrappers, and offline tests are all included here. Runtime files, logs, generated fixture exports and gate results are kept outside the checkout, by default in `$HOME/.local/state/almanac-isolated-demo`. Set `ALMANAC_DEMO_STATE` to select a different private directory consistently for launch/status/stop/gate. Do not publish that directory. Fixed ports and DB name permit one demo per network namespace.

## Observe a repair

All services bind numeric loopback: controller 8810, synthetic registry 8811, broker 8787, daemon 8788, PostgreSQL 55432, and dynamic forge/model ports. Do not publish these ports. A terminal viewer prints actual process identities, health/readiness, fixture observations, read-only DB counts/cursors/run status, and the native JSON report forced into read-only mode. Viewing never triggers events, repairs, resets, or a gate.

```sh
curl --fail --noproxy '*' -X POST http://127.0.0.1:8810/ci/fail
bash contrib/isolated-demo/show-status.sh
# Wait until /state shows a changed head and a simulated push, then:
curl --fail --noproxy '*' -X POST http://127.0.0.1:8810/ci/success
bash contrib/isolated-demo/show-status.sh
```

The success control returns 409 before a repaired head exists. `/model/hold` and `/model/release` pause/resume StubModel for observing an in-flight repair. Report success only from actual model requests, writes, deliveries, cursor progress and native report, not process health. Registry publish dates are synthetic; advisories are redirected locally but no advisory simulation or security repair is claimed. The fixture checkout has only `src/main.py` and no dependency manifests, so dependency fetching does not contact package servers.

## Safety and lifecycle

The generated `fixtures.env` is parsed as exact allowlisted export records, never sourced. URLs must be plain HTTP on numeric loopback and are checked against the controller's actual `/state`. The synthetic owner/repositories are `fleet/demo` and `fleet/ops`; dummy credentials are `t`, `stub`, `synthetic-demo-webhook-secret`, and PostgreSQL `demo-local`. These are not real credentials. A narrow child environment excludes inherited provider keys, proxies, SSH sockets and agent state. The real Claude subprocess uses its native bare/temporary-HOME isolation. The supervisor still trusts its HOME, PATH, Podman configuration and installed executables: environment filtering is not a network firewall or a hostile-code sandbox. Use a dedicated secret-free runtime and review any changed seed/provider before use.

Launch detaches a supervisor and reports only its PID, not readiness. It tracks Linux process start times, holds a lock, owns children, and stops them plus its DB on normal stop or child failure. There are no hidden retries. A second launch is a no-op, not a mode switch. Database data lives in the ephemeral container overlay (`--image-volume=ignore`), fixtures are in memory, and both reset on restart. Under its lock the supervisor may remove only a recorded stopped DB matching the exact synthetic image, labels, ports, environment and absence of mounts, with all prior tracked processes dead. It never adopts or force-removes an unknown container. Workspace stop must terminate the complete workload; this helper is not host boot persistence. An owner-only terminal app may call `show-status.sh`, but must not expose HTTP ports or credentials.

## Validation

```sh
mise exec -- python -m unittest discover -s contrib/isolated-demo -p 'test_*.py' -v
mise exec -- cargo fmt --manifest-path contrib/isolated-demo/Cargo.toml --check
mise exec -- cargo clippy --locked --manifest-path contrib/isolated-demo/Cargo.toml --all-targets -- -D warnings
mise exec -- cargo test --locked --manifest-path contrib/isolated-demo/Cargo.toml
mise exec -- bash contrib/isolated-demo/run-gate.sh
```

Offline Python tests use mocks and no services. The separate recorder runs the exact full `mise exec -- make check`, without filters, in the repository root. It records Git HEAD, working-tree status, actual exit code, timestamps, log path and resource settings (Cargo jobs 2, dev/test debug 0). Status matching is not proof of unchanged dirty/untracked file contents. A stale or interrupted recorder never counts as passed. Signals stop the entire owned gate process group before releasing its lock. Old gate results do not establish acceptance for a new source revision.

The full gate needs the private Podman Docker API socket (root `/run/podman/podman.sock`, otherwise `$XDG_RUNTIME_DIR/podman/podman.sock`), real Claude CLI, OpenSSL executable and `systemd-analyze`; systemd need not be PID 1. Resource settings reflect a tested 2 CPU/4 GiB development environment, not minimum requirements. Building tools/images requires explicit network setup; the demo workflow and tests use local fakes, not external providers.
