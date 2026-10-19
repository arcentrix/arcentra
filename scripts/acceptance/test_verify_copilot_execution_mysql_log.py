import unittest
import re
from pathlib import Path
from verify_copilot_execution_mysql_log import (
    ROOT, COLLATIONS, SCENARIOS, MARKER, PACKAGE, required_names, verify,
)


class RequiredExecutionMySQLLogTest(unittest.TestCase):
    def valid(self):
        lines = ["=== RUN   " + name for name in required_names()]
        lines += ["    repo_copilot_execution_mysql_test.go:321: " + MARKER + c for c in COLLATIONS]
        lines += ["--- PASS: " + name + " (0.01s)" for name in reversed(required_names())]
        return "\n".join(lines + ["PASS", "ok\t" + PACKAGE + "\t1.23s", ""])

    def test_complete(self):
        verify(self.valid())

    def test_actual_history_observe_contention_required_in_both_collations(self):
        self.assertEqual(len(SCENARIOS), 17)
        self.assertEqual(len(required_names()), 1 + 2 + 17 * 2)
        for collation in COLLATIONS:
            for scenario in ("history-reserve-observe-contention", "history-review-observe-contention",
                             "run-cancel-observe-contention", "approval-cancel-observe-contention",
                             "approval-expire-observe-contention"):
                name = ROOT + "/" + collation + "/" + scenario
                self.assertIn(name, required_names())
                # An older complete 12-case-per-collation log is insufficient.
                old = self.valid().replace("=== RUN   " + name + "\n", "")
                old = old.replace("--- PASS: " + name + " (0.01s)\n", "")
                with self.subTest(name=name), self.assertRaises(ValueError):
                    verify(old)

    def test_each_case_must_run_and_pass_once(self):
        for name in required_names():
            for record in ("=== RUN   " + name, "--- PASS: " + name + " (0.01s)"):
                for bad in (self.valid().replace(record + "\n", ""), self.valid() + record + "\n"):
                    with self.subTest(name=name, record=record), self.assertRaises(ValueError):
                        verify(bad)

    def test_each_actual_work_content_marker_required_once(self):
        for c in COLLATIONS:
            marker = "    repo_copilot_execution_mysql_test.go:321: " + MARKER + c
            for bad in (self.valid().replace(marker, ""), self.valid() + marker + "\n",
                        self.valid() + MARKER + c + "\n", self.valid().replace(marker, MARKER + c)):
                with self.subTest(collation=c), self.assertRaises(ValueError):
                    verify(bad)

    def test_reject_failure_skip_panic_race_and_unknown_case(self):
        for suffix in ("--- SKIP: other (0.01s)", "--- FAIL: other (0.01s)", "FAIL",
                       "FAIL\t" + PACKAGE, "panic: fixture failed", "WARNING: DATA RACE",
                       "=== RUN   unexpected", "--- PASS: unexpected (0.01s)"):
            with self.subTest(suffix=suffix), self.assertRaises(ValueError):
                verify(self.valid() + suffix + "\n")

    def test_package_and_terminal_pass_are_exact_uncached(self):
        for bad in (self.valid().replace("ok\t", "not-ok\t"),
                    self.valid().replace("1.23s", "(cached)"),
                    self.valid() + "ok\t" + PACKAGE + "\t1.23s\n",
                    self.valid().replace("\nPASS\n", "\n"), self.valid() + "PASS\n"):
            with self.assertRaises(ValueError):
                verify(bad)

    def test_reject_oversized_log(self):
        with self.assertRaises(ValueError):
            verify("x" * (8 * 1024 * 1024 + 1))

    def test_workflow_required_gate_contract(self):
        root = Path(__file__).resolve().parents[2]
        workflow = (root / ".github/workflows/ci.yml").read_text()
        history = workflow.index("      - name: Verify private Copilot history against disposable MySQL")
        execution = workflow.index("      - name: Verify bound Copilot execution against disposable MySQL")
        frontend = workflow.index("      - name: Check out pinned frontend acceptance client")
        self.assertLess(history, execution)
        self.assertLess(execution, frontend)
        step = workflow[execution:frontend]
        for exact in (
            "timeout-minutes: 4", "set -o pipefail", "schema=arcentra_copilot_execution_test_ci",
            "ARCENTRA_REQUIRE_COPILOT_EXECUTION_MYSQL=1", "@tcp(127.0.0.1:13306)/$schema?",
            "go test -race -v -count=1 -timeout=3m -run '^" + ROOT + "$' ./internal/control/repo",
            "| tee .acceptance/evidence/copilot-execution-mysql.log",
            "python3 scripts/acceptance/verify_copilot_execution_mysql_log.py .acceptance/evidence/copilot-execution-mysql.log",
        ):
            self.assertIn(exact, step)
        self.assertEqual(step.count("mysqladmin -uroot create"), 1)
        # The explicit witness must cover each current Go subtest, not silently
        # remain green when a fixture scenario is added or renamed.
        source = (root / "internal/control/repo/repo_copilot_execution_mysql_test.go").read_text()
        declared = re.search(r'for _, scenario := range \[\]string\{([^}]+)\}', source)
        self.assertIsNotNone(declared)
        self.assertEqual(tuple(re.findall(r'"([^"\n]+)"', declared.group(1))), SCENARIOS)
        self.assertIn('t.Log("' + MARKER + '" + collation)', source)


if __name__ == "__main__":
    unittest.main()
