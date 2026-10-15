"""Offline policy regression tests. No daemon, privilege, mounts or network."""
import importlib.util
import os
from pathlib import Path
import tempfile
import tomllib
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("containerd_guard", Path(__file__).with_name("containerd_runtime_guard.py"))
GUARD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GUARD)
LOG_SPEC = importlib.util.spec_from_file_location("containerd_test_log", Path(__file__).with_name("verify_containerd_test_log.py"))
TEST_LOG = importlib.util.module_from_spec(LOG_SPEC)
LOG_SPEC.loader.exec_module(TEST_LOG)


class RuntimeTestLogTests(unittest.TestCase):
    expected = ["TestSession", "TestDurable", "TestDurable/cancel", "TestDurable/timeout", "TestRecovery"]
    round = "\n".join([
        "=== RUN   TestSession", "--- PASS: TestSession (1.00s)",
        "=== RUN   TestDurable", "=== RUN   TestDurable/cancel",
        "=== RUN   TestDurable/timeout", "--- PASS: TestDurable (2.00s)",
        "    --- PASS: TestDurable/cancel (0.50s)",
        "    --- PASS: TestDurable/timeout (1.00s)",
        "=== RUN   TestRecovery", "--- PASS: TestRecovery (1.00s)", "",
    ])

    def test_accepts_three_complete_repetitions(self):
        TEST_LOG.verify(self.round * 3 + "PASS\n", 3, self.expected)

    def test_rejects_missing_repetition_or_deadline_subtest(self):
        for text in (self.round * 2 + "PASS\n", (self.round * 3 + "PASS\n").replace("    --- PASS: TestDurable/timeout (1.00s)\n", "", 1)):
            with self.subTest(text=text), self.assertRaises(ValueError):
                TEST_LOG.verify(text, 3, self.expected)

    def test_rejects_indented_skip_failure_or_no_tests(self):
        for failure in ("    --- SKIP: TestDurable/timeout (0.00s)", "--- FAIL: TestRecovery (1.00s)", "FAIL", "testing: warning: no tests to run"):
            with self.subTest(failure=failure), self.assertRaises(ValueError):
                TEST_LOG.verify(self.round * 3 + failure + "\nPASS\n", 3, self.expected)

    def test_rejects_duplicate_pass_missing_start_and_extra_round(self):
        complete = self.round * 3 + "PASS\n"
        for text in (complete + "--- PASS: TestRecovery (1.00s)\n", complete.replace("=== RUN   TestRecovery\n", "", 1), self.round * 4 + "PASS\n", complete.replace("=== RUN   TestSession\n", "=== RUN   TestSession\n=== RUN   TestSession\n", 1)):
            with self.subTest(text=text), self.assertRaises(ValueError):
                TEST_LOG.verify(text, 3, self.expected)

    def test_rejects_missing_or_duplicate_success_summary(self):
        for suffix in ("", "PASS\nPASS\n"):
            with self.subTest(suffix=suffix), self.assertRaises(ValueError):
                TEST_LOG.verify(self.round * 3 + suffix, 3, self.expected)

    def test_rejects_invalid_expectations(self):
        for repetitions, expected in ((0, self.expected), (1, []), (1, ["TestSession", "TestSession"])):
            with self.subTest(repetitions=repetitions, expected=expected), self.assertRaises(ValueError):
                TEST_LOG.verify(self.round + "PASS\n", repetitions, expected)


class RuntimeGuardTests(unittest.TestCase):
    def test_rejects_host_and_symlink_paths(self):
        for path in ("/", "/tmp", "/run/containerd", "/tmp/arc-cd.12.1.ABCDEF/../host", "/tmp/arc-cd.12.1.ABCDEF-extra"):
            with self.subTest(path=path), self.assertRaises(ValueError):
                GUARD.validate_base(path)
        with tempfile.TemporaryDirectory(prefix="arc-cd.12.1.") as real:
            link = "/tmp/arc-cd.12.1.aB9cD1"
            try:
                os.symlink(real, link)
                with self.assertRaises(ValueError):
                    GUARD.validate_base(link)
            finally:
                os.unlink(link)

    def test_config_has_only_owned_sockets(self):
        base = Path("/tmp/arc-cd.12.1.ABCDEF")
        config = tomllib.loads(GUARD.daemon_config(base, 1001, 1001))
        self.assertEqual(config["version"], 4)
        self.assertEqual(config["root"], str(base / "root"))
        self.assertEqual(config["state"], str(base / "state"))
        plugins = config["plugins"]
        self.assertEqual(plugins["io.containerd.server.v1.grpc"]["address"], str(base / "run/containerd.sock"))
        self.assertEqual(plugins["io.containerd.server.v1.grpc"]["uid"], 1001)
        self.assertEqual(plugins["io.containerd.server.v1.ttrpc"]["address"], str(base / "run/containerd.ttrpc"))
        self.assertEqual(plugins["io.containerd.shim.v1.manager"]["socket_dir"], str(base / "s"))
        for plugin in ("io.containerd.server.v1.grpc-tcp", "io.containerd.server.v1.debug", "io.containerd.server.v1.metrics", "io.containerd.nri.v1.nri", "io.containerd.cri.v1.runtime", "io.containerd.internal.v1.opt"):
            self.assertIn(plugin, config["disabled_plugins"])
        self.assertNotIn("/run/containerd", GUARD.daemon_config(base, 1001, 1001).replace(str(base / "run/containerd"), ""))

    def test_mount_matching_is_component_scoped_and_deepest_first(self):
        base = Path("/tmp/arc-cd.12.1.ABCDEF")
        text = "\n".join(f"1 2 3:4 / {target} rw - tmpfs tmpfs rw" for target in [f"{base}/state", f"{base}-other/state", "/", f"{base}/state/rootfs/proc", f"{base}/with\\040space"])
        self.assertEqual(GUARD.owned_mounts(base, text), [f"{base}/state/rootfs/proc", f"{base}/with space", f"{base}/state"])

    def test_namespace_allowlist_excludes_system_namespaces(self):
        for ns in ("default", "moby", "k8s.io", "arcentra-acceptance-../moby", "arcentra-acceptance-"):
            self.assertIsNone(GUARD.NAMESPACE_PATTERN.fullmatch(ns))
        for ns in ("arcentra-acceptance-1234", "arcentra-session-acceptance-a1", "arcentra-durable-acceptance-12-1", "arcentra-containerjob-acceptance-12-1"):
            self.assertIsNotNone(GUARD.NAMESPACE_PATTERN.fullmatch(ns))

    def test_pid_reuse_is_not_signaled(self):
        with mock.patch.object(GUARD, "pid_identity", return_value=("/other", "123")), mock.patch.object(os, "pidfd_open") as open_fd:
            GUARD.stop_pid(42, ("/owned", "122"))
            open_fd.assert_not_called()

    def test_refuses_local_privileged_execution_before_mutation(self):
        with mock.patch.object(os, "geteuid", return_value=0), mock.patch.dict(os.environ, {}, clear=True), mock.patch.object(GUARD, "validate_base") as validate:
            with self.assertRaisesRegex(RuntimeError, "authorized disposable GitHub VM"):
                GUARD.serve(Path("/tmp/arc-cd.12.1.ABCDEF"), 1001, 1001)
            validate.assert_not_called()

    def test_receipt_replaces_symlink_without_modifying_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "do-not-touch"
            target.write_text("unchanged")
            (root / "cleanup.json").symlink_to(target)
            fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
            try:
                GUARD.write_receipt(fd, "cleanup.json", '{"passed": true}', os.getuid(), os.getgid())
            finally:
                os.close(fd)
            self.assertEqual(target.read_text(), "unchanged")
            self.assertFalse((root / "cleanup.json").is_symlink())
            self.assertEqual((root / "cleanup.json").read_text(), '{"passed": true}')

    def test_diagnostics_are_bounded_and_redacted(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "daemon.log"
            path.write_text('level=info msg="not uploaded"\n' + ('level=error url=https://user:password@example.invalid token=abc secret=xyz\n' * 30))
            lines = GUARD.daemon_diagnostics(path)
            self.assertEqual(len(lines), 20)
            self.assertTrue(all("[REDACTED]" in line for line in lines))
            self.assertNotIn("user:password", "\n".join(lines))
            self.assertNotIn("abc", "\n".join(lines))
            self.assertNotIn("xyz", "\n".join(lines))
            self.assertNotIn("not uploaded", "\n".join(lines))

    def test_rescued_leak_stays_failed_and_preserves_owned_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            marker = root / "journal"
            marker.write_text("evidence")
            (root / "work").mkdir(mode=0o700)
            (root / "work/fifo").mkdir(mode=0o700)
            runtime = GUARD.Runtime(root)
            calls = {"tasks": 0, "containers": 0}

            def ctr(*args, **_kwargs):
                if args[:2] in (("tasks", "list"), ("containers", "list")):
                    calls[args[0]] += 1
                    return "arcentra-fixture\n" if calls[args[0]] == 1 else ""
                if args[:2] == ("snapshots", "list"):
                    return "KEY PARENT KIND\nfixture parent Active\n"
                return ""

            with mock.patch.object(runtime, "discover", return_value=["arcentra-acceptance-test"]), mock.patch.object(runtime, "ctr", side_effect=ctr), mock.patch.object(GUARD, "owned_mounts", return_value=[]), mock.patch.object(GUARD, "pid_identity", return_value=None):
                report = runtime.cleanup()
            self.assertFalse(report["passed"])
            self.assertEqual(report["cleanupErrors"], [])
            self.assertTrue(report["leaksBeforeFallback"])
            self.assertEqual(marker.read_text(), "evidence")

    def test_fifo_directory_requires_private_runner_ownership(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "work").mkdir(mode=0o700)
            fifo = root / "work/fifo"
            fifo.mkdir(mode=0o700)
            self.assertEqual(GUARD.inspect_fifo_directory(root, os.getuid(), os.getgid()), 0)
            with self.assertRaisesRegex(RuntimeError, "runner-owned mode 0700"):
                GUARD.inspect_fifo_directory(root, os.getuid() + 1, os.getgid())
            with self.assertRaisesRegex(RuntimeError, "runner-owned mode 0700"):
                GUARD.inspect_fifo_directory(root, os.getuid(), os.getgid() + 1)
            fifo.chmod(0o755)
            with self.assertRaisesRegex(RuntimeError, "runner-owned mode 0700"):
                GUARD.inspect_fifo_directory(root, os.getuid(), os.getgid())

    def test_fifo_directory_rejects_symlinks_and_missing_root(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "work").mkdir(mode=0o700)
            with self.assertRaises(FileNotFoundError):
                GUARD.inspect_fifo_directory(root, os.getuid(), os.getgid())
            outside = root / "outside"
            outside.mkdir(mode=0o700)
            (root / "work/fifo").symlink_to(outside)
            with self.assertRaisesRegex(RuntimeError, "symlinks"):
                GUARD.inspect_fifo_directory(root, os.getuid(), os.getgid())

    def test_fifo_leak_fails_cleanup_and_retains_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "work").mkdir(mode=0o700)
            fifo = root / "work/fifo"
            fifo.mkdir(mode=0o700)
            marker = fifo / "unexpected-entry"
            marker.write_text("residual client state")
            runtime = GUARD.Runtime(root)
            with mock.patch.object(runtime, "discover", return_value=[]), mock.patch.object(GUARD, "owned_mounts", return_value=[]), mock.patch.object(GUARD, "pid_identity", return_value=None):
                report = runtime.cleanup()
            self.assertFalse(report["passed"])
            self.assertEqual(report["cleanupErrors"], [])
            self.assertEqual(report["leaksBeforeFallback"], [{"clientFIFOEntries": 1}])
            self.assertTrue(marker.is_file())

    def test_shell_keeps_test_execution_unprivileged(self):
        script = Path(__file__).with_name("run_containerd_runtime.sh").read_text()
        self.assertEqual(script.count("sudo -n"), 1)
        self.assertNotIn("sudo -E", script)
        self.assertIn('env -i', script)
        self.assertIn('ARCENTRA_CONTAINERD_RUNC_ROOT=$BASE/runc', script)
        self.assertIn('"$BASE/work/fifo"', script)
        self.assertIn('ARCENTRA_CONTAINERD_FIFO_DIR=$BASE/work/fifo', script)
        self.assertIn('sha256sum --check --strict', script)
        self.assertIn('Missing mandatory runtime cleanup receipt', script)

    def test_shell_repeats_sessions_inside_existing_deadline(self):
        script = Path(__file__).with_name("run_containerd_runtime.sh").read_text()
        self.assertIn('"-test.count=$repetitions"', script)
        self.assertIn(' 5m sessions 3 ', script)
        self.assertIn('SESSION_LANE_STARTED=$SECONDS', script)
        self.assertIn('SESSION_LANE_REMAINING=$((300 - (SECONDS - SESSION_LANE_STARTED)))', script)
        self.assertIn('SESSION_LANE_REMAINING=120', script)
        self.assertIn('"${SESSION_LANE_REMAINING}s" step-restart 1', script)
        self.assertIn('TestLiveStepJournalRestartContainerdAcceptance/successor-consumed', script)
        self.assertIn(' 3m sandbox 1 TestContainerdAcceptance', script)
        self.assertIn(' 8m full-job 1 TestRealContainerJobAcceptance', script)
        for case in ("same-live-session-immutable-inputs-ack-loss", "earlier-background-refused", "adopted-zombie-refused", "cancel-before-successor", "uncertain-resume-cleans"):
            self.assertIn('TestRealContainerJobAcceptance/failed-step/' + case, script)
        self.assertIn('TestDurableControllerContainerdAcceptance/cancel TestDurableControllerContainerdAcceptance/timeout', script)
        self.assertIn('verify_containerd_test_log.py "$EVIDENCE/$name.log" "$repetitions" "$@"', script)


if __name__ == "__main__":
    unittest.main()
