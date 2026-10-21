"""Fail-closed regressions for the complete terminal rerun required-mode log."""
import unittest

from verify_copilot_rerun_mysql_log import PACKAGE, COLLATIONS, SCENARIOS, required_names, verify


class RerunMySQLLogTests(unittest.TestCase):
    @staticmethod
    def valid():
        records = []
        for name in required_names():
            records.extend(("=== RUN   " + name, "--- PASS: " + name + " (0.01s)"))
        records.extend(["    schema_copilot_rerun_artifact_mysql_test.go:140: artifact exact absent lookup actual work: -> fixture"] * 4)
        records.extend(["    repo_copilot_rerun_reports_mysql_test.go:224: exact postclaim prefix wait, terminal aggregate and once-only output receipt verified"] * 6)
        records.extend(("PASS", "ok\t" + PACKAGE + "\t123.45s"))
        return "\n".join(records) + "\n"

    def test_complete_matrix(self):
        self.assertEqual(len(SCENARIOS), 8)
        self.assertEqual(sum(len(v) for v in SCENARIOS.values()), 96)
        self.assertEqual(len(required_names()), 220)
        self.assertEqual(len(set(required_names())), 220)
        verify(self.valid())

    def test_every_run_and_pass_required_exactly_once(self):
        original = self.valid()
        for name in required_names():
            for record in ("=== RUN   " + name, "--- PASS: " + name + " (0.01s)"):
                for changed in (original.replace(record + "\n", ""), original + record + "\n"):
                    with self.subTest(name=name, record=record), self.assertRaises(ValueError):
                        verify(changed)

    def test_markers_require_source_and_exact_count(self):
        for line in self.valid().splitlines():
            if ".go:" not in line:
                continue
            for changed in (self.valid().replace(line + "\n", "", 1),
                            self.valid() + line + "\n",
                            self.valid().replace(line, line.split(": ", 1)[1], 1)):
                with self.assertRaises(ValueError):
                    verify(changed)

    def test_reject_incomplete_old_matrix(self):
        text = self.valid()
        for name in required_names():
            if "CancelActor" in name or "artifact-indexes/" in name:
                text = text.replace("=== RUN   " + name + "\n", "")
                text = text.replace("--- PASS: " + name + " (0.01s)\n", "")
        with self.assertRaises(ValueError):
            verify(text)

    def test_source_membership_races_required_in_both_collations(self):
        for collation in COLLATIONS:
            for scenario in ("source-job-attachment-first", "source-job-confirm-first",
                             "source-job-move-attachment-first", "source-job-move-confirm-first"):
                name = "TestCopilotRerunConfirmMySQLRequiredGate/" + collation + "/" + scenario
                self.assertIn(name, required_names())
                missing = self.valid().replace("=== RUN   " + name + "\n", "")
                missing = missing.replace("--- PASS: " + name + " (0.01s)\n", "")
                with self.subTest(name=name), self.assertRaises(ValueError):
                    verify(missing)

    def test_cross_parent_races_required_in_both_collations(self):
        for collation in COLLATIONS:
            for scenario in ("source-step-move", "source-step-job", "original-job-move", "original-job-duplicate"):
                for winner in ("attachment-first", "confirm-first"):
                    name = "TestCopilotRerunConfirmMySQLRequiredGate/" + collation + "/" + scenario + "-" + winner
                    self.assertIn(name, required_names())
                    missing = self.valid().replace("=== RUN   " + name + "\n", "")
                    missing = missing.replace("--- PASS: " + name + " (0.01s)\n", "")
                    with self.subTest(name=name), self.assertRaises(ValueError):
                        verify(missing)

    def test_reject_failure_skip_panic_race_unknown_case(self):
        for suffix in ("--- SKIP: hidden (0.01s)", "--- FAIL: hidden (0.01s)", "FAIL",
                       "FAIL\t" + PACKAGE, "panic: timed out", "WARNING: DATA RACE",
                       "=== RUN   unexpected", "--- PASS: unexpected (0.01s)"):
            with self.subTest(suffix=suffix), self.assertRaises(ValueError):
                verify(self.valid() + suffix + "\n")

    def test_uncached_single_completion_required(self):
        for bad in (self.valid().replace("ok\t", "not-ok\t"),
                    self.valid().replace("123.45s", "(cached)"),
                    self.valid().replace("PASS\nok", "ok"),
                    self.valid() + "PASS\n",
                    self.valid() + "ok\t" + PACKAGE + "\t1.23s\n"):
            with self.assertRaises(ValueError):
                verify(bad)

    def test_reject_invalid_or_oversized(self):
        for bad in (None, "x" * (16 * 1024 * 1024 + 1)):
            with self.assertRaises(ValueError):
                verify(bad)


if __name__ == "__main__":
    unittest.main()
