#!/usr/bin/env python3
"""Exercise a fresh installation using the real CLI, Control, Agent and browser.

This is intentionally a disposable GitHub-hosted runner fixture, not a deployment
helper. It never seeds product tables, disables authorization, or supplies mocks.
The caller starts the loopback dependency services and creates the S3 bucket.
"""

import argparse
import base64
import hashlib
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
BUILD_CONTRACT = "stdlib-build-v1"
GO_VERSION = "go1.25.6"
ARTIFACT_NAME = "stdlib-demo"
MANIFEST_NAME = "manifest.json"
EXPECTED_OUTPUT = '{"message":"HELLO, ARCENTRA!","length":8}\n'
FIXTURE_SOURCE = Path(__file__).resolve().parent / "fixtures/stdlib-build"
COMPILE_ERROR = "\nvar _ = acceptanceCompileError\n"
MAX_BINARY_BYTES = 32 * 1024 * 1024
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
        self.redactions = [PASSWORD, DEPENDENCIES["ARCENTRA_TEST_S3_SECRET_KEY"],
                           "ci-only-mysql-password"]
        self.token = ""
        self.steps = []
        self.agent_id = ""
        self.commit = ""
        self.failed_commit = ""
        self.failed_run = None
        self.successful_run = None
        self.artifact_evidence = None
        self.build_verified = False
        self.coordination = root / "coordination"
        self.coordination.mkdir(mode=0o700)
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

    def request(self, path, payload=None, method="GET", authenticated=True):
        url = BACKEND_URL + "/api/v1" + path
        validate_http_url(url)
        headers = {"Content-Type": "application/json"}
        if authenticated:
            if not self.token:
                raise RuntimeError("authenticated fixture request has no login token")
            headers["Authorization"] = "Bearer " + self.token
        data = None if payload is None else json.dumps(payload).encode()
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        with self.http.open(req, timeout=10) as response:
            body = json.load(response)
        if body.get("code") != 200 or "errMsg" in body:
            # Never put response bodies (which may contain returned credentials)
            # into CI output, exceptions or saved receipts.
            raise RuntimeError(f"{method} {path} failed with application code {body.get('code')}")
        return body.get("detail")

    def probe(self, url):
        validate_http_url(url)
        try:
            with self.http.open(url, timeout=2) as response:
                return response.status
        except urllib.error.HTTPError as exc:
            return exc.code
        except (urllib.error.URLError, TimeoutError):
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

    def login(self):
        detail = self.request("/users/login", {"username": USERNAME, "password": encoded(PASSWORD)},
                              "POST", authenticated=False)
        self.token = detail["token"]["accessToken"]
        self.redactions.extend(detail["token"].values())

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
        (source / ".arcentra/pipeline.yaml").write_text(json.dumps(pipeline_spec(), indent=2) + "\n")
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
        daemon = self.start("git-daemon", ["git", "daemon", "--listen=127.0.0.1", "--port=19418",
                                           "--export-all", f"--base-path={repositories}", str(repositories)])
        def reachable():
            result = subprocess.run(["git", "ls-remote", REPO_URL, "refs/heads/main"],
                                    env=self.env, capture_output=True, text=True, timeout=5)
            return result.returncode == 0 and self.commit in result.stdout
        self.wait_until("loopback Git source", reachable, daemon, timeout=20)
        self.note("Synthetic Git source is served read-only on loopback")

    def run_snapshot(self, receipt, successful):
        """Independently read scoped API evidence; never trust browser status claims."""
        validate_run_receipt(receipt, self.commit)
        base = f"/pipelines/{receipt['pipelineId']}/runs/{receipt['runId']}"
        run = self.request(base)
        jobs = self.request(base + "/jobs")
        artifacts = self.request(f"/projects/{receipt['projectId']}/artifacts?" +
                                 urllib.parse.urlencode({"runId": receipt["runId"], "pageSize": 100}))
        validate_run_snapshot(receipt, run, jobs, artifacts, successful, self.agent_id)
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
        verify_toolchain(self.env)
        self.note("Preinstalled Go 1.25.6 verified in the actual sanitized Agent child environment")
        self.create_tls()
        jwt_secret = secrets.token_hex(32)
        self.redactions.append(jwt_secret)
        self.write_private("control.toml", control_config(self.root, jwt_secret))
        self.write_private("plugins.toml", '[builtins.shell]\nshell = "/bin/sh"\ntimeout = 120\n'
                           '[plugins.git]\ngitPath = "git"\ntimeout = 60\nshallow = false\n')
        self.run("migrate", [self.args.cli_bin, "migrate", "--conf", str(self.root / "control.toml")])
        self.note("Real CLI migration completed; no account or product data seeded")
        control = self.start_control("control-before-bootstrap")
        self.request("/users/register", {"username": USERNAME, "email": "ci-admin@example.invalid",
                                         "fullName": "Acceptance Administrator", "password": encoded(PASSWORD)},
                     "POST", authenticated=False)
        self.stop(control)
        self.run("bootstrap-admin", [self.args.cli_bin, "bootstrap-admin", "--conf",
                                      str(self.root / "control.toml"), "--username", USERNAME])
        control = self.start_control("control-after-bootstrap")
        self.login()
        self.note("HTTP registration, explicit CLI administrator bootstrap and fresh login completed")
        if self.request("/storage/configs"):
            raise RuntimeError("fresh-install fixture found pre-existing storage configuration")
        storage = {"endpoint": "127.0.0.1:19000", "accessKey": DEPENDENCIES["ARCENTRA_TEST_S3_ACCESS_KEY"],
                   "secretKey": DEPENDENCIES["ARCENTRA_TEST_S3_SECRET_KEY"], "bucket": "acceptance",
                   "region": "us-east-1", "useTLS": False, "basePath": "actual-app"}
        self.request("/storage/configs", {"storageId": "acceptance", "name": "Acceptance MinIO",
                                          "storageType": "minio", "isDefault": 1,
                                          "config": encoded(json.dumps(storage))}, "POST")
        salt, agent_secret = secrets.token_hex(16), secrets.token_hex(32)
        self.redactions.extend([salt, agent_secret])
        self.request("/settings/AGENT_SECRET_KEY", {"name": "AGENT_SECRET_KEY",
                     "value": {"salt": salt, "secret_key": agent_secret}}, "PUT")
        self.stop(control)
        control = self.start_control("control-configured")
        self.login()
        self.note("Authenticated storage and Agent signing configuration saved; Control restarted")
        if self.request("/agent?pageNum=1&pageSize=100")["count"] != 0:
            raise RuntimeError("fresh-install fixture found a pre-existing Agent")
        token = self.request("/registration-tokens", {"description": "Disposable acceptance enrollment",
                                                     "maxUses": 0}, "POST")["token"]
        self.redactions.append(token)
        self.write_private("agent.toml", agent_config(self.root, token))
        agent = self.start("agent-pending", [self.args.agent_bin, "-conf", str(self.root / "agent.toml")])
        def pending_agent():
            detail = self.request("/agent?pageNum=1&pageSize=100")
            if detail["count"] > 1:
                raise RuntimeError("enrollment created duplicate Agent identities")
            return detail["agents"][0] if detail["count"] == 1 else None
        pending = self.wait_until("one pending Agent", pending_agent, agent)
        self.agent_id = pending["agentId"]
        if pending["isEnabled"] != 0 or pending.get("lastHeartbeat"):
            raise RuntimeError("new Agent did not fail closed pending explicit approval")
        identity_path = self.root / "identity/identity.json"
        self.wait_until("durable pending Agent identity", identity_path.is_file, agent)
        identity_digest = hashlib.sha256(identity_path.read_bytes()).digest()
        self.wait_until("pending Agent liveness", lambda: self.probe(AGENT_URL + "/live") == 200, agent)
        if self.probe(AGENT_URL + "/ready") != 503 or self.probe(AGENT_URL + "/live") != 200:
            raise RuntimeError("pending Agent readiness/liveness contract failed")
        self.request(f"/agent/{self.agent_id}/approve", {}, "PUT")
        self.stop(agent)
        # Remove the one-time enrollment token. The second start must use the
        # durable original identity and verified TLS; no config credential seed.
        self.write_private("agent.toml", agent_config(self.root, ""))
        agent = self.start("agent-approved", [self.args.agent_bin, "-conf", str(self.root / "agent.toml")])
        self.wait_until("approved Agent readiness", lambda: self.probe(AGENT_URL + "/ready") == 200, agent)
        restored = self.request("/agent?pageNum=1&pageSize=100")
        if (restored["count"] != 1 or restored["agents"][0]["agentId"] != self.agent_id
                or restored["agents"][0]["isEnabled"] != 1 or not restored["agents"][0].get("lastHeartbeat")
                or hashlib.sha256(identity_path.read_bytes()).digest() != identity_digest):
            raise RuntimeError("approved Agent did not resume exactly the saved authenticated identity")
        self.note("TLS enrollment remained pending, was explicitly approved and resumed the same persisted identity")
        self.create_repository()
        browser_env = self.env | {
            "ARCENTRA_REAL_STACK": "1", "ARCENTRA_ACCEPTANCE_BACKEND_URL": BACKEND_URL,
            "ARCENTRA_ACCEPTANCE_REPO_URL": REPO_URL, "ARCENTRA_ACCEPTANCE_ORG_ID": "ci-acceptance-org",
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
        self.verify_downloads()
        self.note("Browser create-project → create-pipeline → run → logs → artifact journey completed")

    def cleanup(self, error):
        for process in reversed(self.children):
            self.stop(process)
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
            for secret in sorted(self.redactions, key=len, reverse=True):
                if secret:
                    content = content.replace(secret, "[REDACTED]")
            content = re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "[REDACTED-JWT]", content)
            content = re.sub(r"art_[A-Za-z0-9_-]+", "[REDACTED-ENROLLMENT]", content)
            (self.args.evidence_dir / path.name).write_text(content)
        (self.args.evidence_dir / "actual-app-result.json").write_text(json.dumps({
            "status": "failed" if error else "passed", "steps": self.steps,
            "error": error, "agentId": self.agent_id, "sourceCommit": self.commit,
            "contract": BUILD_CONTRACT, "failedSourceCommit": self.failed_commit,
            "failedRun": self.failed_run, "successfulRun": self.successful_run,
            "verifiedArtifact": self.artifact_evidence,
            "backend": BACKEND_URL, "repository": REPO_URL,
            "limitations": ["Organization creation is not implemented; current admin project API accepts a synthetic orgId.",
                            "Trusted baremetal commands run with Agent host permissions on a disposable runner.",
                            "Module downloads are disabled; this does not prove host network isolation or container builds.",
                            "Manifest is an unsigned integrity record, not a provenance attestation."],
        }, indent=2) + "\n")


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
            or job.get("sourceType") != "git" or job.get("sourceRepository") != REPO_URL
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
            error = str(exc) or "acceptance interrupted"
            print(f"[actual-app] FAILED: {error}", file=sys.stderr)
        finally:
            fixture.cleanup(error)
        return 1 if error else 0


if __name__ == "__main__":
    sys.exit(main())
