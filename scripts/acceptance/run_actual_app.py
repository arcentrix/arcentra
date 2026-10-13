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
ARTIFACT_NAME = "acceptance.txt"
ARTIFACT_CONTENT = "arcentra-real-stack-acceptance\n"
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
        (source / ".arcentra").mkdir(parents=True)
        (source / "acceptance-marker").write_text("trusted disposable acceptance fixture\n")
        (source / ".arcentra/pipeline.yaml").write_text(json.dumps(pipeline_spec(), indent=2) + "\n")
        self.run("git-init", ["git", "init", "--initial-branch=main", str(source)])
        self.run("git-add", ["git", "add", "."], source)
        self.run("git-commit", ["git", "-c", "user.name=Acceptance Fixture", "-c",
                                "user.email=acceptance@example.invalid", "commit", "-m",
                                "Trusted synthetic acceptance pipeline"], source)
        result = subprocess.run(["git", "rev-parse", "HEAD"], cwd=source, env=self.env,
                                capture_output=True, text=True, check=True, timeout=10)
        self.commit = result.stdout.strip()
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

    def exercise(self):
        self.create_tls()
        jwt_secret = secrets.token_hex(32)
        self.redactions.append(jwt_secret)
        self.write_private("control.toml", control_config(self.root, jwt_secret))
        self.write_private("plugins.toml", '[builtins.shell]\nshell = "/bin/sh"\ntimeout = 60\n'
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
            "ARCENTRA_ACCEPTANCE_ARTIFACT_NAME": ARTIFACT_NAME,
            "ARCENTRA_ACCEPTANCE_ARTIFACT_CONTENT": ARTIFACT_CONTENT,
            "ARCENTRA_ACCEPTANCE_BRANCH": "main",
            "ARCENTRA_ACCEPTANCE_DEFINITION_PATH": ".arcentra/pipeline.yaml",
            "ARCENTRA_ACCEPTANCE_COMMIT": self.commit,
            "ARCENTRA_ACCEPTANCE_EVIDENCE_DIR": str(self.args.evidence_dir / "browser"),
            "UI_TEST_OUTPUT_DIR": str(self.args.evidence_dir / "browser"),
        }
        browser = self.start("browser", self.args.browser_command, self.args.browser_cwd, browser_env)
        self.wait_until("browser acceptance completion", lambda: browser.poll() is not None,
                        timeout=360)
        if browser.returncode != 0:
            raise RuntimeError(f"browser acceptance exited {browser.returncode}; see sanitized evidence")
        if control.poll() is not None or agent.poll() is not None:
            raise RuntimeError("Control or Agent exited during browser acceptance")
        self.note("Browser create-project → create-pipeline → run → logs → artifact journey completed")

    def cleanup(self, error):
        for process in reversed(self.children):
            self.stop(process)
        for _, handle in self.logs:
            handle.close()
        self.args.evidence_dir.mkdir(parents=True, exist_ok=True)
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
            "backend": BACKEND_URL, "repository": REPO_URL,
            "limitations": ["Organization creation is not implemented; current admin project API accepts a synthetic orgId.",
                            "Trusted baremetal commands run with Agent host permissions on a disposable runner."],
        }, indent=2) + "\n")


def encoded(value):
    return base64.b64encode(value.encode()).decode()


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
jobTimeout = 120
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
            {"name": "produce", "uses": "shell", "runOnAgent": True,
             "args": {"run": "test -f acceptance-marker; printf 'arcentra-real-stack-acceptance\\n' > acceptance.txt; printf 'arcentra-real-stack-acceptance\\n'"}},
            {"name": "publish", "uses": "builtin:artifacts", "action": "upload", "runOnAgent": True,
             "args": {"paths": [ARTIFACT_NAME]}},
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
