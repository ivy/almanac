#!/usr/bin/env python3
"""Local-only operational helpers; no HTTP server or provider mutation API."""
import argparse
import datetime as dt
import fcntl
import json
import os
from pathlib import Path
import shlex
import signal
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

SOURCE = Path(__file__).resolve().parent
REPO = SOURCE.parents[1]
DEMO = Path(os.environ.get("ALMANAC_DEMO_STATE",
                           Path.home() / ".local/state/almanac-isolated-demo")).resolve()
FIXTURE = SOURCE / "target/debug/almanac-demo"
BROKER = REPO / "target/debug/almanac-broker"
DAEMON = REPO / "almanacs/renovate/target/debug/almanac-renovate"
CONTAINER = "almanac-demo-postgres"
DATABASE = "postgres://postgres:demo-local@127.0.0.1:55432/almanac_demo"
CONTROL = "http://127.0.0.1:8810/state"
ENV_KEYS = {
    "GITHUB_API_URL", "GITHUB_TOKEN", "ALMANAC_OWNER", "ALMANAC_RENOVATE_AUTHOR",
    "ALMANAC_REGISTRY_URL", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY",
    "ALMANAC_WEBHOOK_SECRET", "ALMANAC_BROKER_LISTEN", "ALMANAC_LISTEN",
    "ALMANAC_ALERTS_REPO", "ALMANAC_CONFIG", "ALMANAC_SANDBOX_IMAGE", "ALMANAC_MODE",
}


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def save(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    temporary.replace(path)


def base_env():
    # Deliberately do not copy os.environ: app auth overrides dummy GitHub tokens.
    environment = {
        "PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"),
        "HOME": os.environ.get("HOME", "/tmp"),
        "LANG": "C.UTF-8", "SQLX_OFFLINE": "true",
        "ALMANAC_DEMO_STATE": str(DEMO),
        "XDG_RUNTIME_DIR": os.environ.get("XDG_RUNTIME_DIR", f"/run/user/{os.getuid()}"),
    }
    for key in ("BUILDAH_ISOLATION", "TMPDIR", "CONTAINERS_CONF", "CONTAINERS_STORAGE_CONF",
                "CONTAINERS_REGISTRIES_CONF", "CONTAINERS_POLICY"):
        if key in os.environ:
            environment[key] = os.environ[key]
    return environment


def local_url(value):
    parsed = urllib.parse.urlsplit(value)
    if (parsed.scheme != "http" or parsed.hostname != "127.0.0.1"
            or not parsed.port or parsed.username or parsed.password
            or parsed.query or parsed.fragment):
        raise ValueError("fixture endpoint must be plain HTTP on numeric IPv4 loopback")
    return value


def fixture_env(path):
    result = {}
    for line in path.read_text().splitlines():
        parts = shlex.split(line)
        if len(parts) != 2 or parts[0] != "export" or "=" not in parts[1]:
            raise ValueError("unexpected fixture env syntax (file is parsed, never sourced)")
        key, value = parts[1].split("=", 1)
        if key not in ENV_KEYS or key in result:
            raise ValueError("unexpected or duplicate fixture key")
        result[key] = value
    if set(result) != ENV_KEYS:
        raise ValueError("fixture env keys do not match the exported native fixture")
    for key in ("GITHUB_API_URL", "ALMANAC_REGISTRY_URL", "ANTHROPIC_BASE_URL"):
        local_url(result[key])
    expected = {
        "GITHUB_TOKEN": "t", "ANTHROPIC_API_KEY": "stub", "ALMANAC_OWNER": "fleet",
        "ALMANAC_ALERTS_REPO": "fleet/ops", "ALMANAC_BROKER_LISTEN": "127.0.0.1:8787",
        "ALMANAC_LISTEN": "127.0.0.1:8788", "ALMANAC_CONFIG": "/nonexistent/almanac.toml",
        "ALMANAC_MODE": "live", "ALMANAC_WEBHOOK_SECRET": "synthetic-demo-webhook-secret",
        "ALMANAC_SANDBOX_IMAGE": "localhost/almanac-fixer:test",
        "ALMANAC_REGISTRY_URL": "http://127.0.0.1:8811",
    }
    if any(result[key] != value for key, value in expected.items()):
        raise ValueError("fixture env does not match the local synthetic deployment")
    return result


def provider_env(read_only=True):
    result = base_env() | fixture_env(DEMO / "fixtures.env")
    # Read-only is stronger than dry-run: NoAgent and no provider writes.
    result.update(DATABASE_URL=DATABASE, ALMANAC_READ_ONLY="1" if read_only else "0",
                  ALMANAC_MODE="dry-run" if read_only else "live", ALMANAC_BOT_LOGIN="almanac[bot]",
                  ALMANAC_ADVISORIES_URL="http://127.0.0.1:8811")
    return result


def request(url):
    # Do not honor inherited proxies or follow redirects to an external address.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(local_url(url), timeout=5) as response:
            return response.status, response.read(256_000).decode()
    except urllib.error.HTTPError as error:
        return error.code, ""


def run(args, **kwargs):
    return subprocess.run(args, env=base_env(), text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, **kwargs)


def process_identity(pid):
    try:
        # starttime distinguishes PID reuse; comm can contain spaces/parentheses.
        return Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[19]
    except (OSError, IndexError):
        return None


def alive(record):
    return bool(record and record.get("start_ticks") and
                process_identity(record["pid"]) == record["start_ticks"])


def identity(pid):
    return {"pid": pid, "start_ticks": process_identity(pid)}


def read_json(path):
    return json.loads(path.read_text())


def reconcile_previous():
    """Remove only a stopped, recorded demo DB before fresh in-memory seeding.

    Called under supervisor.lock, before runtime.json is replaced. Never adopt.
    """
    exists = run(["podman", "container", "exists", CONTAINER])
    if exists.returncode == 1:
        return
    if exists.returncode != 0:
        raise RuntimeError("cannot inspect previous demo DB: Podman failed")
    path = DEMO / "runtime.json"
    previous = read_json(path) if path.exists() else {}
    if (previous.get("database_container") != CONTAINER
            or alive(previous.get("supervisor"))
            or any(alive(child) for child in previous.get("children", {}).values())):
        raise RuntimeError("existing DB is not recorded with a dead supervisor and children")
    inspected = run(["podman", "container", "inspect", CONTAINER])
    if inspected.returncode:
        raise RuntimeError("cannot inspect previous demo DB")
    records = json.loads(inspected.stdout)
    if not isinstance(records, list) or len(records) != 1:
        raise RuntimeError("unexpected previous demo DB inspection")
    record = records[0]
    config, host, state = (record.get(key, {}) for key in ("Config", "HostConfig", "State"))
    identifier = record.get("Id", "")
    bindings = {"5432/tcp": [{"HostIp": "127.0.0.1", "HostPort": "55432"}]}
    safe = (len(identifier) == 64 and all(c in "0123456789abcdef" for c in identifier)
            and record.get("Name", "").lstrip("/") == CONTAINER
            and config.get("Labels", {}).get("almanac.demo") == "local"
            and config.get("Image") == "docker.io/library/postgres:17-alpine"
            and "POSTGRES_PASSWORD=demo-local" in config.get("Env", [])
            and "POSTGRES_DB=almanac_demo" in config.get("Env", [])
            and state.get("Running") is False
            and state.get("Status") in ("exited", "stopped", "created", "configured")
            and host.get("PortBindings") == bindings and not host.get("Binds")
            and not record.get("Mounts"))
    if not safe:
        raise RuntimeError("previous DB does not match stopped local demo image/labels/ports/storage")
    removed = run(["podman", "rm", identifier])
    if removed.returncode:
        raise RuntimeError("could not remove previous stopped demo DB")
    print("Removed recorded stopped demo DB; fixture seeds and database restart fresh.", flush=True)


def supervise(fixture_live=False):
    lock = (DEMO / "supervisor.lock").open("a")
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    reconcile_previous()
    status = {"started": now(), "supervisor": identity(os.getpid()),
              "phase": "starting", "children": {},
              "scope": "live synthetic fixture" if fixture_live else "read-only local simulation"}
    children, logs = [], []
    stopping = False

    def stop_requested(signum, frame):
        nonlocal stopping
        stopping = True

    signal.signal(signal.SIGTERM, stop_requested)
    signal.signal(signal.SIGINT, stop_requested)

    def persist():
        save(DEMO / "runtime.json", status)

    def spawn(name, binary, env):
        stream = (DEMO / f"{name}.log").open("a")
        logs.append(stream)
        child = subprocess.Popen([str(binary)] + (["run"] if name == "daemon" else []),
                                 cwd=DEMO, env=env, stdout=stream, stderr=subprocess.STDOUT,
                                 start_new_session=True)
        children.append(child)
        status["children"][name] = identity(child.pid)
        persist()
        return child

    def wait_for(check, label):
        deadline = time.monotonic() + 90
        while not stopping and time.monotonic() < deadline:
            if any(child.poll() is not None for child in children):
                raise RuntimeError("demo child exited during startup; inspect its local log")
            try:
                if check():
                    return
            except (OSError, ValueError, urllib.error.URLError):
                pass
            time.sleep(0.5)
        raise RuntimeError(f"startup stopped or timed out waiting for {label}")

    persist()
    try:
        for binary in (FIXTURE, BROKER, DAEMON):
            if not binary.is_file() or not os.access(binary, os.X_OK):
                raise RuntimeError(f"missing executable: {binary}")
        # Never adopt an unowned DB/container or bind a non-loopback host port.
        exists = run(["podman", "container", "exists", CONTAINER])
        if exists.returncode != 1:
            raise RuntimeError("demo DB container exists or Podman failed; inspect/stop explicitly")
        created = run(["podman", "run", "--detach", "--name", CONTAINER,
                       "--label", "almanac.demo=local", "--pull=never", "--image-volume=ignore",
                       "--publish", "127.0.0.1:55432:5432",
                       "--env", "POSTGRES_PASSWORD=demo-local", "--env", "POSTGRES_DB=almanac_demo",
                       "docker.io/library/postgres:17-alpine"])
        if created.returncode:
            raise RuntimeError("Postgres container launch failed: " + created.stdout[-2000:])
        status["database_container"] = CONTAINER
        persist()
        wait_for(lambda: run(["podman", "exec", CONTAINER, "pg_isready", "-U", "postgres",
                              "-d", "almanac_demo"]).returncode == 0, "PostgreSQL")
        old_env = DEMO / "fixtures.env"
        old_env.unlink(missing_ok=True)
        spawn("fixtures", FIXTURE, base_env())
        wait_for(lambda: old_env.exists() and request(CONTROL)[0] == 200, "native fixtures")
        environment = provider_env(read_only=not fixture_live)
        code, body = request(CONTROL)
        observation = json.loads(body)
        expected_state = {"repo": "fleet/demo", "forge_url": environment["GITHUB_API_URL"],
                          "model_url": environment["ANTHROPIC_BASE_URL"],
                          "registry_url": environment["ALMANAC_REGISTRY_URL"],
                          "webhook_target": "http://127.0.0.1:8787/webhooks/github"}
        if code != 200 or any(observation.get(key) != value for key, value in expected_state.items()):
            raise RuntimeError("fixture /state does not match the allowlisted local providers")
        if fixture_live:
            if not shutil.which("claude", path=environment["PATH"]):
                raise RuntimeError("fixture-live needs the real claude CLI on PATH")
            if run(["podman", "image", "exists", "localhost/almanac-fixer:test"]).returncode:
                raise RuntimeError("fixture-live needs prebuilt localhost/almanac-fixer:test")
        spawn("broker", BROKER, base_env() | {
            "DATABASE_URL": DATABASE, "ALMANAC_WEBHOOK_SECRET": environment["ALMANAC_WEBHOOK_SECRET"],
            "ALMANAC_BROKER_LISTEN": "127.0.0.1:8787"})
        wait_for(lambda: request("http://127.0.0.1:8787/readyz")[0] == 200, "broker readyz")
        spawn("daemon", DAEMON, environment)
        wait_for(lambda: request("http://127.0.0.1:8788/healthz")[0] == 200, "daemon healthz")
        status["phase"] = "running"
        persist()
        while not stopping:
            if any(child.poll() is not None for child in children):
                raise RuntimeError("demo child exited; see fixtures/broker/daemon.log")
            time.sleep(1)
    except Exception as error:
        status["phase"], status["error"] = "failed", str(error)
        print(str(error), file=sys.stderr, flush=True)
    finally:
        for child in reversed(children):
            if child.poll() is None:
                os.killpg(child.pid, signal.SIGTERM)
        for child in children:
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(child.pid, signal.SIGKILL)
                child.wait()
        if status.get("database_container"):
            run(["podman", "stop", "--time", "10", CONTAINER])
            run(["podman", "rm", CONTAINER])
        for stream in logs:
            stream.close()
        status["ended"] = now()
        if status["phase"] != "failed":
            status["phase"] = "stopped"
        persist()


def launch(fixture_live=False):
    if (DEMO / "runtime.json").exists():
        previous = read_json(DEMO / "runtime.json")
        if alive(previous.get("supervisor")):
            print("Demo supervisor already running; inspect show-status.sh")
            return
    with (DEMO / "supervisor.log").open("a") as stream:
        arguments = [sys.executable, "-u", str(Path(__file__)), "supervise"]
        if fixture_live:
            arguments.append("--fixture-live")
        child = subprocess.Popen(arguments,
                                 env=base_env(), cwd=DEMO, stdin=subprocess.DEVNULL,
                                 stdout=stream, stderr=subprocess.STDOUT, start_new_session=True)
    print(f"Supervisor launched (PID {child.pid}); startup is asynchronous, not proof of readiness.")
    print(f"Run {SOURCE}/show-status.sh; startup errors: {DEMO}/supervisor.log")


def stop():
    record = read_json(DEMO / "runtime.json")
    if alive(record.get("supervisor")):
        os.kill(record["supervisor"]["pid"], signal.SIGTERM)
        print("Stop requested; supervisor terminates children and removes its DB container.")
    else:
        print("No matching live supervisor. No process was signaled; inspect stale children/DB manually.")


def checkout_identity():
    return (run(["git", "rev-parse", "HEAD"], cwd=REPO),
            run(["git", "status", "--porcelain=v1"], cwd=REPO))


def print_gate():
    print("\nFull make check (separate from CLI check)")
    result_path, log = DEMO / "gate-result.json", DEMO / "gate.log"
    if not result_path.exists():
        print("Not recorded. No passing gate is claimed.")
        return
    result = read_json(result_path)
    print(json.dumps(result, indent=2))
    if result.get("state") == "running" and not alive(result.get("process")):
        print("Recorder no longer running; gate outcome UNKNOWN (interrupted), not passed.")
    current, dirty = checkout_identity()
    same = current.returncode == 0 and current.stdout.strip() == result.get("revision")
    same = same and dirty.returncode == 0 and dirty.stdout == result.get("working_tree")
    print("Recorded revision/working-tree text matches now:" + (" yes" if same else " NO/unknown"))
    print("This does not detect further edits to already-dirty files or untracked file contents.")
    if log.exists():
        print(f"Actual log tail ({log}):")
        lines = log.read_text(errors="replace").splitlines()
        print("\n".join(lines[-12:])[-6000:])


def show():
    print(f"Local simulated Almanac — {now()}")
    print("Operational terminal view, not a native web console. Observations, not expected outcomes.")
    runtime = DEMO / "runtime.json"
    if runtime.exists():
        state = read_json(runtime)
        print("Supervisor:", state.get("phase"), "live:", alive(state.get("supervisor")))
        print("Configured scope:", state.get("scope"), "(not proof of a repair)")
        if state.get("error"):
            print("Startup error:", state["error"])
        for name, record in state.get("children", {}).items():
            print(f"{name}: PID {record['pid']}; matching live process: {alive(record)}")
    else:
        print("No recorded supervisor; services may be unavailable.")
    for label, url in (("Broker health", "http://127.0.0.1:8787/healthz"),
                       ("Broker readiness (SELECT 1)", "http://127.0.0.1:8787/readyz"),
                       ("Daemon health (not DB readiness)", "http://127.0.0.1:8788/healthz")):
        try:
            start = time.monotonic()
            code, _ = request(url)
            print(f"{label}: HTTP {code}, {(time.monotonic()-start)*1000:.0f}ms, {url}")
        except (OSError, ValueError, urllib.error.URLError) as error:
            print(f"{label}: unavailable ({type(error).__name__}), {url}")
    print("\nNative fixture controller observations (not inferred workflow state):")
    try:
        code, body = request(CONTROL)
        if code != 200:
            raise ValueError(f"HTTP {code}")
        observation = json.loads(body)
        print(json.dumps(observation, indent=2)[:24000])
    except (OSError, ValueError, urllib.error.URLError) as error:
        print("Fixture state unavailable:", str(error))
    print("\nPostgreSQL actual delivery/cursor/run observations (read-only transaction):")
    # Table names come from the real schema; missing workflow tables are explicit.
    sql = r"""BEGIN READ ONLY;
SELECT 1 AS database_reachable;
SELECT to_regclass('public.deliveries') AS deliveries_table,
       to_regclass('public.cursors') AS cursors_table,
       to_regclass('public.runs') AS runs_table;
SELECT 'SELECT count(*) AS stored_deliveries FROM deliveries' WHERE to_regclass('public.deliveries') IS NOT NULL \gexec
SELECT 'SELECT seq, source, delivery_id, kind FROM deliveries ORDER BY seq DESC LIMIT 5' WHERE to_regclass('public.deliveries') IS NOT NULL \gexec
SELECT 'SELECT almanac, seq FROM cursors ORDER BY almanac' WHERE to_regclass('public.cursors') IS NOT NULL \gexec
SELECT 'SELECT status, count(*) FROM runs GROUP BY status ORDER BY status' WHERE to_regclass('public.runs') IS NOT NULL \gexec
ROLLBACK;
"""
    try:
        evidence = run(["podman", "exec", "-i", CONTAINER, "psql", "-X", "-v", "ON_ERROR_STOP=1",
                        "-U", "postgres", "-d", "almanac_demo"], input=sql, timeout=15)
        print(evidence.stdout[:16000])
        if evidence.returncode:
            print("DB observation failed, exit:", evidence.returncode)
    except (OSError, subprocess.TimeoutExpired) as error:
        print("DB observation unavailable:", type(error).__name__)
    print("\nActual native --format json report, forced ALMANAC_READ_ONLY=1:")
    try:
        environment = provider_env()
        print("Local forge:", environment["GITHUB_API_URL"], "owner:", environment["ALMANAC_OWNER"])
        report = subprocess.run([str(DAEMON), "--format", "json", "report"], env=environment,
                                cwd=DEMO, text=True, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, timeout=30)
        print(report.stdout[:24000])
        print("Report exit:", report.returncode)
    except (OSError, ValueError, subprocess.TimeoutExpired) as error:
        print("Report unavailable:", str(error))
    print_gate()
    print(f"\nRerun: bash {SOURCE}/show-status.sh")
    print("No mutations or reset commands are sent by this viewer.")


def gate():
    lock = (DEMO / "gate.lock").open("a")
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    command = ["mise", "exec", "--", "make", "check"]
    revision, tree = checkout_identity()
    if revision.returncode or tree.returncode:
        raise RuntimeError("cannot record checkout identity")
    env = base_env()
    socket = Path(env["XDG_RUNTIME_DIR"]) / "podman/podman.sock"
    if os.getuid() == 0:
        socket = Path("/run/podman/podman.sock")
    if not socket.exists():
        raise RuntimeError(f"Podman Docker API socket missing: {socket}; full gate not started")
    env.update(DOCKER_HOST=f"unix://{socket}", TESTCONTAINERS_RYUK_DISABLED="true",
               CARGO_BUILD_JOBS="2", CARGO_PROFILE_DEV_DEBUG="0", CARGO_PROFILE_TEST_DEBUG="0")
    result = {"command": "mise exec -- make check", "cwd": str(REPO), "started": now(),
              "ended": None, "exit_code": None, "state": "running", "process": identity(os.getpid()),
              "revision": revision.stdout.strip(), "working_tree": tree.stdout,
              "log": str(DEMO / "gate.log"), "docker_host": env["DOCKER_HOST"],
              "build_environment": {key: env[key] for key in ("CARGO_BUILD_JOBS",
                                    "CARGO_PROFILE_DEV_DEBUG", "CARGO_PROFILE_TEST_DEBUG")},
              "revision_command": "git rev-parse HEAD",
              "working_tree_command": "git status --porcelain=v1"}
    save(DEMO / "gate-result.json", result)
    print("Running full gate. Actual output:", result["log"], flush=True)
    code, child, interrupted = None, None, None

    def terminate(signum, frame):
        nonlocal interrupted
        interrupted = signum
        # During Popen, defer raising until the owned PID has been assigned.
        if child is not None:
            raise InterruptedError("gate recorder terminated")

    watched = (signal.SIGTERM, signal.SIGHUP, signal.SIGINT)
    handlers = {signum: signal.signal(signum, terminate) for signum in watched}
    try:
        with (DEMO / "gate.log").open("w") as stream:
            stream.write(json.dumps(result) + "\n")
            stream.flush()
            child = subprocess.Popen(command, cwd=REPO, env=env, stdout=stream,
                                     stderr=subprocess.STDOUT, start_new_session=True)
            if interrupted is not None:
                raise InterruptedError("gate recorder terminated during launch")
            code = child.wait()
    except BaseException:
        # Keep the gate lock until its entire owned session is stopped and reaped.
        for signum in watched:
            signal.signal(signum, signal.SIG_IGN)
        if child is not None:
            try:
                os.killpg(child.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                pass
            # Also stop descendants if the group leader exited before them.
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            child.wait()
        if interrupted is None:
            raise
    finally:
        result.update(ended=now(), exit_code=code,
                      state="interrupted" if interrupted is not None or code is None
                      else "passed" if code == 0 else "failed")
        if interrupted is not None:
            result["interrupted_signal"] = interrupted
            result["child_exit_code"] = child.returncode if child is not None else None
        save(DEMO / "gate-result.json", result)
        for signum, handler in handlers.items():
            signal.signal(signum, handler)
        lock.close()
    print("Full gate exit:", code if interrupted is None else "interrupted")
    return code if interrupted is None else 128 + interrupted


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("launch", "supervise", "show", "stop", "gate"))
    parser.add_argument("--fixture-live", action="store_true",
                        help="enable real workflows against verified local fixtures only (launch)")
    arguments = parser.parse_args()
    action = arguments.action
    if arguments.fixture_live and action not in ("launch", "supervise"):
        parser.error("--fixture-live is only valid for launch/supervise")
    os.umask(0o077)
    DEMO.mkdir(parents=True, exist_ok=True, mode=0o700)
    try:
        if action in ("launch", "supervise"):
            return {"launch": launch, "supervise": supervise}[action](arguments.fixture_live)
        return {"show": show, "stop": stop, "gate": gate}[action]()
    except (OSError, ValueError, RuntimeError) as error:
        print(f"Demo {action} failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
