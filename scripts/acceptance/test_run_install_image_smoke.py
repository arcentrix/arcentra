"""Pure subprocess-fixture tests; these never contact Docker or start a container."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import run_install_image_smoke as smoke

SHA = "a" * 40
VERSION = "26.1.0.0"
ENV = {"ARCENTRA_INSTALL_IMAGE_SMOKE": "1", "GITHUB_ACTIONS": "true",
       "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_OS": "Linux", "RUNNER_ARCH": "X64",
       "GITHUB_SHA": SHA, "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1", "PATH": "/usr/bin:/bin"}
STATE = {"Status": "exited", "ExitCode": 1, "OOMKilled": False, "Error": ""}
ERROR = f"failed to read configuration file: open {smoke.MISSING}: no such file or directory"


class ValidationTests(unittest.TestCase):
    def test_runner_guard(self):
        smoke.guard(ENV, "x86_64", 1001)
        for key in ("ARCENTRA_INSTALL_IMAGE_SMOKE", "GITHUB_ACTIONS", "RUNNER_ENVIRONMENT",
                    "RUNNER_OS", "RUNNER_ARCH", "GITHUB_SHA", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"):
            with self.subTest(key=key), self.assertRaises(ValueError):
                smoke.guard({**ENV, key: "bad"}, "x86_64", 1001)
        for key in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"):
            with self.subTest(key=key), self.assertRaises(ValueError):
                smoke.guard({**ENV, key: "custom"}, "x86_64", 1001)
        with self.assertRaises(ValueError):
            smoke.guard(ENV, "aarch64", 1001)
        with self.assertRaises(ValueError):
            smoke.guard(ENV, "x86_64", 0)

    def test_build_metadata_matches_actual_go1256_trimpath_output(self):
        # Actual local dist/linux/amd64/arcentra go version -m header/build rows
        # captured with the pinned toolchain; dependency rows omitted, SHA replaced.
        # All three real artifacts omit -ldflags when compiled with -trimpath.
        # The captured local build was dirty; normalize only that row for the
        # positive CI fixture and assert the original dirty case is rejected.
        text = f"""dist/linux/amd64/arcentra: go1.25.6
\tbuild\t-buildmode=exe
\tbuild\t-compiler=gc
\tbuild\t-trimpath=true
\tbuild\tCGO_ENABLED=1
\tbuild\tGOARCH=amd64
\tbuild\tGOOS=linux
\tbuild\tGOAMD64=v1
\tbuild\tvcs=git
\tbuild\tvcs.revision={SHA}
\tbuild\tvcs.time=2026-10-16T17:35:31Z
\tbuild\tvcs.modified=false
"""
        facts = smoke.validate_build_metadata(text, SHA)
        self.assertEqual(facts["vcs_revision"], SHA)
        self.assertFalse(facts["vcs_modified"])
        self.assertNotIn("version", facts)
        self.assertNotIn("-ldflags", text)
        self.assertFalse(smoke.validate_build_metadata(text.replace("vcs.modified=true", "vcs.modified=false"), SHA)["vcs_modified"])
        smoke.validate_build_metadata(text.replace("dist/linux/amd64/arcentra", "/tmp/path with spaces/arcentra"), SHA)
        for bad in (text.replace("amd64", "arm64"), text.replace(SHA, "b" * 40),
                    text.replace("vcs.modified=false", "vcs.modified=true"),
                    text.replace("vcs.modified=false", "other.modified=false"),
                    text.replace("vcs.revision=", "other.revision="), text.replace("vcs=git", "vcs=other"),
                    text.replace("CGO_ENABLED=1", "CGO_ENABLED=0"), text.replace("CGO_ENABLED=1", "other=1"),
                    text.replace("go1.25.6", "go1.25.7"), text.replace("-trimpath=true", "-trimpath=false"), ""):
            with self.assertRaises(ValueError):
                smoke.validate_build_metadata(bad, SHA)

    def test_exact_image_contract(self):
        info = {"Id": "sha256:" + "b" * 64, "Os": "linux", "Architecture": "amd64",
                "Config": {"Entrypoint": smoke.ENTRYPOINTS["arcentra"], "Cmd": None,
                           "Labels": {smoke.LABEL: "owner"}}}
        self.assertEqual(smoke.validate_image(info, "arcentra", "owner"), info["Id"])
        for key, value in (("Id", "tag"), ("Architecture", "arm64"), ("Os", "windows")):
            with self.assertRaises(ValueError):
                smoke.validate_image({**info, key: value}, "arcentra", "owner")
        for key, value in (("Entrypoint", ["/bin/sh"]), ("Cmd", ["--help"]), ("Labels", {})):
            with self.assertRaises(ValueError):
                smoke.validate_image({**info, "Config": {**info["Config"], key: value}}, "arcentra", "owner")

    def test_missing_config_strictly_requires_application_evidence(self):
        smoke.validate_case("missing", 1, "error: " + ERROR, STATE, VERSION, SHA)
        for output in ("no such file or directory", "GLIBC_2.38 not found", "exec /arcentra: no such file or directory",
                       "failed to load plugin config file\n" + ERROR, ERROR + "\nGLIBC_2.38 not found"):
            with self.subTest(output=output), self.assertRaises(ValueError):
                smoke.validate_case("missing", 1, output, STATE, VERSION, SHA)
        for key, value in (("OOMKilled", True), ("Error", "OCI failed"), ("Status", "running"), ("ExitCode", 127)):
            with self.assertRaises(ValueError):
                smoke.validate_case("missing", 1, ERROR, {**STATE, key: value}, VERSION, SHA)
        with self.assertRaises(ValueError):
            smoke.validate_case("missing", 0, ERROR, {**STATE, "ExitCode": 0}, VERSION, SHA)

    def test_help_and_version(self):
        state = {**STATE, "ExitCode": 0}
        for kind, output in (("help", "Usage of /arcentra:\n -conf value"),
                             ("cli-help", "Usage: arcentra-cli"),
                             ("cli-version", json.dumps({"version": VERSION, "gitCommit": SHA, "platform": "linux/amd64"}))):
            smoke.validate_case(kind, 0, output, state, VERSION, SHA)
            with self.assertRaises(ValueError):
                smoke.validate_case(kind, 127, output, {**state, "ExitCode": 127}, VERSION, SHA)
        with self.assertRaises(ValueError):
            smoke.validate_case("cli-version", 0, '{"version":"wrong"}', state, VERSION, SHA)


class SubprocessFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.runner = smoke.Smoke(self.root, VERSION, ENV)

    def test_startup_uses_real_image_entrypoint_no_host_mounts(self):
        calls = []
        def fixture(args, **kwargs):
            calls.append(args)
            if "inspect" in args:
                output = json.dumps([{"State": STATE}])
            else:
                output = ERROR
            return subprocess.CompletedProcess(args, 1 if "start" in args else 0, output)
        with patch.object(smoke, "run_bounded", side_effect=fixture):
            result, state = self.runner.container("sha256:" + "b" * 64, "arcentra-missing", ["-conf", smoke.MISSING])
        smoke.validate_case("missing", result.returncode, result.stdout, state, VERSION, SHA)
        create = calls[0]
        self.assertNotIn("--entrypoint", create)
        self.assertEqual(create[-3:], ["sha256:" + "b" * 64, "-conf", smoke.MISSING])
        self.assertEqual(create[create.index("--network") + 1], "none")
        for forbidden in ("--privileged", "--mount", "--volume", "--publish", "--cap-add"):
            self.assertNotIn(forbidden, create)
        self.assertTrue((self.root / "resources.json").is_file())

    def test_timeout_keeps_manifest_and_evidence(self):
        with patch.object(smoke, "run_bounded", side_effect=subprocess.TimeoutExpired(["docker"], 30, b"partial")):
            with self.assertRaises(subprocess.TimeoutExpired):
                self.runner.container("image", "arcentra-help", ["-h"])
        self.assertEqual(len(json.loads((self.root / "resources.json").read_text())["containers"]), 1)
        self.assertTrue(json.loads((self.root / "001-command.json").read_text())["timeout"])

    def test_cleanup_requires_ownership_and_verifies_absence(self):
        name = self.runner.owner + "-arcentra-help"
        self.runner.containers = [name]
        owned = json.dumps([{"Config": {"Labels": {smoke.LABEL: self.runner.owner}}}])
        results = [subprocess.CompletedProcess([], 0, owned), subprocess.CompletedProcess([], 0, name),
                   subprocess.CompletedProcess([], 1, "Error: No such container: " + name)]
        with patch.object(smoke, "run_bounded", side_effect=results) as call:
            self.assertEqual(self.runner.cleanup(), [])
        self.assertEqual(call.call_args_list[1].args[0][-3:], ["rm", "--force", name])
        with patch.object(smoke, "run_bounded", return_value=subprocess.CompletedProcess([], 0, '[{"Config":{"Labels":{}}}]')) as call:
            self.assertIn("without own label", self.runner.cleanup()[0])
            self.assertEqual(call.call_count, 1)

    def test_cleanup_inspect_error_not_assumed_absent(self):
        self.runner.containers = [self.runner.owner + "-arcentra-help"]
        with patch.object(smoke, "run_bounded", return_value=subprocess.CompletedProcess([], 1, "daemon disconnected")):
            self.assertIn("cannot establish", self.runner.cleanup()[0])

    def test_full_gate_uses_both_targets_and_rejects_loader_failures(self):
        builds = []
        def docker(*args, **kwargs):
            if args[0] == "build":
                builds.append(args)
                return subprocess.CompletedProcess(args, 0, "built")
            target = args[-1].split(":")[-1]
            info = {"Id": "sha256:" + "b" * 64, "Os": "linux", "Architecture": "amd64",
                    "Config": {"Entrypoint": smoke.ENTRYPOINTS[target], "Cmd": None,
                               "Labels": {smoke.LABEL: self.runner.owner}}}
            return subprocess.CompletedProcess(args, 0, json.dumps([info]))
        def container(image, suffix, args, entrypoint=None):
            if suffix.endswith("diagnostic"):
                return subprocess.CompletedProcess([], 0, "glibc 2.36"), {**STATE, "ExitCode": 0}
            if suffix.endswith("missing"):
                return subprocess.CompletedProcess([], 1, ERROR), STATE
            if suffix.endswith("cli-version"):
                output = json.dumps({"version": VERSION, "gitCommit": SHA, "platform": "linux/amd64"})
            elif suffix.endswith("cli-help"):
                output = "Usage: arcentra-cli"
            else:
                output = "Usage of /arcentra: -conf value"
            return subprocess.CompletedProcess([], 0, output), {**STATE, "ExitCode": 0}
        with patch.object(self.runner, "docker", side_effect=docker), patch.object(self.runner, "container", side_effect=container):
            self.assertEqual(self.runner.run(self.root), [])
        self.assertEqual([call[call.index("--target") + 1] for call in builds], ["arcentra", "arcentra-agent"])
        evidence = json.loads((self.root / "evidence.json").read_text())
        self.assertEqual(len(evidence["cases"]), 6)
        self.assertTrue(all(case["passed"] for case in evidence["cases"]))
        with patch.object(self.runner, "docker", side_effect=docker), patch.object(self.runner, "container", return_value=(
                subprocess.CompletedProcess([], 1, "GLIBC_2.38 not found"), STATE)):
            self.assertEqual(len(self.runner.run(self.root)), 6)

    def test_timeout_reaps_client_process_group(self):
        with patch.object(smoke.subprocess, "Popen") as popen, patch.object(smoke.os, "killpg") as kill:
            process = popen.return_value
            process.pid = 12345
            process.wait.side_effect = [subprocess.TimeoutExpired(["fixture"], 1), -9]
            with self.assertRaises(subprocess.TimeoutExpired):
                smoke.run_bounded(["fixture"], cwd=self.root, env={}, text=True,
                                  stdout=None, stderr=None, timeout=1)
            self.assertTrue(popen.call_args.kwargs["start_new_session"])
            kill.assert_called_once_with(12345, smoke.signal.SIGKILL)
            self.assertEqual(process.wait.call_count, 2)

    def test_interruption_reaps_client_process_group(self):
        with patch.object(smoke.subprocess, "Popen") as popen, patch.object(smoke.os, "killpg") as kill:
            process = popen.return_value
            process.pid = 12345
            process.wait.side_effect = [RuntimeError("SIGTERM"), -9]
            with self.assertRaisesRegex(RuntimeError, "SIGTERM"):
                smoke.run_bounded(["fixture"], cwd=self.root, env={}, text=True,
                                  stdout=None, stderr=None, timeout=1)
            kill.assert_called_once_with(12345, smoke.signal.SIGKILL)
            self.assertEqual(process.wait.call_count, 2)

    def test_interrupted_build_cleanup_cannot_report_success(self):
        with patch.object(smoke, "run_bounded", side_effect=subprocess.TimeoutExpired(["docker"], 1)):
            with self.assertRaises(subprocess.TimeoutExpired):
                self.runner.docker("build", "context")
        self.assertTrue(json.loads((self.root / "resources.json").read_text())["active_build"])
        self.assertIn("build cancellation uncertain", self.runner.cleanup()[0])

    def test_signal_stops_all_further_resource_admission(self):
        with patch.object(self.runner, "docker", side_effect=smoke.SmokeInterrupted("SIGTERM")) as call:
            with self.assertRaises(smoke.SmokeInterrupted):
                self.runner.run(self.root)
            self.assertEqual(call.call_count, 1)
            self.assertEqual(len(self.runner.images), 1)

    def test_global_deadline_stops_subprocesses(self):
        self.runner.deadline = 0
        with patch.object(smoke, "run_bounded") as call:
            with self.assertRaisesRegex(RuntimeError, "global smoke deadline"):
                self.runner.command(["docker", "info"])
            call.assert_not_called()

    def test_cleanup_manifest_is_same_run_only(self):
        self.runner.containers = [self.runner.owner + "-arcentra-help"]
        self.runner.save_resources()
        other = smoke.Smoke(self.root, VERSION, ENV)
        other.restore_resources()
        self.assertEqual(other.owner, self.runner.owner)
        for field, value in (("sha", "b" * 40), ("attempt", "2"), ("run_id", "456"),
                             ("owner", "foreign"), ("containers", ["foreign-container"]),
                             ("images", ["ubuntu:latest"])):
            original = json.loads((self.root / "resources.json").read_text())
            (self.root / "resources.json").write_text(json.dumps({**original, field: value}))
            with self.subTest(field=field), self.assertRaises(ValueError):
                other.restore_resources()
            (self.root / "resources.json").write_text(json.dumps(original))


if __name__ == "__main__":
    unittest.main()
