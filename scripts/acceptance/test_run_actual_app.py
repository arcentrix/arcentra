"""Offline fixture checks only: no services, sockets or browser are started."""

import base64
import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import tomllib
import unittest
from unittest import mock

import run_actual_app as app


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


if __name__ == "__main__":
    unittest.main()
