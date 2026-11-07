"""Offline support tests. No services and no external provider calls."""
import importlib.util
import copy
import json
from types import SimpleNamespace
import os
import signal
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("demo", Path(__file__).with_name("demo.py"))
demo = importlib.util.module_from_spec(spec)
spec.loader.exec_module(demo)

FIXTURE = """export GITHUB_API_URL=http://127.0.0.1:12345
export GITHUB_TOKEN=t
export ALMANAC_OWNER=fleet
export ALMANAC_RENOVATE_AUTHOR='ivy-s-renovate-bot[bot]'
export ALMANAC_REGISTRY_URL=http://127.0.0.1:8811
export ANTHROPIC_BASE_URL=http://127.0.0.1:12346
export ANTHROPIC_API_KEY=stub
export ALMANAC_WEBHOOK_SECRET=synthetic-demo-webhook-secret
export ALMANAC_BROKER_LISTEN=127.0.0.1:8787
export ALMANAC_LISTEN=127.0.0.1:8788
export ALMANAC_ALERTS_REPO=fleet/ops
export ALMANAC_CONFIG=/nonexistent/almanac.toml
export ALMANAC_SANDBOX_IMAGE=localhost/almanac-fixer:test
export ALMANAC_MODE=live
"""


class DemoTests(unittest.TestCase):
    def test_gate_termination_stops_owned_group_and_records_interrupted(self):
        handlers, events = {}, []
        child = SimpleNamespace(pid=456, returncode=-signal.SIGTERM)

        def register(signum, handler):
            previous = handlers.get(signum, signal.SIG_DFL)
            handlers[signum] = handler
            return previous

        def wait(timeout=None):
            events.append(("wait", timeout))
            if len(events) == 1:
                handlers[signal.SIGHUP](signal.SIGHUP, None)
            return child.returncode

        child.wait = wait
        success = SimpleNamespace(returncode=0, stdout="checkout")
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            with patch.object(demo, "DEMO", directory), \
                    patch.object(demo, "checkout_identity", return_value=(success, success)), \
                    patch.object(demo.Path, "exists", return_value=True), \
                    patch.object(demo.signal, "signal", side_effect=register), \
                    patch.object(demo.subprocess, "Popen", return_value=child), \
                    patch.object(demo.os, "killpg", side_effect=lambda pid, sig: events.append((pid, sig))):
                code = demo.gate()
            result = json.loads((directory / "gate-result.json").read_text())
        self.assertEqual(code, 128 + signal.SIGHUP)
        self.assertEqual(result["state"], "interrupted")
        self.assertIsNone(result["exit_code"])
        self.assertEqual(result["child_exit_code"], -signal.SIGTERM)
        self.assertEqual(events[1:], [(456, signal.SIGTERM), ("wait", 10),
                                     (456, signal.SIGKILL), ("wait", None)])
        self.assertTrue(all(handler == signal.SIG_DFL for handler in handlers.values()))

    def stale_database(self):
        return {
            "Id": "a" * 64, "Name": demo.CONTAINER,
            "Config": {"Labels": {"almanac.demo": "local"},
                       "Image": "docker.io/library/postgres:17-alpine",
                       "Env": ["POSTGRES_PASSWORD=demo-local", "POSTGRES_DB=almanac_demo"]},
            "HostConfig": {"Binds": None, "PortBindings": {
                "5432/tcp": [{"HostIp": "127.0.0.1", "HostPort": "55432"}]}},
            "State": {"Running": False, "Status": "exited"}, "Mounts": [],
        }

    def reconcile_mock(self, directory, record, *, recorded=True, process_alive=False):
        if recorded:
            (directory / "runtime.json").write_text(json.dumps({
                "database_container": demo.CONTAINER, "supervisor": {"pid": 123}, "children": {}}))
        calls = []

        def fake_run(args):
            calls.append(args)
            if args[1:3] == ["container", "inspect"]:
                return SimpleNamespace(returncode=0, stdout=json.dumps([record]))
            return SimpleNamespace(returncode=0, stdout="")

        with patch.object(demo, "DEMO", directory), patch.object(demo, "alive", return_value=process_alive), \
                patch.object(demo, "run", side_effect=fake_run):
            demo.reconcile_previous()
        return calls

    def test_reconcile_removes_only_recorded_stopped_matching_container_id(self):
        with tempfile.TemporaryDirectory() as temporary:
            calls = self.reconcile_mock(Path(temporary), self.stale_database())
        self.assertEqual(calls[-1], ["podman", "rm", "a" * 64])
        self.assertNotIn("--force", calls[-1])
        self.assertNotIn("--volumes", calls[-1])

    def test_reconcile_refuses_unowned_live_or_changed_container(self):
        good = self.stale_database()
        altered = []
        for key, value in (("Labels", {}), ("Image", "other-image")):
            record = copy.deepcopy(good)
            record["Config"][key] = value
            altered.append(record)
        for key, value in (("Running", True), ("Status", "running")):
            record = copy.deepcopy(good)
            record["State"][key] = value
            altered.append(record)
        record = copy.deepcopy(good)
        record["Mounts"] = [{"Type": "bind", "Source": "/home", "Destination": "/data"}]
        altered.append(record)
        record = copy.deepcopy(good)
        record["HostConfig"]["PortBindings"]["5432/tcp"][0]["HostIp"] = "0.0.0.0"
        altered.append(record)
        for record, options in [(record, {}) for record in altered] + [
                (good, {"recorded": False}), (good, {"process_alive": True})]:
            with tempfile.TemporaryDirectory() as temporary:
                with self.assertRaises(RuntimeError):
                    self.reconcile_mock(Path(temporary), record, **options)

    def test_fixture_parser_and_forced_mode(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            path = directory / "fixtures.env"
            path.write_text(FIXTURE)
            values = demo.fixture_env(path)
            self.assertEqual(values["GITHUB_TOKEN"], "t")
            with patch.object(demo, "DEMO", directory):
                self.assertEqual(demo.provider_env()["ALMANAC_READ_ONLY"], "1")
                self.assertEqual(demo.provider_env()["ALMANAC_MODE"], "dry-run")
                live = demo.provider_env(read_only=False)
                self.assertEqual(live["ALMANAC_READ_ONLY"], "0")
                self.assertEqual(live["ALMANAC_MODE"], "live")
                self.assertEqual(live["ALMANAC_ADVISORIES_URL"], "http://127.0.0.1:8811")
            for bad in (FIXTURE.replace("127.0.0.1:12345", "api.github.com:443"),
                        FIXTURE.replace("GITHUB_TOKEN=t", "GITHUB_TOKEN=real"),
                        FIXTURE + "export GITHUB_APP_ID=123\n",
                        FIXTURE + "export GITHUB_TOKEN=t\n",
                        FIXTURE + "touch /tmp/not-executed\n"):
                path.write_text(bad)
                with self.assertRaises(ValueError):
                    demo.fixture_env(path)

    def test_urls_are_numeric_local_http_without_credentials(self):
        for bad in ("https://127.0.0.1:8810", "http://localhost:8810", "http://evil.test:8810",
                    "http://127.0.0.1", "http://token@127.0.0.1:8810", "http://127.0.0.1:8810?q=1"):
            with self.assertRaises(ValueError):
                demo.local_url(bad)
        self.assertEqual(demo.local_url("http://127.0.0.1:8810/state"),
                         "http://127.0.0.1:8810/state")

    def test_base_environment_does_not_forward_auth(self):
        with patch.dict(os.environ, {"GITHUB_TOKEN": "real", "ANTHROPIC_API_KEY": "real",
                                     "GITHUB_APP_PRIVATE_KEY": "secret", "HTTP_PROXY": "remote",
                                     "SSH_AUTH_SOCK": "/secret", "CARGO_TARGET_DIR": "/wrong",
                                     "BUILDAH_ISOLATION": "chroot", "CONTAINERS_CONF": "/runtime.conf"}):
            actual = demo.base_env()
        for name in ("GITHUB_TOKEN", "GITHUB_APP_PRIVATE_KEY", "ANTHROPIC_API_KEY", "HTTP_PROXY",
                     "SSH_AUTH_SOCK", "CARGO_TARGET_DIR"):
            self.assertNotIn(name, actual)
        self.assertEqual(actual["BUILDAH_ISOLATION"], "chroot")
        self.assertEqual(actual["CONTAINERS_CONF"], "/runtime.conf")


if __name__ == "__main__":
    unittest.main()
