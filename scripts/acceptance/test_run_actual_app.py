"""Offline by default; explicit CI opt-in enables bounded loopback reset tests."""

import base64
import copy
import hashlib
import http.client
import io
import json
import os
from pathlib import Path
import subprocess
import socket
import socketserver
import struct
import threading
import time
import tempfile
import tomllib
import unittest
import urllib.error
import urllib.parse
from unittest import mock

import run_actual_app as app


class FixtureResponse(io.BytesIO):
    def __init__(self, body, code=200):
        super().__init__(json.dumps(body).encode())
        self.code = code


class SyntheticSessionHTTP:
    """Offline transport model of the existing one-active-token-per-user contract.

    This checks fixture request sequencing only. The actual-app harness runs the
    same session/denial assertions against real Control and Redis in approved CI.
    """
    def __init__(self):
        self.sessions = {app.USERNAME: "synthetic-admin-0"}
        self.issued = {"synthetic-admin-0": app.USERNAME}
        self.calls = []
        self.sequence = 0
        self.permissions = []
        self.role_override = {}
        self.first_build_override = {}

    @staticmethod
    def reply(request, code, detail=None, http_status=200):
        body = {"code": code, "detail": detail}
        if http_status >= 400:
            raise urllib.error.HTTPError(request.full_url, http_status, "synthetic response", {}, FixtureResponse(body))
        return FixtureResponse(body, http_status)

    def open(self, request, timeout):
        path = urllib.parse.urlsplit(request.full_url).path.removeprefix("/api/v1")
        method = request.get_method()
        payload = None if request.data is None else json.loads(request.data)
        self.calls.append((method, path, payload))
        if path == "/users/login":
            username = payload["username"]
            assert username in (app.USERNAME, app.OBSERVER_USERNAME)
            assert payload["password"] == app.encoded(app.PASSWORD)
            self.sequence += 1
            token = f"synthetic-session-{self.sequence}"
            self.sessions[username] = token
            self.issued[token] = username
            return self.reply(request, 200, {"token": {"accessToken": token, "refreshToken": "synthetic-refresh"}})
        token = request.get_header("Authorization", "").removeprefix("Bearer ")
        username = self.issued.get(token)
        if not username or self.sessions.get(username) != token:
            return self.reply(request, 4405, http_status=401)
        if path == "/users/fetch":
            return self.reply(request, 200, {"userId": "admin-id" if username == app.USERNAME else "observer-id",
                                             "username": username})
        if username == app.OBSERVER_USERNAME:
            if path == "/me/first-build":
                return self.reply(request, 200, {"scopeOptions": [], "scopeOptionsTruncated": False,
                                                "canEnterScope": False, "configuration": {"access": "restricted"},
                                                "agent": {"access": "restricted"}} | self.first_build_override)
            if path.endswith("/trigger"):
                return self.reply(request, 4030)
            if path == "/users/":
                return self.reply(request, 403, http_status=403)
            return self.reply(request, 200, {"readBy": username})
        if path == "/role" and method == "POST":
            assert payload["permissions"] == [] and payload["roleId"] == app.OBSERVER_ROLE
            return self.reply(request, 200, {})
        if path == "/users/invite":
            assert payload["role"] == app.OBSERVER_ROLE
            return self.reply(request, 200, {"userId": "observer-id", **payload})
        if path == "/users/observer-id/password":
            assert payload == {"newPassword": app.encoded(app.PASSWORD)}
            return self.reply(request, 200, {})
        if path == f"/role/{app.OBSERVER_ROLE}/permissions":
            self.permissions = payload["permissions"]
            return self.reply(request, 200, {})
        if path == f"/role/{app.OBSERVER_ROLE}":
            return self.reply(request, 200, {"roleId": app.OBSERVER_ROLE, "scope": "platform", "isEnabled": 1,
                                             "isBuiltin": 0, "permissions": self.permissions} | self.role_override)
        raise AssertionError(f"unexpected synthetic route {method} {path}")


class ObserverSessionTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.fixture = app.Fixture(mock.Mock(), Path(self.directory.name))
        self.fixture.http = SyntheticSessionHTTP()
        self.fixture.token = "synthetic-admin-0"

    def configure(self):
        with mock.patch.object(app.time, "sleep") as sleep:
            self.fixture.configure_observer()
            sleep.assert_called_once_with(1.1)

    def test_observer_uses_only_read_permission_without_second_admin_bootstrap(self):
        with mock.patch.object(self.fixture, "run") as run:
            self.configure()
            run.assert_not_called()
        self.assertEqual(self.fixture.observer_id, "observer-id")
        self.assertEqual(self.fixture.http.permissions, ["pipeline:read"])
        calls = self.fixture.http.calls
        invite = next(i for i, call in enumerate(calls) if call[1] == "/users/invite")
        permissions = next(i for i, call in enumerate(calls) if call[1].endswith("/permissions"))
        self.assertLess(invite, permissions, "policy reload must include the newly granted observer")
        logins = [call[2]["username"] for call in calls if call[1] == "/users/login"]
        self.assertEqual(logins, [app.OBSERVER_USERNAME, app.OBSERVER_USERNAME])
        self.assertFalse(any("token" in key.lower() for key in self.fixture.env))
        self.assertEqual(list(self.fixture.coordination.iterdir()), [])
        self.assertIn(("GET", "/me/first-build", None), calls)

    def test_browser_admin_relogin_leaves_observer_valid_and_single_user_old_token_invalid(self):
        self.configure()
        observer_token = self.fixture.token
        browser_request = urllib.request.Request(app.BACKEND_URL + "/api/v1/users/login", method="POST",
                                                 data=json.dumps({"username": app.USERNAME,
                                                                  "password": app.encoded(app.PASSWORD)}).encode())
        self.fixture.http.open(browser_request, timeout=10).close()
        self.fixture.require_observer_session()
        self.assertEqual(self.fixture.token, observer_token)
        self.fixture.expect_rejected("/users/fetch", 401, 4405, token="synthetic-admin-0")
        self.fixture.expect_rejected("/pipelines/real-fixture-pipeline/trigger", 200, 4030, {}, "POST")
        self.assertEqual(self.fixture.token, observer_token)

    def test_observer_role_cannot_silently_expand_permissions_or_be_builtin(self):
        for override in ({"permissions": ["pipeline:read", "pipeline:trigger"]}, {"isBuiltin": 1}, {"scope": "project"}):
            self.fixture.http.role_override = override
            with self.subTest(override=override), self.assertRaisesRegex(RuntimeError, "exactly"):
                self.configure()
            self.assertEqual(self.fixture.token, "synthetic-admin-0")

    def test_observer_readiness_rejects_visible_leaked_or_missing_fields(self):
        for patch in (
            {"configuration": {"access": "restricted", "defaultStorageConfigured": True}},
            {"configuration": {"access": "visible", "agentSigningConfigured": True}},
            {"agent": {"access": "restricted", "enabledRecentlySeen": False}},
            {"agent": {"access": "visible"}},
            {"canEnterScope": True}, {"canEnterScope": 0},
            {"scopeOptions": [{"scopeId": "other-org", "source": "role_grant"}]},
            {"scopeOptionsTruncated": True}, {"secret": "private-response-secret"},
        ):
            self.fixture.http = SyntheticSessionHTTP()
            self.fixture.token = "synthetic-admin-0"
            self.fixture.http.first_build_override = patch
            with self.subTest(patch=patch), self.assertRaisesRegex(RuntimeError, "restricted fields") as raised:
                self.configure()
            self.assertNotIn("private-response-secret", str(raised.exception))

    def test_independent_verification_refuses_missing_or_wrong_observer_identity(self):
        with self.assertRaisesRegex(RuntimeError, "read-only"):
            self.fixture.require_observer_session()
        self.fixture.observer_id = "observer-id"
        with self.assertRaisesRegex(RuntimeError, "observer identity"):
            self.fixture.require_observer_session()

    def test_denial_checks_reject_success_other_codes_and_session_failures_without_leaking_tokens(self):
        for status, code in ((200, 200), (200, 4031), (401, 4405), (500, 5000)):
            self.fixture.http = mock.Mock()
            self.fixture.http.open.return_value = FixtureResponse({"code": code, "detail": "private-response-secret"}, status)
            with self.subTest(status=status, code=code), self.assertRaises(RuntimeError) as raised:
                self.fixture.expect_rejected("/pipelines/fixture/trigger", 200, 4030, {}, "POST", "private-token-secret")
            self.assertNotIn("private-token-secret", str(raised.exception))
            self.assertNotIn("private-response-secret", str(raised.exception))


class FirstUseBrowserSequenceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.args = mock.Mock(browser_command=["node", "tests/actual-backend-acceptance.mjs"],
                              browser_cwd=self.root / "web", evidence_dir=self.root / "evidence")
        self.fixture = app.Fixture(self.args, self.root)
        self.fixture.commit = self.fixture.failed_commit = "a" * 40
        self.fixture.http = SyntheticSessionHTTP()
        self.fixture.token = "synthetic-admin-0"
        self.control = mock.Mock()
        self.control.poll.return_value = None
        self.agent = mock.Mock()
        self.agent.poll.return_value = None
        self.browser = mock.Mock(returncode=0)
        self.browser.poll.return_value = 0
        self.summary_path = self.args.evidence_dir / "browser/summary.json"
        self.summary_path.parent.mkdir(parents=True)

    def summary(self, phase="preflight"):
        return {"kind": "actual-backend", "status": "passed",
                "stage": "preflight-complete" if phase == "preflight" else "journey-complete",
                "firstBuildReadiness": {"journey": app.FIRST_BUILD_JOURNEY, "phase": phase,
                    "sourceCommit": self.fixture.failed_commit,
                    "steps": app.FIRST_BUILD_PREFLIGHT_STEPS + (app.FIRST_BUILD_CONFIGURED_STEPS if phase == "build" else [])}}

    def test_preflight_uses_same_command_then_refreshes_invalidated_admin_session(self):
        app.atomic_json(self.summary_path, self.summary())
        def start(name, command, cwd, env):
            self.assertEqual((name, command, cwd), ("browser-preflight", self.args.browser_command, self.args.browser_cwd))
            self.assertEqual(env["ARCENTRA_ACCEPTANCE_PHASE"], "preflight")
            self.assertEqual(env["ARCENTRA_ACCEPTANCE_COMMIT"], self.fixture.failed_commit)
            self.assertEqual(self.fixture.observer_id, "")
            request = urllib.request.Request(app.BACKEND_URL + "/api/v1/users/login", method="POST",
                data=json.dumps({"username": app.USERNAME, "password": app.encoded(app.PASSWORD)}).encode())
            self.fixture.http.open(request, timeout=10).close()
            self.fixture.expect_rejected("/users/fetch", 401, 4405, token="synthetic-admin-0")
            return self.browser
        with mock.patch.object(self.fixture, "start", side_effect=start):
            self.fixture.run_preflight_browser(self.control)
        self.assertTrue(self.fixture.preflight_verified)
        self.assertEqual(self.fixture.request("/users/fetch")["username"], app.USERNAME)
        logins = [call for call in self.fixture.http.calls if call[1] == "/users/login"]
        self.assertEqual(len(logins), 2, "browser login must be followed by a fresh Python administrator login")
        self.assertFalse(self.fixture.build_verified)
        self.assertFalse((self.fixture.coordination / "successful-run.json").exists())

    def test_preflight_failure_does_not_refresh_session_or_claim_verification(self):
        for browser_status, control_status, summary in (
            (1, None, self.summary()), (0, 2, self.summary()),
            (0, None, self.summary() | {"status": "failed"}),
            (0, None, self.summary("build")),
        ):
            self.browser.returncode = browser_status
            self.browser.poll.return_value = browser_status
            self.control.poll.return_value = control_status
            app.atomic_json(self.summary_path, summary)
            with self.subTest(browser_status=browser_status, control_status=control_status), \
                    mock.patch.object(self.fixture, "start", return_value=self.browser), \
                    mock.patch.object(self.fixture, "login") as login, self.assertRaises(RuntimeError):
                self.fixture.run_preflight_browser(self.control)
            login.assert_not_called()
            self.assertFalse(self.fixture.preflight_verified)

    def test_full_browser_is_mandatory_and_always_uses_build_phase(self):
        with mock.patch.object(self.fixture, "start") as start, self.assertRaisesRegex(RuntimeError, "requires"):
            self.fixture.run_build_browser(self.control, self.agent)
        start.assert_not_called()
        self.fixture.preflight_verified = True
        app.atomic_json(self.summary_path, self.summary("build"))
        with mock.patch.object(self.fixture, "start", return_value=self.browser) as start, \
                mock.patch.object(self.fixture, "verify_downloads") as verify:
            self.fixture.run_build_browser(self.control, self.agent)
        self.assertEqual(start.call_args.args[0], "browser")
        self.assertEqual(start.call_args.args[1], self.args.browser_command)
        self.assertEqual(start.call_args.args[3]["ARCENTRA_ACCEPTANCE_PHASE"], "build")
        verify.assert_called_once_with()

    def test_preflight_summary_cannot_substitute_for_full_build_completion(self):
        self.fixture.preflight_verified = True
        app.atomic_json(self.summary_path, self.summary())
        with mock.patch.object(self.fixture, "start", return_value=self.browser), \
                mock.patch.object(self.fixture, "verify_downloads") as verify, \
                self.assertRaisesRegex(RuntimeError, "exact successful"):
            self.fixture.run_build_browser(self.control, self.agent)
        verify.assert_not_called()

    def test_phase_configuration_rejects_unknown_values_and_does_not_inherit_parent_mode(self):
        for phase in ("", "skip", "preflight-only", None):
            with self.subTest(phase=phase), self.assertRaises(ValueError):
                self.fixture.browser_environment(phase)
        self.fixture.env["ARCENTRA_ACCEPTANCE_PHASE"] = "preflight"
        self.assertEqual(self.fixture.browser_environment("build")["ARCENTRA_ACCEPTANCE_PHASE"], "build")

    def test_summary_requires_exact_same_source_completed_steps_without_extra_claims(self):
        for phase in ("preflight", "build"):
            summary = self.summary(phase)
            app.validate_first_build_summary(summary, phase, self.fixture.failed_commit)
            for key, bad in (("phase", "other"), ("sourceCommit", "b" * 40), ("journey", "continuous-session"),
                             ("steps", []), ("steps", summary["firstBuildReadiness"]["steps"] + ["private-token-secret"]),
                             ("token", "private-token-secret")):
                changed = copy.deepcopy(summary)
                changed["firstBuildReadiness"][key] = bad
                with self.subTest(phase=phase, key=key), self.assertRaises(RuntimeError) as raised:
                    app.validate_first_build_summary(changed, phase, self.fixture.failed_commit)
                self.assertNotIn("private-token-secret", str(raised.exception))

    def test_exercise_orders_real_preflight_relogin_setup_observer_and_full_build(self):
        events = []
        app.atomic_json(self.summary_path, self.summary())
        identity = self.root / "identity/identity.json"
        identity.parent.mkdir()
        identity.write_text("synthetic durable identity")
        agent_reads = 0
        approved = False
        def request(path, payload=None, method="GET", **options):
            nonlocal agent_reads, approved
            events.append((method, path))
            if path == "/storage/configs" and method == "GET":
                return []
            if path == "/agent?pageNum=1&pageSize=100":
                agent_reads += 1
                if agent_reads == 1:
                    return {"count": 0, "agents": []}
                return {"count": 1, "agents": [{"agentId": "synthetic-agent", "isEnabled": int(approved),
                                                "lastHeartbeat": "synthetic-heartbeat" if approved else None}]}
            if path == "/registration-tokens":
                return {"token": "art_synthetic"}
            if path == "/agent/synthetic-agent/approve":
                approved = True
            return {}
        def start(name, *args):
            events.append(name)
            return self.browser if name == "browser-preflight" else self.agent
        def probe(url):
            return 503 if url.endswith("/ready") and not approved else 200
        with mock.patch.object(app, "verify_toolchain"), mock.patch.object(self.fixture, "create_tls"), \
                mock.patch.object(self.fixture, "run"), mock.patch.object(self.fixture, "stop"), \
                mock.patch.object(self.fixture, "start_control", return_value=self.control), \
                mock.patch.object(self.fixture, "login", side_effect=lambda: events.append("admin-login")), \
                mock.patch.object(self.fixture, "create_repository", side_effect=lambda: events.append("repository")) as source, \
                mock.patch.object(self.fixture, "start", side_effect=start), \
                mock.patch.object(self.fixture, "request", side_effect=request), \
                mock.patch.object(self.fixture, "probe", side_effect=probe), \
                mock.patch.object(self.fixture, "configure_observer", side_effect=lambda: events.append("observer")) as observer, \
                mock.patch.object(self.fixture, "run_build_browser", side_effect=lambda *args: events.append("full-build")) as build:
            self.fixture.exercise()
        source.assert_called_once_with()
        observer.assert_called_once_with()
        build.assert_called_once_with(self.control, self.agent)
        self.assertEqual(events.count("admin-login"), 3)
        first_write = events.index(("POST", "/storage/configs"))
        preflight = events.index("browser-preflight")
        self.assertLess(events.index("repository"), preflight)
        self.assertLess(preflight, events.index("admin-login", preflight))
        self.assertLess(events.index("admin-login", preflight), first_write)
        self.assertLess(first_write, events.index(("PUT", "/settings/AGENT_SECRET_KEY")))
        self.assertLess(events.index("agent-approved"), events.index("observer"))
        self.assertLess(events.index("observer"), events.index("full-build"))
        self.assertTrue(self.fixture.preflight_verified)

    def test_preflight_reauthentication_failure_stops_before_configuration(self):
        app.atomic_json(self.summary_path, self.summary())
        with mock.patch.object(self.fixture, "start", return_value=self.browser), \
                mock.patch.object(self.fixture, "login", side_effect=RuntimeError("login rejected")), \
                self.assertRaisesRegex(RuntimeError, "login rejected"):
            self.fixture.run_preflight_browser(self.control)
        self.assertEqual(self.fixture.http.calls, [], "no configuration API may follow rejected reauthentication")
        self.assertFalse(self.fixture.build_verified)

    def test_full_browser_error_does_not_verify_downloads(self):
        self.fixture.preflight_verified = True
        app.atomic_json(self.summary_path, self.summary("build"))
        self.browser.returncode = 1
        self.browser.poll.return_value = 1
        with mock.patch.object(self.fixture, "start", return_value=self.browser), \
                mock.patch.object(self.fixture, "verify_downloads") as verify, \
                self.assertRaisesRegex(RuntimeError, "browser acceptance exited"):
            self.fixture.run_build_browser(self.control, self.agent)
        verify.assert_not_called()

    def test_exercise_cannot_configure_or_enroll_after_preflight_error(self):
        events = []
        self.fixture.request = mock.Mock(return_value={})
        with mock.patch.object(app, "verify_toolchain"), mock.patch.object(self.fixture, "create_tls"), \
                mock.patch.object(self.fixture, "run"), mock.patch.object(self.fixture, "stop"), \
                mock.patch.object(self.fixture, "start_control", return_value=self.control), \
                mock.patch.object(self.fixture, "login"), \
                mock.patch.object(self.fixture, "create_repository", side_effect=lambda: events.append("repository")), \
                mock.patch.object(self.fixture, "run_preflight_browser", side_effect=RuntimeError("preflight failed")), \
                mock.patch.object(self.fixture, "configure_observer") as observer, \
                mock.patch.object(self.fixture, "run_build_browser") as build, \
                self.assertRaisesRegex(RuntimeError, "preflight failed"):
            self.fixture.exercise()
        self.assertEqual(events, ["repository"])
        self.assertEqual([call.args[0] for call in self.fixture.request.call_args_list], ["/users/register"])
        observer.assert_not_called()
        build.assert_not_called()


class RuntimeHookTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.args = mock.Mock(control_bin="control-bin", agent_bin="agent-bin", cli_bin="cli-bin",
                              evidence_dir=self.root / "evidence")
        self.fixture = app.Fixture(self.args, self.root)

    def test_default_runtime_hooks_preserve_host_commands_and_configuration(self):
        fixture = self.fixture
        with mock.patch.object(app, "verify_toolchain") as verify:
            fixture.verify_runtime_toolchain()
            verify.assert_called_once_with(fixture.env)
        fixture.write_control_configuration("synthetic-secret")
        self.assertEqual((self.root / "control.toml").read_text(), app.control_config(self.root, "synthetic-secret"))
        fixture.write_agent_configuration("art_synthetic")
        self.assertEqual((self.root / "agent.toml").read_text(), app.agent_config(self.root, "art_synthetic"))
        for path in ("control.toml", "plugins.toml", "agent.toml"):
            self.assertEqual((self.root / path).stat().st_mode & 0o777, 0o600)
        self.assertEqual(fixture.identity_path(), self.root / "identity/identity.json")
        with mock.patch.object(fixture, "run") as run:
            fixture.run_cli("migrate", ["migrate"])
            fixture.run_cli("bootstrap-admin", ["bootstrap-admin", "--username", app.USERNAME])
            self.assertEqual(run.call_args_list, [
                mock.call("migrate", ["cli-bin", "migrate", "--conf", str(self.root / "control.toml")]),
                mock.call("bootstrap-admin", ["cli-bin", "bootstrap-admin", "--conf",
                                               str(self.root / "control.toml"), "--username", app.USERNAME])])
        with mock.patch.object(fixture, "start") as start, mock.patch.object(fixture, "stop") as stop:
            process = fixture.start_agent("agent-pending")
            start.assert_called_once_with("agent-pending", ["agent-bin", "-conf", str(self.root / "agent.toml")])
            fixture.stop_application(process)
            stop.assert_called_once_with(process)
        self.assertEqual(fixture.repository_url(), app.REPO_URL)
        self.assertEqual(fixture.storage_configuration()["endpoint"], "127.0.0.1:19000")
        self.assertEqual(fixture.runtime_evidence(), {})
        self.assertEqual(fixture.browser_environment("build")["ARCENTRA_ACCEPTANCE_BACKEND_URL"], app.BACKEND_URL)
        self.assertNotIn("ARCENTRA_ACCEPTANCE_PROFILE", fixture.browser_environment("build"))

    def test_repository_hook_changes_only_source_and_browser_repository(self):
        fixture = self.fixture
        fixed_internal_repository = "git://git:9418/acceptance.git"
        with mock.patch.object(fixture, "repository_url", return_value=fixed_internal_repository), \
                mock.patch.object(fixture, "run"), mock.patch.object(fixture, "start_git_server") as serve, \
                mock.patch.object(app.subprocess, "run", return_value=mock.Mock(stdout="a" * 40 + "\n")):
            fixture.create_repository()
            spec = json.loads((self.root / "source/.arcentra/pipeline.yaml").read_text())
            expected = app.pipeline_spec()
            expected["jobs"][0]["source"]["repo"] = fixed_internal_repository
            self.assertEqual(spec, expected)
            serve.assert_called_once_with(self.root / "repositories")
            env = fixture.browser_environment("build")
            self.assertEqual(env["ARCENTRA_ACCEPTANCE_REPO_URL"], fixed_internal_repository)
            self.assertEqual(env["ARCENTRA_ACCEPTANCE_BACKEND_URL"], app.BACKEND_URL)
        self.assertIn(app.COMPILE_ERROR, (self.root / "source/main.go").read_text())
        self.assertEqual(fixture.failed_commit, fixture.commit)
        self.assertEqual(app.pipeline_spec()["jobs"][0]["source"]["repo"], app.REPO_URL)

    def test_default_cleanup_stops_children_in_reverse_before_evidence(self):
        fixture = self.fixture
        fixture.children = [mock.Mock(), mock.Mock()]
        with mock.patch.object(fixture, "stop") as stop:
            fixture.cleanup("synthetic failure")
            self.assertEqual(stop.call_args_list, [mock.call(child) for child in reversed(fixture.children)])
        result = json.loads((self.args.evidence_dir / "actual-app-result.json").read_text())
        self.assertNotIn("runtime", result)
        self.assertEqual(result["repository"], app.REPO_URL)
        self.assertEqual(result["limitations"], fixture.runtime_limitations())
        self.assertEqual(result["status"], "failed")

    def test_runtime_evidence_cannot_overwrite_shared_failure_verdict(self):
        fixture = self.fixture
        with mock.patch.object(fixture, "runtime_evidence", return_value={"status": "passed"}), \
                mock.patch.object(fixture, "cleanup_runtime") as cleanup:
            fixture.cleanup("synthetic failure")
            cleanup.assert_called_once_with()
        result = json.loads((self.args.evidence_dir / "actual-app-result.json").read_text())
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["error"], "synthetic failure")
        self.assertEqual(result["runtime"], {"status": "passed"})


class ActualAppFixtureTests(unittest.TestCase):
    def test_requires_explicit_disposable_runner_opt_in(self):
        valid = {"ARCENTRA_REAL_STACK": "1", "GITHUB_ACTIONS": "true",
                 "RUNNER_ENVIRONMENT": "github-hosted"}
        app.validate_environment(valid)
        for key in valid:
            with self.subTest(key=key), self.assertRaises(ValueError):
                app.validate_environment({k: v for k, v in valid.items() if k != key})
        with self.assertRaises(ValueError):
            app.validate_environment(valid | {"RUNNER_ENVIRONMENT": "self-hosted"})

    def test_refuses_every_dependency_override(self):
        valid = {"ARCENTRA_REAL_STACK": "1", "GITHUB_ACTIONS": "true",
                 "RUNNER_ENVIRONMENT": "github-hosted"}
        for key in app.DEPENDENCIES:
            with self.subTest(key=key), self.assertRaises(ValueError):
                app.validate_environment(valid | {key: "production.example.invalid"})

    def test_http_never_leaves_fixed_loopback_ports(self):
        for url in (app.BACKEND_URL + "/ready", app.AGENT_URL + "/live"):
            app.validate_http_url(url)
        for url in ("http://example.invalid:18080", "http://localhost:18080",
                    "http://127.0.0.1:80", "https://127.0.0.1:18080",
                    "http://user:secret@127.0.0.1:18080", "http://127.0.0.1:18080/#fragment"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                app.validate_http_url(url)

    def test_configs_preserve_authorization_tls_and_separate_identity(self):
        root = Path('/tmp/fixture with spaces')
        control = tomllib.loads(app.control_config(root, "synthetic-jwt"))
        agent = tomllib.loads(app.agent_config(root, "art_synthetic"))
        self.assertTrue(control["authz"]["enabled"])
        self.assertTrue(control["grpc"]["tls"]["enabled"])
        self.assertEqual(control["database"]["mysql"]["dsn"], app.DEPENDENCIES["ARCENTRA_TEST_MYSQL_DSN"])
        self.assertTrue(agent["grpc"]["tls"]["enabled"])
        self.assertEqual(agent["grpc"]["tls"]["serverName"], "arcentra-acceptance.local")
        self.assertEqual(agent["grpc"]["tls"]["caFile"], str(root / "ca.pem"))
        self.assertEqual(agent["agent"]["mode"], "baremetal")
        self.assertTrue(agent["agent"]["identityPersistence"]["enabled"])
        self.assertEqual(agent["agent"]["identityPersistence"]["directory"], str(root / "identity"))
        self.assertEqual(agent["agent"]["workspaceDir"], str(root / "workspaces"))
        self.assertEqual(tomllib.loads(app.agent_config(root, ""))["grpc"]["registrationToken"], "")
        for config in (control, agent):
            self.assertEqual(config["taskQueue"]["messageFormat"], "sonic")
            self.assertEqual(config["taskQueue"]["messageCodec"], "sonic")
            for listener in ("http", "metrics", "pprof"):
                self.assertEqual(config[listener]["host"], "127.0.0.1")
            self.assertFalse(config["metrics"]["enable"])
            self.assertFalse(config["pprof"]["enable"])

    def test_child_does_not_inherit_host_credentials_or_proxies(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = app.child_environment({"PATH": "/bin", "HOME": "/fixture-original",
                                         "AWS_SECRET_ACCESS_KEY": "not-for-child", "GH_TOKEN": "not-for-child",
                                         "HTTPS_PROXY": "not-for-child", "SSH_AUTH_SOCK": "not-for-child"}, root)
            for name in ("AWS_SECRET_ACCESS_KEY", "GH_TOKEN", "HTTPS_PROXY", "SSH_AUTH_SOCK"):
                self.assertNotIn(name, env)
            self.assertEqual(env["HOME"], str(root / "home"))
            self.assertEqual(env["PLAYWRIGHT_BROWSERS_PATH"], "/fixture-original/.cache/ms-playwright")
            self.assertEqual(env["GIT_CONFIG_NOSYSTEM"], "1")
            self.assertTrue(all(env[key] == value for key, value in app.GO_ENV.items()))
            self.assertNotIn("GOCACHE", env)
            self.assertNotIn("GOPATH", env)

    def test_source_and_all_steps_require_actual_agent_execution(self):
        spec = app.pipeline_spec()
        self.assertEqual(spec["namespace"], "acceptance")
        job = spec["jobs"][0]
        self.assertEqual(job["source"], {"type": "git", "repo": app.REPO_URL, "branch": "main"})
        self.assertTrue(all(step["runOnAgent"] for step in job["steps"]))
        self.assertEqual([step["name"] for step in job["steps"]], ["toolchain", "test", "build", "verify", "publish"])
        self.assertEqual([step["args"]["run"] for step in job["steps"][:-1]],
                         [f"sh build.sh {stage}" for stage in ("toolchain", "test", "build", "verify")])
        self.assertEqual(job["steps"][-1]["args"]["paths"], [app.ARTIFACT_NAME, app.MANIFEST_NAME])
        self.assertEqual(base64.b64decode(app.encoded(app.PASSWORD)).decode(), app.PASSWORD)

    def test_missing_go_fails_without_installing_or_running_anything(self):
        with mock.patch.object(app.shutil, "which", return_value=None), mock.patch.object(app.subprocess, "run") as run:
            with self.assertRaisesRegex(RuntimeError, "must already be installed"):
                app.verify_toolchain({"PATH": "/empty"})
            run.assert_not_called()

    def test_go_version_is_checked_in_sanitized_environment(self):
        env = {"PATH": "/fixture/bin"} | app.GO_ENV
        with mock.patch.object(app.shutil, "which", return_value="/fixture/bin/go"), mock.patch.object(app.subprocess, "run") as run:
            run.return_value = subprocess.CompletedProcess([], 0, "go version go1.25.6 linux/amd64\n", "")
            app.verify_toolchain(env)
            self.assertEqual(run.call_args.kwargs["env"], env)
            for stdout, code in (("go version go1.25.5 linux/amd64\n", 0),
                                 ("go version go1.25.6 darwin/arm64\n", 0),
                                 ("go version go1.25.6 linux/amd64\n", 1)):
                with self.subTest(stdout=stdout, code=code), self.assertRaises(RuntimeError):
                    run.return_value = subprocess.CompletedProcess([], code, stdout, "")
                    app.verify_toolchain(env)

    def test_source_copy_is_explicit_and_contains_no_prebuilt_output(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "source"
            app.copy_fixture_source(source)
            self.assertEqual(set(path.name for path in source.iterdir()),
                             {"go.mod", "main.go", "main_test.go", "build.sh", "verify.py", ".gitignore"})
            self.assertEqual((source / "go.mod").read_text(), "module example.invalid/arcentra/stdlib-demo\n\ngo 1.25.6\n")
            self.assertNotIn("acceptanceCompileError", (source / "main.go").read_text())
            script = (source / "build.sh").read_text()
            for guard in ("GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "GOENV=off", "CGO_ENABLED=0"):
                self.assertIn(guard, script)
            self.assertNotIn("|| true", script)
            self.assertIn("go test -count=1 ./...", script)
            self.assertIn("go build -trimpath -buildvcs=false -o stdlib-demo .", script)


class BuildContractTests(unittest.TestCase):
    def setUp(self):
        self.commit = "a" * 40
        self.binary = b"\x7fELFsynthetic bytes for validator tests, not an executable"
        self.manifest = {
            "schemaVersion": 1, "sourceCommit": self.commit, "goVersion": app.GO_VERSION,
            "binary": {"name": app.ARTIFACT_NAME, "sizeBytes": len(self.binary),
                       "sha256": hashlib.sha256(self.binary).hexdigest()},
            "behavior": {"args": ["arcentra"], "stdout": app.EXPECTED_OUTPUT,
                         "stdoutSha256": hashlib.sha256(app.EXPECTED_OUTPUT.encode()).hexdigest()},
        }
        self.receipt = {"schemaVersion": 1, "projectId": "project-id", "pipelineId": "pipeline-id",
                        "runId": "run-id", "sourceCommit": self.commit}
        self.run = {"runId": "run-id", "pipelineId": "pipeline-id", "status": "PIPELINE_STATUS_FAILED",
                    "commitSha": self.commit, "definitionCommitSha": self.commit, "allowDeployments": False}
        self.job = {"pipelineId": "pipeline-id", "pipelineRunId": "run-id", "agentId": "agent-id", "status": 5,
                    "jobRunId": "job-id", "sourceExpectationCaptured": True,
                    "sourceType": "git", "sourceRepository": app.REPO_URL, "sourceProvenanceState": "reported",
                    "sourceResolvedCommit": self.commit, "steps": [
                        {"name": name, "status": status, "exitCode": code, "jobRunId": "job-id",
                         "stepRunId": "step-" + name, "retryMode": ""}
                        for name, status, code in (("toolchain", 4, 0), ("test", 5, 1), ("build", 8, None),
                                                   ("verify", 8, None), ("publish", 8, None))]}

    def test_internal_image_repository_is_exact_and_host_validator_stays_host_only(self):
        internal = "git://git:9418/acceptance.git"
        job = self.job | {"sourceRepository": internal}
        jobs = {"total": 1, "list": [job]}
        artifacts = {"total": 0, "list": []}
        app._validate_run_snapshot(self.receipt, self.run, jobs, artifacts, False, "agent-id", internal)
        with self.assertRaises(ValueError):
            app.validate_run_snapshot(self.receipt, self.run, jobs, artifacts, False, "agent-id")
        for bad in ("git://git:9418/other.git", "git://example.com/acceptance.git", internal + "/",
                    "git://127.0.0.1:9418/acceptance.git", "git://git:9419/acceptance.git",
                    "git://user:token@git:9418/acceptance.git", internal + "?token=synthetic"):
            jobs["list"] = [self.job | {"sourceRepository": bad}]
            with self.subTest(repository=bad), self.assertRaisesRegex(ValueError, "fixed"):
                app._validate_run_snapshot(self.receipt, self.run, jobs, artifacts, False, "agent-id", bad)
        jobs["list"] = [self.job]
        with self.assertRaisesRegex(ValueError, "checkout evidence"):
            app._validate_run_snapshot(self.receipt, self.run, jobs, artifacts, False, "agent-id", internal)
        jobs["list"] = [job]
        with self.assertRaisesRegex(ValueError, "zero artifacts"):
            app._validate_run_snapshot(self.receipt, self.run, jobs, {"total": 1, "list": [{}]},
                                       False, "agent-id", internal)

    def test_manifest_checks_independent_full_commit_binary_and_behavior(self):
        self.assertEqual(app.validate_manifest(json.dumps(self.manifest), self.binary, self.commit), self.manifest)
        for path, wrong in ((("sourceCommit",), "b" * 40), (("goVersion",), "go1.25.5"),
                            (("schemaVersion",), True), (("schemaVersion",), 2),
                            (("binary", "sizeBytes"), len(self.binary) + 1),
                            (("binary", "sha256"), "0" * 64), (("binary", "name"), "../../program"),
                            (("behavior", "stdout"), "untrusted output"), (("behavior", "args"), ["other"]),
                            (("behavior", "stdoutSha256"), "f" * 64)):
            altered = copy.deepcopy(self.manifest)
            target = altered
            for key in path[:-1]:
                target = target[key]
            target[path[-1]] = wrong
            with self.subTest(path=path), self.assertRaises(ValueError):
                app.validate_manifest(json.dumps(altered), self.binary, self.commit)
        with self.assertRaises(ValueError):
            app.validate_manifest(json.dumps(self.manifest), self.binary + b"tampered", self.commit)
        with self.assertRaises(ValueError):
            app.validate_manifest(json.dumps(self.manifest), b"text artifact", self.commit)
        with self.assertRaises(ValueError):
            app.validate_manifest(json.dumps(self.manifest | {"extra": "unsupported"}), self.binary, self.commit)

    def test_duplicate_manifest_keys_rejected(self):
        raw = json.dumps(self.manifest).replace('"schemaVersion": 1', '"schemaVersion": 9, "schemaVersion": 1')
        with self.assertRaisesRegex(ValueError, "duplicate"):
            app.validate_manifest(raw, self.binary, self.commit)

    def test_receipt_rejects_bad_version_source_paths_and_extra_claims(self):
        app.validate_run_receipt(self.receipt, self.commit)
        for patch in ({"schemaVersion": True}, {"schemaVersion": 2}, {"sourceCommit": "b" * 40},
                      {"runId": "../../elsewhere"}, {"pipelineId": "a?token=b"}, {"status": "success"}):
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                app.validate_run_receipt(self.receipt | patch, self.commit)

    def test_private_evidence_is_bounded_regular_and_not_a_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "receipt.json"
            app.atomic_json(path, self.receipt)
            self.assertEqual(app.read_private_json(path), self.receipt)
            with self.assertRaises(ValueError):
                app.read_private_bytes(path, 1)
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                app.read_private_json(path)
            path.chmod(0o600)
            alias = Path(directory) / "symlink"
            alias.symlink_to(path)
            with self.assertRaises(OSError):
                app.read_private_json(alias)
            with self.assertRaises(ValueError):
                app.read_private_bytes(Path(directory), 4096)
            hardlink = Path(directory) / "hardlink"
            os.link(path, hardlink)
            with self.assertRaises(ValueError):
                app.read_private_json(hardlink)
            fifo = Path(directory) / "fifo"
            os.mkfifo(fifo, 0o600)
            with self.assertRaises(ValueError):
                app.read_private_bytes(fifo, 4096)

    def test_verified_execution_copies_exact_bytes_into_private_nonwritable_file(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = []
            def execute(command, **options):
                path = Path(command[0])
                paths.append(path)
                self.assertEqual(path.read_bytes(), self.binary)
                self.assertEqual(path.stat().st_mode & 0o777, 0o500)
                self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)
                self.assertEqual(options["timeout"], 5)
                return subprocess.CompletedProcess(command, 0 if command[1] == "arcentra" else 2,
                                                   app.EXPECTED_OUTPUT.encode() if command[1] == "arcentra" else b"",
                                                   b"" if command[1] == "arcentra" else b"invalid input\n")
            with mock.patch.object(app.subprocess, "run", side_effect=execute):
                app.verify_downloaded_program(self.binary, Path(directory), {})
            self.assertEqual(paths[0], paths[1])
            self.assertFalse(paths[0].exists())

    def validate_failure(self, run=None, job=None, artifacts=None):
        app.validate_run_snapshot(self.receipt, run or self.run, {"total": 1, "list": [job or self.job]},
                                  artifacts or {"total": 0, "list": []}, False, "agent-id")

    def test_compile_failure_requires_actual_failed_step_and_no_artifacts(self):
        self.validate_failure()
        for patch in ({"status": 3}, {"definitionCommitSha": "b" * 40}, {"runId": "old-run"}, {"allowDeployments": True}):
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                self.validate_failure(run=self.run | patch)
        for patch in ({"agentId": "other-agent"}, {"sourceResolvedCommit": "b" * 40}, {"sourceProvenanceState": "unknown"}):
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                self.validate_failure(job=self.job | patch)
        for index, patch in ((1, {"exitCode": 0}), (1, {"status": 4}), (4, {"status": 4})):
            job = copy.deepcopy(self.job)
            job["steps"][index].update(patch)
            with self.subTest(index=index, patch=patch), self.assertRaises(ValueError):
                self.validate_failure(job=job)
        with self.assertRaises(ValueError):
            self.validate_failure(artifacts={"total": 1, "list": [{"name": app.ARTIFACT_NAME}]})

    def test_success_requires_two_current_run_artifacts_and_every_step_success(self):
        run = self.run | {"status": 3}
        job = copy.deepcopy(self.job)
        job["status"] = 4
        for step in job["steps"]:
            step.update(status=4, exitCode=0)
        artifacts = {"total": 2, "list": [{"name": name, "projectId": "project-id", "pipelineId": "pipeline-id",
                                            "jobRunId": "job-id", "stepRunId": "step-publish",
                                            "pipelineRunId": "run-id", "status": "available", "downloadAvailable": True}
                                           for name in (app.ARTIFACT_NAME, app.MANIFEST_NAME)]}
        app.validate_run_snapshot(self.receipt, run, {"total": 1, "list": [job]}, artifacts, True, "agent-id")
        artifacts["list"][0]["pipelineRunId"] = "previous-success"
        with self.assertRaises(ValueError):
            app.validate_run_snapshot(self.receipt, run, {"total": 1, "list": [job]}, artifacts, True, "agent-id")

    def test_missing_failure_receipt_cannot_authorize_successful_download_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = app.Fixture(mock.Mock(), Path(directory))
            app.atomic_json(fixture.coordination / "successful-run.json", self.receipt)
            with mock.patch.object(app.subprocess, "run") as run, self.assertRaises(RuntimeError):
                fixture.verify_downloads()
            run.assert_not_called()

    def test_public_build_evidence_is_strict_success_only_allowlist(self):
        failed = self.receipt | {"sourceCommit": "b" * 40, "runId": "failed-run"}
        value = app.public_build_verification(failed, self.receipt, self.manifest, True)
        self.assertEqual(set(value), {"contract", "status", "sourceCommits", "runs", "goVersion", "binary", "behavior", "checks"})
        self.assertEqual(value["status"], "passed")
        self.assertTrue(all(flag is True for flag in value["checks"].values()))
        for key, forbidden in (("headers", {"Authorization": "secret"}), ("path", "/private/key"),
                               ("url", "https://example.invalid/private"), ("error", "raw secret")):
            with self.subTest(key=key), self.assertRaises(ValueError):
                app.public_build_verification(failed, self.receipt, self.manifest | {key: forbidden}, True)
            with self.subTest(receipt_key=key), self.assertRaises(ValueError):
                app.public_build_verification(failed | {key: forbidden}, self.receipt, self.manifest, True)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "browser/project-build-verification.json"
            self.assertTrue(app.write_build_verification(path, None, True, failed, self.receipt, self.manifest))
            self.assertEqual(app.read_private_json(path), value)
            for error, verified in (("private failure", True), (None, False)):
                self.assertFalse(app.write_build_verification(path, error, verified, failed, self.receipt, self.manifest))
                self.assertFalse(path.exists())
            with self.assertRaises(ValueError):
                app.write_build_verification(path, None, True, failed, self.receipt, self.manifest | {"token": "secret"})
            self.assertFalse(path.exists())




class ReadinessProbeTransportTests(unittest.TestCase):
    def fixture(self):
        fixture = object.__new__(app.Fixture)
        fixture.http = mock.Mock()
        fixture.token = 'synthetic-test-token'
        return fixture

    def test_transient_reset_and_disconnect_then_readiness(self):
        fixture = self.fixture()
        response = mock.MagicMock()
        response.__enter__.return_value.status = 200
        fixture.http.open.side_effect = [ConnectionResetError(104, 'reset'), http.client.RemoteDisconnected(), response]
        process = mock.Mock(returncode=None)
        process.poll.return_value = None
        with mock.patch.object(app.time, 'sleep'):
            self.assertTrue(fixture.wait_until('readiness', lambda: fixture.probe(app.BACKEND_URL + '/ready') == 200,
                                               process, timeout=1))
        self.assertEqual(fixture.http.open.call_count, 3)
        self.assertEqual(process.poll.call_count, 3)

    def test_persistent_reset_remains_bounded(self):
        fixture = self.fixture()
        fixture.http.open.side_effect = ConnectionResetError(104, 'reset')
        process = mock.Mock(returncode=None)
        process.poll.return_value = None
        with mock.patch.object(app.time, 'monotonic', side_effect=[0, 0, 0.5, 1]), mock.patch.object(app.time, 'sleep'):
            with self.assertRaisesRegex(RuntimeError, 'timed out waiting'):
                fixture.wait_until('readiness', lambda: fixture.probe(app.BACKEND_URL + '/ready') == 200, process, timeout=1)
        self.assertEqual(fixture.http.open.call_count, 2)

    def test_exited_process_fails_before_probe(self):
        fixture = self.fixture()
        process = mock.Mock(returncode=1)
        process.poll.return_value = 1
        with self.assertRaisesRegex(RuntimeError, 'process exited 1'):
            fixture.wait_until('readiness', lambda: fixture.probe(app.BACKEND_URL + '/ready'), process)
        fixture.http.open.assert_not_called()

    def test_mutation_reset_is_never_retried_or_suppressed(self):
        fixture = self.fixture()
        fixture.http.open.side_effect = ConnectionResetError(104, 'reset')
        with self.assertRaises(ConnectionResetError):
            fixture.request('/users/register', {'username': 'synthetic'}, 'POST', authenticated=False)
        self.assertEqual(fixture.http.open.call_count, 1)


@unittest.skipUnless(os.environ.get('ARCENTRA_TEST_LOOPBACK_PROBES') == '1',
                     'real loopback TCP tests require explicit authorized CI opt-in')
class LoopbackReadinessProbeTests(unittest.TestCase):
    def exercise_server(self, always_reset):
        counts = []
        class Handler(socketserver.BaseRequestHandler):
            def handle(self):
                self.request.settimeout(1)
                self.request.recv(4096)
                counts.append(1)
                if always_reset or len(counts) == 1:
                    self.request.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack('ii', 1, 0))
                    return
                self.request.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
        server = socketserver.TCPServer(('127.0.0.1', 0), Handler)
        server.timeout = 1
        thread = threading.Thread(target=lambda: server.serve_forever(poll_interval=0.01), daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(lambda: thread.join(timeout=2))
        self.addCleanup(server.shutdown)
        opener = app.urllib.request.build_opener(app.urllib.request.ProxyHandler({}), app.NoRedirects())
        fixture = object.__new__(app.Fixture)
        # Keep the production guard on its exact fixed URL, and adapt only the
        # test transport to an exclusively owned ephemeral loopback listener.
        class Transport:
            def open(self, url, timeout):
                self_url = app.BACKEND_URL + '/ready'
                if url != self_url:
                    raise AssertionError('unexpected test request')
                return opener.open('http://127.0.0.1:%d/ready' % server.server_address[1], timeout=timeout)
        fixture.http = Transport()
        process = mock.Mock(returncode=None)
        process.poll.return_value = None
        started = time.monotonic()
        if always_reset:
            with self.assertRaisesRegex(RuntimeError, 'timed out waiting'):
                fixture.wait_until('real reset readiness', lambda: fixture.probe(app.BACKEND_URL + '/ready') == 200,
                                   process, timeout=0.1)
            self.assertLess(time.monotonic() - started, 2)
        else:
            self.assertTrue(fixture.wait_until('real reset then ready', lambda: fixture.probe(app.BACKEND_URL + '/ready') == 200,
                                               process, timeout=3))
            self.assertGreaterEqual(len(counts), 2)

    def test_actual_tcp_reset_then_ready(self):
        self.exercise_server(False)

    def test_actual_tcp_persistent_reset_bounded(self):
        self.exercise_server(True)


if __name__ == "__main__":
    unittest.main()
