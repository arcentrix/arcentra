#!/usr/bin/env python3
"""Exercise a fresh installation using the real CLI, Control, Agent and browser.

This is intentionally a disposable GitHub-hosted runner fixture, not a deployment
helper. It never seeds product tables, disables authorization, or supplies mocks.
The caller starts the loopback dependency services and creates the S3 bucket.
"""

import argparse
import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


BACKEND_URL = "http://127.0.0.1:18080"
AGENT_URL = "http://127.0.0.1:18081"
REPO_URL = "git://127.0.0.1:19418/acceptance.git"
USERNAME = "ci-admin"
PASSWORD = "Ci-Only-Password-123!"
OBSERVER_USERNAME = "ci-build-observer"
OBSERVER_ROLE = "ci-build-observer-read"
BUILD_CONTRACT = "stdlib-build-v1"
GO_VERSION = "go1.25.6"
ARTIFACT_NAME = "stdlib-demo"
MANIFEST_NAME = "manifest.json"
EXPECTED_OUTPUT = '{"message":"HELLO, ARCENTRA!","length":8}\n'
FIXTURE_SOURCE = Path(__file__).resolve().parent / "fixtures/stdlib-build"
COMPILE_ERROR = "\nvar _ = acceptanceCompileError\n"
MAX_BINARY_BYTES = 32 * 1024 * 1024
MAX_IDENTITY_BYTES = 16 * 1024
FIRST_BUILD_JOURNEY = "three-phase-separate-browser-contexts"
FIRST_BUILD_PREFLIGHT_STEPS = [
    "preflight-login-via-real-api",
    "preflight-missing-configuration-no-agent-no-scope-manual-scope-allowed",
    "preflight-explicit-status-refresh",
    "preflight-no-product-mutations",
]
FIRST_BUILD_STORAGE_STEPS = [
    "storage-login-via-real-api",
    "storage-create-via-ui-redacted-response",
    "storage-edit-metadata-preserves-blank-credentials",
    "storage-explicit-default-and-reload",
    "storage-no-secret-in-page-or-diagnostics",
]
FIRST_BUILD_CONFIGURED_STEPS = [
    "configured-persisted-storage-and-signing-recent-agent",
    "configured-explicit-status-refresh",
]
GO_ENV = {"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off",
          "GOENV": "off", "CGO_ENABLED": "0", "GOTELEMETRY": "off", "GOMAXPROCS": "2"}
DEPENDENCIES = {
    "ARCENTRA_TEST_MYSQL_DSN": "root:ci-only-mysql-password@tcp(127.0.0.1:13306)/arcentra_acceptance?charset=utf8mb4&parseTime=True&loc=UTC",
    "ARCENTRA_TEST_KAFKA_BROKERS": "127.0.0.1:19092",
    "ARCENTRA_TEST_REDIS_ADDRESS": "127.0.0.1:16379",
    "ARCENTRA_TEST_S3_ENDPOINT": "http://127.0.0.1:19000",
    "ARCENTRA_TEST_S3_ACCESS_KEY": "ci-only-s3-user",
    "ARCENTRA_TEST_S3_SECRET_KEY": "ci-only-s3-password",
    "ARCENTRA_TEST_S3_BUCKET": "acceptance",
}


def validate_environment(env):
    """Refuse accidental execution on a workstation or non-fixture services."""
    if env.get("ARCENTRA_REAL_STACK") != "1":
        raise ValueError("ARCENTRA_REAL_STACK=1 is required")
    if env.get("GITHUB_ACTIONS") != "true" or env.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise ValueError("actual-binary acceptance requires a disposable GitHub-hosted runner")
    for key, expected in DEPENDENCIES.items():
        if env.get(key, expected) != expected:
            raise ValueError(f"{key} must use the documented synthetic loopback fixture value")


def validate_http_url(url):
    parsed = urllib.parse.urlsplit(url)
    if (parsed.scheme != "http" or parsed.hostname != "127.0.0.1"
            or parsed.port not in (18080, 18081) or parsed.username
            or parsed.password or parsed.fragment):
        raise ValueError("HTTP fixture requests must use the fixed loopback Control/Agent ports")


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("acceptance HTTP redirects are forbidden")


class Fixture:
    def __init__(self, args, root):
        self.args = args
        self.root = root
        self.children = []
        self.logs = []
        self.redactions = [PASSWORD, encoded(PASSWORD), DEPENDENCIES["ARCENTRA_TEST_S3_SECRET_KEY"],
                           "ci-only-mysql-password"]
        self.token = ""
        self.observer_id = ""
        self.steps = []
        self.agent_id = ""
        self.commit = ""
        self.failed_commit = ""
        self.failed_run = None
        self.successful_run = None
        self.artifact_evidence = None
        self.build_verified = False
        self.preflight_verified = False
        self.storage_setup_verified = False
        self.coordination = root / "coordination"
        self.coordination.mkdir(mode=0o700)
        self.redactions.extend([DEPENDENCIES["ARCENTRA_TEST_S3_ACCESS_KEY"],
                                encoded(DEPENDENCIES["ARCENTRA_TEST_S3_ACCESS_KEY"]),
                                encoded(DEPENDENCIES["ARCENTRA_TEST_S3_SECRET_KEY"])])
        self.env = child_environment(os.environ, root)
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirects())

    def note(self, step):
        self.steps.append(step)
        print(f"[actual-app] {step}", flush=True)

    def write_private(self, name, content):
        path = self.root / name
        path.write_text(content)
        path.chmod(0o600)
        return path

    def start(self, name, command, cwd=None, env=None):
        logfile = self.root / f"{name}.log"
        handle = logfile.open("wb")
        self.logs.append((logfile, handle))
        process = subprocess.Popen(command, cwd=cwd or self.root, env=env or self.env,
                                   stdin=subprocess.DEVNULL, stdout=handle,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        self.children.append(process)
        return process

    def run(self, name, command, cwd=None, timeout=120):
        process = self.start(name, command, cwd)
        try:
            code = process.wait(timeout=timeout)
        except subprocess.TimeoutExpired as exc:
            raise RuntimeError(f"{name} timed out; see sanitized evidence") from exc
        if code != 0:
            raise RuntimeError(f"{name} exited {code}; see sanitized evidence")

    @staticmethod
    def stop(process):
        if process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                return
            try:
                process.wait(timeout=35)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=10)

    def request(self, path, payload=None, method="GET", authenticated=True, token=None):
        url = BACKEND_URL + "/api/v1" + path
        validate_http_url(url)
        headers = {"Content-Type": "application/json"}
        if authenticated:
            active_token = self.token if token is None else token
            if not active_token:
                raise RuntimeError("authenticated fixture request has no login token")
            headers["Authorization"] = "Bearer " + active_token
        data = None if payload is None else json.dumps(payload).encode()
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        with self.http.open(req, timeout=10) as response:
            body = json.load(response)
        if body.get("code") != 200 or "errMsg" in body:
            # Never put response bodies (which may contain returned credentials)
            # into CI output, exceptions or saved receipts.
            raise RuntimeError(f"{method} {path} failed with application code {body.get('code')}")
        return body.get("detail")

    def expect_rejected(self, path, http_status, code, payload=None, method="GET", token=None):
        """Require the exact real denial, not an arbitrary error or a stale session."""
        url = BACKEND_URL + "/api/v1" + path
        validate_http_url(url)
        active_token = self.token if token is None else token
        if not active_token:
            raise RuntimeError("denial check requires an authenticated fixture token")
        request = urllib.request.Request(url, method=method,
                                         data=None if payload is None else json.dumps(payload).encode(),
                                         headers={"Authorization": "Bearer " + active_token,
                                                  "Content-Type": "application/json"})
        try:
            response = self.http.open(request, timeout=10)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            status, body = response.code, json.load(response)
        if status != http_status or body.get("code") != code:
            # Do not include returned bodies, request headers or token values.
            raise RuntimeError(f"{method} {path} did not return the required authorization denial")

    def probe(self, url):
        validate_http_url(url)
        try:
            with self.http.open(url, timeout=2) as response:
                return response.status
        except urllib.error.HTTPError as exc:
            return exc.code
        except (urllib.error.URLError, TimeoutError, ConnectionError, http.client.RemoteDisconnected):
            return None

    def wait_until(self, description, predicate, process=None, timeout=90):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if process is not None and process.poll() is not None:
                raise RuntimeError(f"{description}: process exited {process.returncode}; see sanitized evidence")
            value = predicate()
            if value:
                return value
            time.sleep(0.5)
        raise RuntimeError(f"timed out waiting for {description}; see sanitized evidence")

    def start_control(self, name):
        process = self.start(name, [self.args.control_bin, "-conf", str(self.root / "control.toml"),
                                    "-plugin-conf", str(self.root / "plugins.toml")])
        self.wait_until("Control readiness", lambda: self.probe(BACKEND_URL + "/ready") == 200, process)
        return process

    def verify_runtime_toolchain(self):
        """Verify the host profile's exact toolchain before any application work."""
        verify_toolchain(self.env)
        self.note("Preinstalled Go 1.25.6 verified in the actual sanitized Agent child environment")

    def write_control_configuration(self, jwt_secret):
        self.write_private("control.toml", control_config(self.root, jwt_secret))
        self.write_private("plugins.toml", '[builtins.shell]\nshell = "/bin/sh"\ntimeout = 120\n'
                           '[plugins.git]\ngitPath = "git"\ntimeout = 60\nshallow = false\n')

    def write_agent_configuration(self, registration_token):
        self.write_private("agent.toml", agent_config(self.root, registration_token))

    def run_cli(self, name, arguments):
        self.run(name, [self.args.cli_bin, arguments[0], "--conf",
                        str(self.root / "control.toml"), *arguments[1:]])

    def start_agent(self, name):
        return self.start(name, [self.args.agent_bin, "-conf", str(self.root / "agent.toml")])

    def stop_application(self, process):
        self.stop(process)

    def identity_path(self):
        return self.root / "identity/identity.json"

    def repository_url(self):
        return REPO_URL

    def storage_configuration(self):
        return {"endpoint": "127.0.0.1:19000", "accessKey": DEPENDENCIES["ARCENTRA_TEST_S3_ACCESS_KEY"],
                "secretKey": DEPENDENCIES["ARCENTRA_TEST_S3_SECRET_KEY"], "bucket": "acceptance",
                "region": "us-east-1", "useTLS": False, "basePath": "actual-app"}

    def runtime_evidence(self):
        """Additional runtime facts cannot override the shared journey verdict."""
        return {}

    def runtime_limitations(self):
        return ["Organization creation is not implemented; current admin project API accepts a synthetic orgId.",
                "Trusted baremetal commands run with Agent host permissions on a disposable runner.",
                "Module downloads are disabled; this does not prove host network isolation or container builds.",
                "Manifest is an unsigned integrity record, not a provenance attestation."]

    def cleanup_runtime(self):
        for process in reversed(self.children):
            self.stop(process)

    def login(self, username=USERNAME):
        detail = self.request("/users/login", {"username": username, "password": encoded(PASSWORD)},
                              "POST", authenticated=False)
        self.token = detail["token"]["accessToken"]
        self.redactions.extend(detail["token"].values())

    def configure_observer(self):
        """Create a read-only synthetic session through existing authorized APIs.

        Login replaces the one active token per user. The browser alone owns the
        administrator session; independent snapshots/downloads use this separate
        disposable identity. No second administrator or database seeding is used.
        """
        admin_token = self.token
        admin = self.request("/users/fetch")
        if admin.get("username") != USERNAME:
            raise RuntimeError("observer setup requires the synthetic fixture administrator")
        self.request("/role", {"roleId": OBSERVER_ROLE, "name": OBSERVER_ROLE,
                               "description": "Disposable actual-build read-only observer",
                               "scope": "platform", "isEnabled": 1, "permissions": []}, "POST")
        email = OBSERVER_USERNAME + "@example.invalid"
        invited = self.request("/users/invite", {"email": email, "role": OBSERVER_ROLE}, "POST")
        observer_id = safe_identifier(invited.get("userId"))
        if invited.get("email") != email or invited.get("role") != OBSERVER_ROLE or observer_id == admin.get("userId"):
            raise RuntimeError("observer invitation did not create the intended distinct fixture identity")
        # This endpoint initializes only the newly invited, empty-database test
        # account's synthetic password. It never targets a real user credential.
        self.request(f"/users/{observer_id}/password", {"newPassword": encoded(PASSWORD)}, "PUT")
        # Invite grants do not synchronously reload Casbin. Applying this exact
        # sole permission after the grant uses the existing API reload path.
        self.request(f"/role/{OBSERVER_ROLE}/permissions", {"permissions": ["pipeline:read"]}, "PUT")
        role = self.request(f"/role/{OBSERVER_ROLE}")
        if (role.get("roleId") != OBSERVER_ROLE or role.get("permissions") != ["pipeline:read"]
                or role.get("scope") != "platform" or role.get("isEnabled") != 1 or role.get("isBuiltin") != 0):
            raise RuntimeError("observer must have exactly the non-administrator pipeline-read role")
        self.observer_id = observer_id
        self.login(OBSERVER_USERNAME)
        self.require_observer_session()
        old_observer_token = self.token
        # JWT issue/expiry timestamps have second precision; a same-second login
        # can reproduce the same token. This bounded pause makes revocation real.
        time.sleep(1.1)
        self.login(OBSERVER_USERNAME)
        if self.token == old_observer_token:
            raise RuntimeError("observer relogin did not rotate its synthetic session")
        self.expect_rejected("/users/fetch", 401, 4405, token=old_observer_token)
        self.require_observer_session()
        self.expect_rejected("/users/?pageNum=1&pageSize=1", 403, 403)
        self.expect_rejected("/storage/configs", 403, 403)
        validate_restricted_first_build(self.request("/me/first-build"))
        self.note("Read-only observer first-build guidance omits configuration and Agent state")
        if self.request("/users/fetch", token=admin_token) != admin:
            raise RuntimeError("observer login interfered with the administrator session")
        self.note("Separate synthetic pipeline-read observer verified; relogin revokes only its own prior session")

    def require_observer_session(self):
        if not self.observer_id:
            raise RuntimeError("independent build verification requires its read-only fixture observer")
        profile = self.request("/users/fetch")
        if profile.get("userId") != self.observer_id or profile.get("username") != OBSERVER_USERNAME:
            raise RuntimeError("independent build verification is not using the fixture observer identity")

    def create_tls(self):
        self.run("tls-ca", ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                             "-keyout", "ca.key", "-out", "ca.pem", "-days", "1",
                             "-subj", "/CN=Arcentra-Acceptance-CA",
                             "-addext", "basicConstraints=critical,CA:TRUE",
                             "-addext", "keyUsage=critical,keyCertSign,cRLSign"])
        self.run("tls-request", ["openssl", "req", "-newkey", "rsa:2048", "-nodes",
                                  "-keyout", "server.key", "-out", "server.csr",
                                  "-subj", "/CN=arcentra-acceptance.local"])
        self.write_private("server.ext", "subjectAltName=DNS:arcentra-acceptance.local,IP:127.0.0.1\n"
                           "extendedKeyUsage=serverAuth\nbasicConstraints=critical,CA:FALSE\n")
        self.run("tls-sign", ["openssl", "x509", "-req", "-in", "server.csr", "-CA", "ca.pem",
                               "-CAkey", "ca.key", "-CAcreateserial", "-out", "server.pem",
                               "-days", "1", "-extfile", "server.ext"])
        self.run("tls-verify", ["openssl", "verify", "-CAfile", "ca.pem", "-verify_hostname",
                                 "arcentra-acceptance.local", "server.pem"])

    def create_repository(self):
        source = self.root / "source"
        copy_fixture_source(source)
        (source / ".arcentra").mkdir(parents=True)
        (source / "main.go").write_text((source / "main.go").read_text() + COMPILE_ERROR)
        spec = pipeline_spec()
        spec["jobs"][0]["source"]["repo"] = self.repository_url()
        (source / ".arcentra/pipeline.yaml").write_text(json.dumps(spec, indent=2) + "\n")
        self.run("git-init", ["git", "init", "--initial-branch=main", str(source)])
        self.run("git-add", ["git", "add", "."], source)
        self.run("git-commit", ["git", "-c", "user.name=Acceptance Fixture", "-c",
                                "user.email=acceptance@example.invalid", "commit", "-m",
                                "Synthetic project with intentional compiler failure"], source)
        result = subprocess.run(["git", "rev-parse", "HEAD"], cwd=source, env=self.env,
                                capture_output=True, text=True, check=True, timeout=10)
        self.commit = result.stdout.strip()
        self.failed_commit = self.commit
        repositories = self.root / "repositories"
        repositories.mkdir()
        self.run("git-bare", ["git", "clone", "--bare", str(source), str(repositories / "acceptance.git")])
        self.start_git_server(repositories)

    def start_git_server(self, repositories):
        daemon = self.start("git-daemon", ["git", "daemon", "--listen=127.0.0.1", "--port=19418",
                                           "--export-all", f"--base-path={repositories}", str(repositories)])
        def reachable():
            result = subprocess.run(["git", "ls-remote", self.repository_url(), "refs/heads/main"],
                                    env=self.env, capture_output=True, text=True, timeout=5)
            return result.returncode == 0 and self.commit in result.stdout
        self.wait_until("loopback Git source", reachable, daemon, timeout=20)
        self.note("Synthetic Git source is served read-only on loopback")

    def run_snapshot(self, receipt, successful):
        """Independently read scoped API evidence; never trust browser status claims."""
        validate_run_receipt(receipt, self.commit)
        self.require_observer_session()
        base = f"/pipelines/{receipt['pipelineId']}/runs/{receipt['runId']}"
        run = self.request(base)
        jobs = self.request(base + "/jobs")
        artifacts = self.request(f"/projects/{receipt['projectId']}/artifacts?" +
                                 urllib.parse.urlencode({"runId": receipt["runId"], "pageSize": 100}))
        _validate_run_snapshot(receipt, run, jobs, artifacts, successful, self.agent_id, self.repository_url())
        logs = self.request(base + "/logs?limit=1000")
        messages = "\n".join(entry.get("message", "") for entry in logs.get("entries", []))
        expected = ["ACTUAL_PROJECT_TOOLCHAIN go1.25.6", "ACTUAL_PROJECT_TEST"]
        if successful:
            expected.extend(["ACTUAL_PROJECT_BUILD", "ACTUAL_PROJECT_BEHAVIOR", EXPECTED_OUTPUT.strip()])
        else:
            expected.append("undefined: acceptanceCompileError")
        if any(marker not in messages for marker in expected):
            raise RuntimeError("actual Agent logs lack required compiler/behavior evidence")
        return artifacts["list"]

    def fix_repository_after_failed_run(self):
        receipt = read_private_json(self.coordination / "failed-run.json")
        self.run_snapshot(receipt, successful=False)
        # The current observer can read the real run but cannot trigger even this
        # synthetic pipeline. Legacy permission middleware uses HTTP 200/4030.
        self.expect_rejected(f"/pipelines/{receipt['pipelineId']}/trigger", 200, 4030, {}, "POST")
        self.failed_run = receipt
        self.note("Actual Agent compiler failure persisted with failed test step and zero run artifacts")
        source = self.root / "source"
        (source / "main.go").write_bytes((FIXTURE_SOURCE / "main.go").read_bytes())
        self.run("git-fix-add", ["git", "add", "main.go"], source)
        self.run("git-fix-commit", ["git", "-c", "user.name=Acceptance Fixture", "-c",
                                    "user.email=acceptance@example.invalid", "commit", "-m",
                                    "Fix compiler error in synthetic project"], source)
        result = subprocess.run(["git", "rev-parse", "HEAD"], cwd=source, env=self.env,
                                capture_output=True, text=True, check=True, timeout=10)
        self.commit = result.stdout.strip()
        if self.commit == self.failed_commit:
            raise RuntimeError("source fix must create a distinct Git commit")
        # This writes only the disposable local bare repository; the Git daemon
        # remains read-only. No GitHub or other external remote is involved.
        self.run("git-publish-fix", ["git", "push", str(self.root / "repositories/acceptance.git"),
                                     "HEAD:refs/heads/main"], source)
        atomic_json(self.coordination / "fixed-source.json", {"schemaVersion": 1, "sourceCommit": self.commit})
        self.note("Corrected source committed after the failed run; browser must review the new full revision")

    def verify_downloads(self):
        receipt = read_private_json(self.coordination / "successful-run.json")
        if not self.failed_run or any(receipt.get(key) != self.failed_run[key]
                                      for key in ("projectId", "pipelineId")):
            raise RuntimeError("successful run must use the original project and pipeline")
        if receipt.get("runId") == self.failed_run["runId"]:
            raise RuntimeError("source fix requires a new run, not mutation of the failed run")
        artifacts = self.run_snapshot(receipt, successful=True)
        downloads = self.coordination / "downloads"
        info = downloads.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077:
            raise RuntimeError("browser downloads must be in the private fixture directory")
        binary = read_private_bytes(downloads / ARTIFACT_NAME, MAX_BINARY_BYTES)
        manifest_bytes = read_private_bytes(downloads / MANIFEST_NAME, 4096)
        manifest = validate_manifest(manifest_bytes, binary, self.commit)
        expected = {ARTIFACT_NAME: binary, MANIFEST_NAME: manifest_bytes}
        for artifact in artifacts:
            if artifact.get("sizeKnown") is not True or artifact.get("size") != len(expected[artifact["name"]]):
                raise RuntimeError("artifact byte length differs from its persisted metadata")
            artifact_id = safe_identifier(artifact.get("artifactId"))
            url = BACKEND_URL + f"/api/v1/projects/{receipt['projectId']}/artifacts/{artifact_id}/download"
            validate_http_url(url)
            request = urllib.request.Request(url, headers={"Authorization": "Bearer " + self.token})
            with self.http.open(request, timeout=30) as response:
                data = response.read(MAX_BINARY_BYTES + 1)
                if response.status != 200 or data != expected[artifact["name"]]:
                    raise RuntimeError("browser bytes differ from independent authenticated artifact download")
        # Only this reviewed, synthetic, current-run binary is executable. Never
        # execute an arbitrary project artifact or a pathname supplied by an API.
        verify_downloaded_program(binary, self.root, self.env)
        # Re-read the original failed run after success, so a later publication
        # cannot masquerade as a clean failure or contaminate the artifact filter.
        current = self.commit
        self.commit = self.failed_commit
        try:
            self.run_snapshot(self.failed_run, successful=False)
        finally:
            self.commit = current
        self.successful_run = receipt
        self.artifact_evidence = manifest
        self.build_verified = True
        self.note("Browser-downloaded binary independently matched API bytes, manifest, fixed commit and program behavior")

    def exercise(self):
        self.verify_runtime_toolchain()
        self.create_tls()
        jwt_secret = secrets.token_hex(32)
        self.redactions.append(jwt_secret)
        self.write_control_configuration(jwt_secret)
        self.run_cli("migrate", ["migrate"])
        self.note("Real CLI migration completed; no account or product data seeded")
        control = self.start_control("control-before-bootstrap")
        self.request("/users/register", {"username": USERNAME, "email": "ci-admin@example.invalid",
                                         "fullName": "Acceptance Administrator", "password": encoded(PASSWORD)},
                     "POST", authenticated=False)
        self.stop_application(control)
        self.run_cli("bootstrap-admin", ["bootstrap-admin", "--username", USERNAME])
        control = self.start_control("control-after-bootstrap")
        self.login()
        self.note("HTTP registration, explicit CLI administrator bootstrap and fresh login completed")
        # The same real, intentionally broken Git source supplies all browser
        # phases. It is not built or changed until the original full journey.
        self.create_repository()
        self.run_preflight_browser(control)
        if self.request("/storage/configs"):
            raise RuntimeError("fresh-install fixture found pre-existing storage configuration")
        self.run_storage_setup_browser(control)
        salt, agent_secret = secrets.token_hex(16), secrets.token_hex(32)
        self.redactions.extend([salt, agent_secret])
        self.request("/settings/AGENT_SECRET_KEY", {"name": "AGENT_SECRET_KEY",
                     "value": {"salt": salt, "secret_key": agent_secret}}, "PUT")
        self.stop_application(control)
        control = self.start_control("control-configured")
        self.login()
        self.note("Authenticated storage and Agent signing configuration saved; Control restarted")
        if self.request("/agent?pageNum=1&pageSize=100")["count"] != 0:
            raise RuntimeError("fresh-install fixture found a pre-existing Agent")
        token = self.request("/registration-tokens", {"description": "Disposable acceptance enrollment",
                                                     "maxUses": 0}, "POST")["token"]
        self.redactions.append(token)
        self.write_agent_configuration(token)
        agent = self.start_agent("agent-pending")
        def pending_agent():
            detail = self.request("/agent?pageNum=1&pageSize=100")
            if detail["count"] > 1:
                raise RuntimeError("enrollment created duplicate Agent identities")
            return detail["agents"][0] if detail["count"] == 1 else None
        pending = self.wait_until("one pending Agent", pending_agent, agent)
        self.agent_id = pending["agentId"]
        if pending["isEnabled"] != 0 or pending.get("lastHeartbeat"):
            raise RuntimeError("new Agent did not fail closed pending explicit approval")
        identity_path = self.identity_path()
        # A pending Control row can precede the registration RPC response and
        # local ready-record commit. File existence also accepts the earlier
        # token-free pending record; only the completed identity is a baseline.
        identity_digest = self.wait_until("committed Agent identity",
                                          lambda: committed_identity_digest(identity_path, self.agent_id), agent)
        self.wait_until("pending Agent liveness", lambda: self.probe(AGENT_URL + "/live") == 200, agent)
        if self.probe(AGENT_URL + "/ready") != 503 or self.probe(AGENT_URL + "/live") != 200:
            raise RuntimeError("pending Agent readiness/liveness contract failed")
        self.request(f"/agent/{self.agent_id}/approve", {}, "PUT")
        self.stop_application(agent)
        # Remove the one-time enrollment token. The second start must use the
        # durable original identity and verified TLS; no config credential seed.
        self.write_agent_configuration("")
        agent = self.start_agent("agent-approved")
        self.wait_until("approved Agent readiness", lambda: self.probe(AGENT_URL + "/ready") == 200, agent)
        restored = self.request("/agent?pageNum=1&pageSize=100")
        require_resumed_agent_identity(restored, self.agent_id, identity_path, identity_digest)
        self.note("TLS enrollment remained pending, was explicitly approved and resumed the same persisted identity")
        self.configure_observer()
        self.run_build_browser(control, agent)

    def browser_environment(self, phase):
        if phase not in ("preflight", "storage-setup", "build"):
            raise ValueError("unknown actual-browser acceptance phase")
        return self.env | {
            "ARCENTRA_ACCEPTANCE_PHASE": phase,
            "ARCENTRA_REAL_STACK": "1", "ARCENTRA_ACCEPTANCE_BACKEND_URL": BACKEND_URL,
            "ARCENTRA_ACCEPTANCE_REPO_URL": self.repository_url(), "ARCENTRA_ACCEPTANCE_ORG_ID": "ci-acceptance-org",
            "ARCENTRA_ACCEPTANCE_CONTRACT": BUILD_CONTRACT,
            "ARCENTRA_ACCEPTANCE_ARTIFACT_NAME": ARTIFACT_NAME,
            "ARCENTRA_ACCEPTANCE_MANIFEST_NAME": MANIFEST_NAME,
            "ARCENTRA_ACCEPTANCE_GO_VERSION": GO_VERSION,
            "ARCENTRA_ACCEPTANCE_EXPECTED_OUTPUT": EXPECTED_OUTPUT,
            "ARCENTRA_ACCEPTANCE_COORDINATION_DIR": str(self.coordination),
            "ARCENTRA_ACCEPTANCE_BRANCH": "main",
            "ARCENTRA_ACCEPTANCE_DEFINITION_PATH": ".arcentra/pipeline.yaml",
            "ARCENTRA_ACCEPTANCE_COMMIT": self.commit,
            "ARCENTRA_ACCEPTANCE_EVIDENCE_DIR": str(self.args.evidence_dir / "browser"),
            "UI_TEST_OUTPUT_DIR": str(self.args.evidence_dir / "browser"),
        }

    def run_preflight_browser(self, control):
        browser = self.start("browser-preflight", self.args.browser_command, self.args.browser_cwd,
                             self.browser_environment("preflight"))
        self.wait_until("read-only first-build browser preflight", lambda: browser.poll() is not None, timeout=120)
        if browser.returncode != 0:
            raise RuntimeError(f"browser preflight exited {browser.returncode}; see sanitized evidence")
        if control.poll() is not None:
            raise RuntimeError("Control exited during first-build browser preflight")
        self.require_browser_summary("preflight")
        self.preflight_verified = True
        # Each browser owns its own administrator session. A successful browser
        # login invalidates this Python caller's previous token for that user.
        # Refresh it before any configuration writes; observers do not yet exist.
        self.login()
        self.note("Read-only unconfigured first-build browser preflight passed; fresh administrator API session restored")

    def run_storage_setup_browser(self, control):
        if self.preflight_verified is not True:
            raise RuntimeError("storage setup requires the verified read-only first-use preflight")
        browser = self.start("browser-storage-setup", self.args.browser_command, self.args.browser_cwd,
                             self.browser_environment("storage-setup"))
        self.wait_until("browser storage configuration", lambda: browser.poll() is not None, timeout=150)
        if browser.returncode != 0:
            raise RuntimeError(f"browser storage setup exited {browser.returncode}; see sanitized evidence")
        if control.poll() is not None:
            raise RuntimeError("Control exited during browser storage setup")
        self.require_browser_summary("storage-setup")
        # The new browser login invalidates the fixture's prior administrator
        # token. Reauthenticate before reading persisted state or writing signing.
        self.login()
        validate_persisted_storage(self.request("/storage/configs"), self.storage_configuration())
        self.storage_setup_verified = True
        self.note("Browser storage create, metadata edit with credentials preserved, explicit default and redacted reload passed; administrator API session restored")

    def require_browser_summary(self, phase):
        path = self.args.evidence_dir / "browser/summary.json"
        validate_first_build_summary(read_private_json(path), phase, self.failed_commit)

    def run_build_browser(self, control, agent):
        if self.preflight_verified is not True or self.storage_setup_verified is not True:
            raise RuntimeError("full build browser requires verified read-only preflight and browser storage setup")
        browser_env = self.browser_environment("build")
        browser = self.start("browser", self.args.browser_command, self.args.browser_cwd, browser_env)
        def browser_complete():
            if not self.failed_run and (self.coordination / "failed-run.json").exists():
                self.fix_repository_after_failed_run()
            return browser.poll() is not None
        self.wait_until("browser acceptance completion", browser_complete, timeout=600)
        if browser.returncode != 0:
            raise RuntimeError(f"browser acceptance exited {browser.returncode}; see sanitized evidence")
        if control.poll() is not None or agent.poll() is not None:
            raise RuntimeError("Control or Agent exited during browser acceptance")
        self.require_browser_summary("build")
        self.verify_downloads()
        self.note("Browser create-project → create-pipeline → run → logs → artifact journey completed")

    def cleanup(self, error):
        error = redact_diagnostics(error, self.redactions) if error else error
        self.cleanup_runtime()
        for _, handle in self.logs:
            handle.close()
        self.args.evidence_dir.mkdir(parents=True, exist_ok=True)
        write_build_verification(self.args.evidence_dir / "browser/project-build-verification.json",
                                 error, self.build_verified, self.failed_run, self.successful_run,
                                 self.artifact_evidence)
        for path, _ in self.logs:
            # Save diagnostic logs only. Configs, CA private keys, identity store,
            # response credentials, workspace and raw logs die with TemporaryDirectory.
            content = path.read_text(errors="replace")
            content = redact_diagnostics(content, self.redactions)
            content = re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "[REDACTED-JWT]", content)
            content = re.sub(r"art_[A-Za-z0-9_-]+", "[REDACTED-ENROLLMENT]", content)
            (self.args.evidence_dir / path.name).write_text(content)
        result = {
            "status": "failed" if error else "passed", "steps": self.steps,
            "error": error, "agentId": self.agent_id, "sourceCommit": self.commit,
            "contract": BUILD_CONTRACT, "failedSourceCommit": self.failed_commit,
            "firstBuildJourney": FIRST_BUILD_JOURNEY, "firstBuildPreflightVerified": self.preflight_verified,
            "storageSetupVerified": self.storage_setup_verified,
            "failedRun": self.failed_run, "successfulRun": self.successful_run,
            "verifiedArtifact": self.artifact_evidence,
            "backend": BACKEND_URL, "repository": self.repository_url(),
            "limitations": self.runtime_limitations(),
        }
        runtime = self.runtime_evidence()
        if runtime:
            result["runtime"] = runtime
        (self.args.evidence_dir / "actual-app-result.json").write_text(json.dumps(result, indent=2) + "\n")


def redact_diagnostics(content, values):
    """Redact synthetic credentials even inside reordered base64 JSON bodies."""
    secrets_to_remove = sorted((value for value in values if value), key=len, reverse=True)
    def redact_encoded(match):
        candidate = match.group(0)
        if len(candidate) > 65536:
            return "[REDACTED-LARGE-ENCODED-VALUE]"
        try:
            decoded = base64.b64decode(candidate + "=" * (-len(candidate) % 4)).decode("utf8", errors="replace")
        except ValueError:
            return candidate
        return "[REDACTED]" if any(secret in decoded for secret in secrets_to_remove) else candidate
    content = re.sub(r"[A-Za-z0-9+/]{32,}={0,2}", redact_encoded, content)
    for secret in secrets_to_remove:
        content = content.replace(secret, "[REDACTED]")
    return content


def validate_restricted_first_build(detail):
    expected = {"scopeOptions": [], "scopeOptionsTruncated": False, "canEnterScope": False,
                "configuration": {"access": "restricted"}, "agent": {"access": "restricted"}}
    if not isinstance(detail, dict) or json.dumps(detail, sort_keys=True) != json.dumps(expected, sort_keys=True):
        raise RuntimeError("read-only observer first-build response must contain only the restricted fields")


def validate_first_build_summary(summary, phase, source_commit):
    if phase not in ("preflight", "storage-setup", "build"):
        raise ValueError("unknown actual-browser acceptance phase")
    steps = (FIRST_BUILD_PREFLIGHT_STEPS + (FIRST_BUILD_STORAGE_STEPS if phase != "preflight" else [])
             + (FIRST_BUILD_CONFIGURED_STEPS if phase == "build" else []))
    expected = {"journey": FIRST_BUILD_JOURNEY, "phase": phase, "sourceCommit": source_commit, "steps": steps}
    if (not re.fullmatch(r"[a-f0-9]{40}|[a-f0-9]{64}", source_commit)
            or not isinstance(summary, dict) or summary.get("kind") != "actual-backend"
            or summary.get("status") != "passed"
            or summary.get("stage") != ({"preflight": "preflight-complete", "storage-setup": "storage-setup-complete", "build": "journey-complete"}[phase])
            or summary.get("firstBuildReadiness") != expected):
        raise RuntimeError("browser phase lacks the exact successful first-build guidance evidence")


def validate_persisted_storage(detail, configuration):
    """Independently verify redacted persisted UI setup, without exposing its data."""
    public = {key: value for key, value in configuration.items() if key not in ("accessKey", "secretKey")}
    if not isinstance(detail, list) or len(detail) != 1 or not isinstance(detail[0], dict):
        raise RuntimeError("browser storage setup must persist exactly one configuration")
    item = detail[0]
    if (item.get("storageId") != "acceptance" or item.get("storageType") != "minio"
            or item.get("name") != "Acceptance MinIO reviewed"
            or item.get("description") != "Synthetic CI artifact storage, metadata reviewed"
            or type(item.get("isEnabled")) is not int or type(item.get("isDefault")) is not int
            or item.get("isEnabled") != 1 or item.get("isDefault") != 1
            or item.get("config") != public
            or sorted(item.get("configuredFields", [])) != ["accessKey", "secretKey"]):
        raise RuntimeError("browser storage setup did not preserve the expected redacted synthetic configuration")
    serialized = json.dumps(item)
    values = [configuration["accessKey"], configuration["secretKey"],
              encoded(configuration["accessKey"]), encoded(configuration["secretKey"])]
    if redact_diagnostics(serialized, values) != serialized:
        raise RuntimeError("browser storage response exposed a synthetic credential")


def encoded(value):
    return base64.b64encode(value.encode()).decode()


def copy_fixture_source(destination):
    """Copy an explicit source allowlist, never a locally cached build output."""
    destination.mkdir(parents=True)
    for name in ("go.mod", "main.go", "main_test.go", "build.sh", "verify.py", ".gitignore"):
        (destination / name).write_bytes((FIXTURE_SOURCE / name).read_bytes())


def verify_toolchain(env):
    executable = shutil.which("go", path=env.get("PATH", ""))
    if not executable:
        raise RuntimeError("Go 1.25.6 must already be installed on the sanitized Agent PATH; no download is attempted")
    result = subprocess.run([executable, "version"], env=env, capture_output=True, text=True, timeout=10, check=False)
    if result.returncode != 0 or not re.fullmatch(r"go version go1\.25\.6 linux/(amd64|arm64)\n?", result.stdout):
        raise RuntimeError("sanitized Agent environment requires the preinstalled Linux Go 1.25.6 toolchain")


def atomic_json(path, value):
    temporary = path.with_name(path.name + ".harness-tmp")
    descriptor = os.open(temporary, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "w") as handle:
        handle.write(json.dumps(value, indent=2) + "\n")
    temporary.replace(path)


def verify_downloaded_program(binary, root, env):
    # Execute an exclusive private copy of the exact verified download bytes,
    # never reopen a browser-writable download path after checking its digest.
    # This is restricted to this reviewed fixture, not arbitrary user binaries.
    with tempfile.TemporaryDirectory(prefix="verified-program-", dir=root) as directory:
        executable = Path(directory) / ARTIFACT_NAME
        descriptor = os.open(executable, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o500)
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(binary)
        result = subprocess.run([str(executable), "arcentra"], cwd=directory, env=env,
                                capture_output=True, timeout=5, check=False)
        invalid = subprocess.run([str(executable), "agent1"], cwd=directory, env=env,
                                 capture_output=True, timeout=5, check=False)
        if (result.returncode != 0 or result.stdout != EXPECTED_OUTPUT.encode() or result.stderr
                or invalid.returncode != 2 or invalid.stdout or not invalid.stderr):
            raise RuntimeError("browser-downloaded compiled program failed independent behavior verification")


def public_build_verification(failed, success, manifest, verified):
    """An explicit public evidence allowlist; never serialize arbitrary diagnostics."""
    if verified is not True or not isinstance(manifest, dict):
        raise ValueError("build verification has not completed")
    for receipt in (failed, success):
        if not isinstance(receipt, dict):
            raise ValueError("both reviewed run receipts are required")
        validate_run_receipt(receipt, receipt.get("sourceCommit", ""))
    if (failed["sourceCommit"] == success["sourceCommit"] or failed["runId"] == success["runId"]
            or any(failed[key] != success[key] for key in ("projectId", "pipelineId"))):
        raise ValueError("build verification requires separate failed and fixed runs on the same pipeline")
    expected_behavior = {"args": ["arcentra"], "stdout": EXPECTED_OUTPUT,
                         "stdoutSha256": hashlib.sha256(EXPECTED_OUTPUT.encode()).hexdigest()}
    binary = manifest.get("binary")
    if (set(manifest) != {"schemaVersion", "sourceCommit", "goVersion", "binary", "behavior"}
            or type(manifest["schemaVersion"]) is not int or manifest["schemaVersion"] != 1
            or manifest["sourceCommit"] != success["sourceCommit"] or manifest["goVersion"] != GO_VERSION
            or manifest["behavior"] != expected_behavior or not isinstance(binary, dict)
            or set(binary) != {"name", "sizeBytes", "sha256"} or binary["name"] != ARTIFACT_NAME
            or type(binary["sizeBytes"]) is not int or not 4 < binary["sizeBytes"] <= MAX_BINARY_BYTES
            or not isinstance(binary["sha256"], str) or not re.fullmatch(r"[a-f0-9]{64}", binary["sha256"])):
        raise ValueError("public build evidence must match the fixed schema and expected fixture values")
    return {
        "contract": BUILD_CONTRACT, "status": "passed",
        "sourceCommits": {"failed": failed["sourceCommit"], "fixed": success["sourceCommit"]},
        "runs": {"failed": failed["runId"], "successful": success["runId"]},
        "goVersion": GO_VERSION,
        "binary": {key: binary[key] for key in ("name", "sizeBytes", "sha256")},
        "behavior": expected_behavior,
        "checks": {name: True for name in ("compilerFailure", "failedRunHasNoArtifacts", "newFixedCommit",
                                            "agentTestAndBuild", "browserDownloadsMatchAPI",
                                            "manifestMatchesDownload", "downloadedProgramBehavior")},
    }


def write_build_verification(path, error, verified, failed, success, manifest):
    # A failed attempt must not leave a stale success record in a reused evidence
    # directory. This exact generated filename is the only CI upload addition.
    path.unlink(missing_ok=True)
    if error or verified is not True:
        return False
    value = public_build_verification(failed, success, manifest, verified)
    path.parent.mkdir(parents=True, exist_ok=True)
    atomic_json(path, value)
    return True


def read_private_bytes(path, limit):
    # Fixed private fixture paths only. Reject symlinks/devices and unexpectedly
    # large files before reading or granting execute permission to the binary.
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_mode & 0o077
                or not 0 < info.st_size <= limit):
            raise ValueError("expected a bounded private regular fixture file")
    except BaseException:
        os.close(descriptor)
        raise
    with os.fdopen(descriptor, "rb") as handle:
        data = handle.read(limit + 1)
        if len(data) != info.st_size:
            raise ValueError("fixture file changed while reading")
        return data


def no_duplicate_keys(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON key in fixture evidence")
        value[key] = item
    return value


def committed_identity_digest(path, agent_id):
    """Read only a bounded, committed v1 identity; never expose its contents.

    Missing/pending records and commit markers are the only retryable states.
    A ready record is visible before CommitIdentity removes enrollment.pending,
    so observe both markers before and after the read. The digest covers the
    original bytes, not normalized JSON, and remains private to this process.
    """
    def private_file(info):
        if (not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600
                or info.st_uid != os.geteuid() or info.st_nlink != 1):
            raise RuntimeError("Agent identity file is unsafe")

    def snapshot(info):
        return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_nlink,
                info.st_size, info.st_mtime_ns, info.st_ctime_ns)

    try:
        try:
            directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_NONBLOCK)
        except FileNotFoundError:
            return None
        try:
            info = os.fstat(directory)
            if (not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o700
                    or info.st_uid != os.geteuid()):
                raise RuntimeError("Agent identity directory is unsafe")

            def markers_present():
                present = False
                for name in ("enrollment.pending", ".pending"):
                    try:
                        marker = os.stat(name, dir_fd=directory, follow_symlinks=False)
                    except FileNotFoundError:
                        continue
                    private_file(marker)
                    present = True
                return present

            before = markers_present()
            try:
                descriptor = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
            except FileNotFoundError:
                return None
            with os.fdopen(descriptor, "rb") as handle:
                info = os.fstat(handle.fileno())
                private_file(info)
                if not 0 < info.st_size <= MAX_IDENTITY_BYTES:
                    raise RuntimeError("Agent identity record size is invalid")
                data = handle.read(MAX_IDENTITY_BYTES + 1)
                if len(data) != info.st_size or snapshot(os.fstat(handle.fileno())) != snapshot(info):
                    raise RuntimeError("Agent identity file changed while reading")
            record = json.loads(data.decode("utf-8"), object_pairs_hook=no_duplicate_keys)
            fields = {"version", "controllerBinding", "state"}
            if (not isinstance(record, dict) or type(record.get("version")) is not int
                    or record["version"] != 1 or record.get("state") not in ("pending", "ready")):
                raise RuntimeError("Agent identity record schema is invalid")
            binding = record.get("controllerBinding")
            if (not isinstance(binding, str) or not 0 < len(binding.encode("utf-8")) <= 8192
                    or binding.strip() != binding or any(ord(char) < 32 or ord(char) == 127 for char in binding)):
                raise RuntimeError("Agent identity controller binding is invalid")
            if record["state"] == "ready":
                fields |= {"agentID", "perAgentToken"}
                if (not isinstance(record.get("agentID"), str)
                        or not re.fullmatch(r"[A-Za-z0-9_-]{1,256}", record["agentID"])
                        or record["agentID"] != agent_id):
                    raise RuntimeError("Agent identity does not match the enrolled Agent")
                token = record.get("perAgentToken")
                prefix = agent_id + ":"
                if (not isinstance(token, str) or len(token) > 4096 or not token.startswith(prefix)
                        or not token[len(prefix):]
                        or any(ord(char) < 33 or ord(char) > 126 or char == ":" for char in token[len(prefix):])):
                    raise RuntimeError("Agent identity token binding is invalid")
            if set(record) != fields:
                raise RuntimeError("Agent identity record fields are invalid")
            after = markers_present()
            if record["state"] == "pending":
                return None
            current = os.stat(path.name, dir_fd=directory, follow_symlinks=False)
            private_file(current)
            if snapshot(current) != snapshot(info):
                raise RuntimeError("Agent identity file changed while reading")
            if before or after:
                return None
            return hashlib.sha256(data).digest()
        finally:
            os.close(directory)
    except (OSError, ValueError, TypeError, RecursionError):
        # Decoder and filesystem errors can contain input or paths. Do not leak
        # them through exception chaining, diagnostics or acceptance receipts.
        raise RuntimeError("Agent identity record could not be safely read") from None


def require_resumed_agent_identity(restored, agent_id, path, identity_digest):
    """All post-readiness invariants are immediate failures, never polling."""
    if not isinstance(restored, dict) or type(restored.get("count")) is not int or restored["count"] != 1:
        raise RuntimeError("approved Agent registry count is not exactly one")
    agents = restored.get("agents")
    if not isinstance(agents, list) or len(agents) != 1 or not isinstance(agents[0], dict):
        raise RuntimeError("approved Agent registry rows are invalid")
    agent = agents[0]
    if agent.get("agentId") != agent_id:
        raise RuntimeError("approved Agent registry identity changed")
    if type(agent.get("isEnabled")) is not int or agent["isEnabled"] != 1:
        raise RuntimeError("approved Agent is not enabled")
    # Initial Heartbeat is synchronously persisted before Agent readiness.
    if not agent.get("lastHeartbeat"):
        raise RuntimeError("approved Agent has no persisted heartbeat after readiness")
    restored_digest = committed_identity_digest(path, agent_id)
    if restored_digest is None:
        raise RuntimeError("approved Agent identity commit is incomplete after readiness")
    if restored_digest != identity_digest:
        raise RuntimeError("approved Agent persisted identity bytes changed")


def read_private_json(path):
    return json.loads(read_private_bytes(path, 4096), object_pairs_hook=no_duplicate_keys)


def safe_identifier(value):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,64}", value):
        raise ValueError("invalid fixture API identifier")
    return value


def validate_run_receipt(receipt, expected_commit):
    if not isinstance(receipt, dict) or set(receipt) != {
            "schemaVersion", "projectId", "pipelineId", "runId", "sourceCommit"}:
        raise ValueError("invalid browser run receipt fields")
    if type(receipt["schemaVersion"]) is not int or receipt["schemaVersion"] != 1:
        raise ValueError("unsupported browser run receipt version")
    if not re.fullmatch(r"[a-f0-9]{40}|[a-f0-9]{64}", expected_commit) or receipt["sourceCommit"] != expected_commit:
        raise ValueError("browser run receipt has the wrong full source commit")
    for key in ("projectId", "pipelineId", "runId"):
        safe_identifier(receipt[key])


def validate_manifest(manifest_bytes, binary, expected_commit):
    manifest = json.loads(manifest_bytes, object_pairs_hook=no_duplicate_keys)
    expected = {
        "schemaVersion": 1, "sourceCommit": expected_commit, "goVersion": GO_VERSION,
        "binary": {"name": ARTIFACT_NAME, "sizeBytes": len(binary), "sha256": hashlib.sha256(binary).hexdigest()},
        "behavior": {"args": ["arcentra"], "stdout": EXPECTED_OUTPUT,
                     "stdoutSha256": hashlib.sha256(EXPECTED_OUTPUT.encode()).hexdigest()},
    }
    if (not isinstance(manifest, dict) or manifest != expected
            or type(manifest.get("schemaVersion")) is not int
            or not isinstance(manifest.get("binary"), dict)
            or type(manifest["binary"].get("sizeBytes")) is not int
            or not re.fullmatch(r"[a-f0-9]{40}|[a-f0-9]{64}", expected_commit)
            or not 4 < len(binary) <= MAX_BINARY_BYTES or not binary.startswith(b"\x7fELF")):
        raise ValueError("downloaded build manifest/binary does not match the independent fixture contract")
    return manifest


def validate_run_snapshot(receipt, run, jobs, artifacts, successful, agent_id):
    """Preserve the host-profile validator API and its fixed repository contract."""
    return _validate_run_snapshot(receipt, run, jobs, artifacts, successful, agent_id, REPO_URL)


def _validate_run_snapshot(receipt, run, jobs, artifacts, successful, agent_id, repository_url):
    if repository_url not in (REPO_URL, "git://git:9418/acceptance.git"):
        raise ValueError("unknown fixed actual-build repository profile")
    commit = receipt["sourceCommit"]
    expected_status = (3, "3", "success", "PIPELINE_STATUS_SUCCESS") if successful else (
        4, "4", "failed", "PIPELINE_STATUS_FAILED")
    if (run.get("runId") != receipt["runId"] or run.get("pipelineId") != receipt["pipelineId"]
            or run.get("status") not in expected_status or run.get("commitSha") != commit
            or run.get("definitionCommitSha") != commit or run.get("allowDeployments", False)):
        raise ValueError("scoped actual run has wrong identity, status, revision or deployment permission")
    if jobs.get("total") != 1 or not isinstance(jobs.get("list"), list) or len(jobs["list"]) != 1:
        raise ValueError("expected exactly one actual Agent job")
    job = jobs["list"][0]
    if (job.get("pipelineId") != receipt["pipelineId"] or job.get("pipelineRunId") != receipt["runId"]
            or job.get("agentId") != agent_id or job.get("status") != (4 if successful else 5)
            or job.get("sourceExpectationCaptured") is not True
            or job.get("sourceType") != "git" or job.get("sourceRepository") != repository_url
            or job.get("sourceResolvedCommit") != commit or job.get("sourceProvenanceState") != "reported"):
        raise ValueError("actual Agent job has wrong status or checkout evidence")
    safe_identifier(job.get("jobRunId"))
    steps = job.get("steps", [])
    names = [step.get("name") for step in steps]
    if names != ["toolchain", "test", "build", "verify", "publish"]:
        raise ValueError("actual Agent job must report every expected build step in order")
    for step in steps:
        safe_identifier(step.get("stepRunId"))
        if step.get("jobRunId") != job["jobRunId"] or step.get("retryMode") != "":
            raise ValueError("build steps must belong to this new job without retry recovery")
    if successful:
        if any(step.get("status") != 4 for step in steps):
            raise ValueError("actual build requires all steps to succeed")
    elif (steps[0].get("status") != 4 or steps[1].get("status") != 5
          or not isinstance(steps[1].get("exitCode"), int) or steps[1]["exitCode"] <= 0
          or any(step.get("status") != 8 for step in steps[2:])):
        raise ValueError("compiler failure must fail the test step and skip build/verify/publication")
    values = artifacts.get("list")
    if not isinstance(values, list) or artifacts.get("total") != len(values):
        raise ValueError("artifact evidence must be a complete exact-run result")
    if not successful:
        if values:
            raise ValueError("compiler-failed run must have zero artifacts")
        return
    if len(values) != 2 or {value.get("name") for value in values} != {ARTIFACT_NAME, MANIFEST_NAME}:
        raise ValueError("successful build must publish only the binary and manifest")
    for value in values:
        if (value.get("projectId") != receipt["projectId"] or value.get("pipelineId") != receipt["pipelineId"]
                or value.get("pipelineRunId") != receipt["runId"] or value.get("status") != "available"
                or value.get("jobRunId") != job["jobRunId"] or value.get("stepRunId") != steps[-1]["stepRunId"]
                or value.get("downloadAvailable") is not True):
            raise ValueError("artifact scope or availability differs from the successful run")


def child_environment(parent, root):
    # Host shell execution must not inherit repository/organization secrets,
    # cloud credentials, proxies, SSH agents or an operator's Git configuration.
    env = {key: parent[key] for key in ("PATH", "CI", "GITHUB_ACTIONS", "RUNNER_ENVIRONMENT",
                                       "PLAYWRIGHT_BROWSERS_PATH") if key in parent}
    home, temp = root / "home", root / "tmp"
    home.mkdir(mode=0o700)
    temp.mkdir(mode=0o700)
    env.update({"HOME": str(home), "TMPDIR": str(temp), "LANG": "C.UTF-8",
                "AWS_EC2_METADATA_DISABLED": "true", "AWS_CONFIG_FILE": "/dev/null",
                "AWS_SHARED_CREDENTIALS_FILE": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
                "GIT_TERMINAL_PROMPT": "0"})
    env.update(GO_ENV)
    # Honor the location where CI installed the browser without using its HOME
    # as the child HOME. CI should preferably set this variable explicitly.
    if "PLAYWRIGHT_BROWSERS_PATH" not in env:
        env["PLAYWRIGHT_BROWSERS_PATH"] = str(Path(parent.get("HOME", "")) / ".cache/ms-playwright")
    return env


def common_config(root, name):
    return f'''[http]
host = "127.0.0.1"
port = {18080 if name == "control" else 18081}
[messageQueue.kafka]
bootstrapServers = "127.0.0.1:19092"
securityProtocol = "PLAINTEXT"
acks = "all"
compression = "none"
[taskQueue]
provider = "kafka"
autoCommit = false
sessionTimeout = 10000
maxPollInterval = 300000
delaySlotCount = 2
delaySlotDuration = 60
messageFormat = "sonic"
messageCodec = "sonic"
[log]
output = "stdout"
level = "INFO"
[metrics]
enable = false
host = "127.0.0.1"
[pprof]
enable = false
host = "127.0.0.1"
[logSpool]
directory = {json.dumps(str(root / (name + '-logs')))}
maxDiskUsageMB = 32
'''


def control_config(root, jwt_secret):
    return common_config(root, "control") + f'''[grpc]
host = "127.0.0.1"
port = 19090
reflection = false
readWriteTimeout = 10
[grpc.tls]
enabled = true
certFile = {json.dumps(str(root / 'server.pem'))}
keyFile = {json.dumps(str(root / 'server.key'))}
[http.auth]
accessExpire = "30m"
refreshExpire = "1h"
secretKey = {json.dumps(jwt_secret)}
[database]
driver = "mysql"
[database.mysql]
dsn = {json.dumps(DEPENDENCIES['ARCENTRA_TEST_MYSQL_DSN'])}
[database.options]
output = false
maxOpenConns = 20
maxIdleConns = 5
[redis]
mode = "single"
address = "127.0.0.1:16379"
db = 0
[trace]
enabled = false
[authz]
enabled = true
enforcer = "casbin"
cacheTtlSeconds = 30
policySyncEnable = true
'''


def agent_config(root, registration_token):
    return common_config(root, "agent") + f'''[grpc]
serverAddr = "127.0.0.1:19090"
registrationToken = {json.dumps(registration_token)}
readWriteTimeout = 10
[grpc.tls]
enabled = true
caFile = {json.dumps(str(root / 'ca.pem'))}
serverName = "arcentra-acceptance.local"
[agent]
mode = "baremetal"
id = ""
name = "ci-acceptance-agent"
interval = 5
maxConcurrentJobs = 1
jobTimeout = 240
workspaceDir = {json.dumps(str(root / 'workspaces'))}
tempDir = {json.dumps(str(root / 'tmp'))}
[agent.identityPersistence]
enabled = true
directory = {json.dumps(str(root / 'identity'))}
[agent.sandbox]
enable = false
[agent.labels]
acceptance = "true"
[outbox]
sendIntervalMs = 50
sendBatchSize = 100
maxDiskUsageMB = 32
'''


def pipeline_spec():
    return {"namespace": "acceptance", "version": "1.0.0", "jobs": [{
        "name": "build", "source": {"type": "git", "repo": REPO_URL, "branch": "main"},
        "steps": [
            *[{"name": stage, "uses": "shell", "runOnAgent": True, "timeout": "120s",
               "args": {"run": f"sh build.sh {stage}"}} for stage in ("toolchain", "test", "build", "verify")],
            {"name": "publish", "uses": "builtin:artifacts", "action": "upload", "runOnAgent": True,
             "args": {"paths": [ARTIFACT_NAME, MANIFEST_NAME]}},
        ],
    }]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--control-bin", required=True)
    parser.add_argument("--agent-bin", required=True)
    parser.add_argument("--cli-bin", required=True)
    parser.add_argument("--browser-cwd", type=Path, required=True)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("browser_command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    validate_environment(os.environ)
    if args.browser_command[:1] == ["--"]:
        args.browser_command = args.browser_command[1:]
    if not args.browser_command:
        parser.error("a real browser acceptance command after -- is required")
    for name in ("control_bin", "agent_bin", "cli_bin"):
        path = Path(getattr(args, name)).resolve(strict=True)
        if not path.is_file() or not os.access(path, os.X_OK):
            parser.error(f"{name} is not an executable file")
        setattr(args, name, str(path))
    for tool in ("git", "openssl"):
        if not shutil.which(tool):
            parser.error(f"{tool} must be installed from its official runner package")
    args.browser_cwd = args.browser_cwd.resolve(strict=True)
    args.evidence_dir = args.evidence_dir.resolve()
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="arcentra-actual-app-") as directory:
        fixture = Fixture(args, Path(directory))
        error = None
        def interrupted(signum, _frame):
            raise RuntimeError(f"acceptance interrupted by signal {signum}")
        signal.signal(signal.SIGTERM, interrupted)
        try:
            fixture.exercise()
        except (Exception, KeyboardInterrupt) as exc:
            error = redact_diagnostics(str(exc) or "acceptance interrupted", fixture.redactions)
            print(f"[actual-app] FAILED: {error}", file=sys.stderr)
        finally:
            fixture.cleanup(error)
        return 1 if error else 0


if __name__ == "__main__":
    sys.exit(main())
