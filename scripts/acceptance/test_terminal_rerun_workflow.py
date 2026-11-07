"""Offline wiring checks; these do not claim any MySQL or runtime acceptance."""
from pathlib import Path
import re
import subprocess
import unittest

from verify_copilot_rerun_mysql_log import SCENARIOS


ROOT = Path(__file__).resolve().parents[2]


class TerminalRerunWorkflowTests(unittest.TestCase):
    def test_required_mysql_selector_matches_strict_witness(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        start = workflow.index("      - name: Verify terminal rerun durable boundaries against disposable MySQL\n")
        end = workflow.index("      - name:", start + 10)
        step = workflow[start:end]
        selector = re.search(r"-run '([^']+)'", step).group(1)
        self.assertTrue(all(re.fullmatch(selector, name) for name in SCENARIOS))
        for bad in ("TestCopilotRerunOptional", "TestCopilotRerunConfirmMySQLRequiredGateExtra", "TestCopilotRerunConfirmMySQL"):
            self.assertIsNone(re.fullmatch(selector, bad))
        self.assertIn("timeout-minutes: 9", step)
        self.assertIn("-race -v -count=1 -timeout=8m", step)
        self.assertIn("set -o pipefail", step)
        self.assertIn("ARCENTRA_REQUIRE_COPILOT_RERUN_MYSQL=1", step)
        self.assertIn("schema=arcentra_copilot_rerun_test_ci", step)
        self.assertIn('mysqladmin -uroot create "$schema"', step)
        self.assertIn("python3 scripts/acceptance/verify_copilot_rerun_mysql_log.py .acceptance/evidence/copilot-rerun-mysql.log", step)
        self.assertNotIn("continue-on-error", step)
        self.assertNotIn("|| true", step)
        self.assertNotIn("-parallel", step)

    def test_observer_guards_are_selected_before_runtime(self):
        workflow = (ROOT / ".github/workflows/containerd-acceptance.yml").read_text()
        selectors = re.findall(r"-run '([^']+)'", workflow)
        for name in ("TestContainerTerminalRerunLostClaimBoundary", "TestContainerTerminalRerunObserverActivationConcurrent"):
            self.assertTrue(any(re.fullmatch(selector, name) for selector in selectors), name)

    def test_real_router_authorization_is_selected_before_runtime(self):
        workflow = (ROOT / ".github/workflows/containerd-acceptance.yml").read_text()
        line = next(line for line in workflow.splitlines() if "go test -race -tags=sandbox_acceptance ./internal/control/router" in line)
        selector = re.search(r"-run '([^']+)'", line).group(1)
        for name in ("TestCopilotRerunFullRouteGroupIdentityAndLegacyIsolation", "TestCopilotRerunProductionRouterDefaultOff", "TestCopilotRerunHTTPStaticErrors"):
            self.assertIsNotNone(re.fullmatch(selector, name), name)
        self.assertIn("-count=1 -timeout=2m -v", line)
        self.assertNotIn("||", line)

    def test_frontend_status_failure_is_not_clean(self):
        script = (ROOT / "scripts/acceptance/run_containerd_runtime.sh").read_text()
        guard = script[script.index("FRONTEND_HEAD=$("):script.index("BASE=$(mktemp")]
        for status, head, expected in (("return 42", "a" * 40, False), ("printf dirty", "a" * 40, False), ("return 0", "b" * 40, False), ("return 0", "a" * 40, True)):
            shell = "set -euo pipefail\nfake_git() { if [[ $1 == rev-parse ]]; then printf %s " + head + "; else " + status + "; fi; }\nFRONTEND_GIT=(fake_git)\nARCENTRA_COPILOT_BROWSER_FRONTEND_COMMIT=" + "a" * 40 + "\n" + guard
            result = subprocess.run(["/bin/bash", "-c", shell], env={"PATH": "/usr/bin:/bin"}, capture_output=True, timeout=3)
            self.assertEqual(result.returncode == 0, expected)

    def test_owner_observation_follows_success_without_changing_reviewer_contract(self):
        source = (ROOT / "internal/agent/taskqueue/real_container_terminal_rerun_acceptance_test.go").read_text()
        reviewer = source.index('httpFixture.browser(t, f, "outcome", policy.Candidates[1], contract, &outcome)')
        success = source.index('f.waitRun(confirmed.NewRunID, model.PipelineStatusSuccess)', reviewer)
        output_bytes = source.index('!bytes.Equal(actual, []byte(terminalRerunOutput))', success)
        owner = source.index('httpFixture.browser(t, f, "owner-outcome", actor, contract, &ownerOutcome)', output_bytes)
        validate_owner = source.index('!ownerOutcome.validFor(wantOwnerOutcome)', owner)
        owner_login = source.index('httpFixture.login(t, f.ctx, actor)', validate_owner)
        replay = source.index('var replay controlservice.CopilotRerunIntent', owner_login)
        self.assertLess(reviewer, success)
        self.assertLess(success, owner)
        self.assertLess(owner_login, replay)
        self.assertIn('"actorId": policy.Candidates[1]', source[:reviewer])
        self.assertIn('"actorId": actor', source[:reviewer])
        self.assertNotIn('contract["actorId"] =', source)
        self.assertIn('receipt["responseRecovery"] = preparation.ResponseRecovery', source)
        self.assertIn('receipt["ownerObserved"] = true', source)
        self.assertIn('receipt["targetCounts"] = targetCounts', source)

    def test_terminal_browser_recovery_and_owner_receipts_remain_separate(self):
        source = (ROOT / "internal/agent/taskqueue/real_container_terminal_rerun_acceptance_test.go").read_text()
        http = (ROOT / "internal/agent/taskqueue/real_container_terminal_rerun_http_test.go").read_text()
        receipts = (ROOT / "internal/agent/taskqueue/real_container_terminal_rerun_receipts_test.go").read_text()
        self.assertIn('!preparation.ResponseRecovery.valid()', source)
        for counter in ('preparation.PreparePosts != 1', 'preparation.ConfirmPosts != 1', 'preparation.ApprovalPosts != 0'):
            self.assertIn(counter, source)
        self.assertIn('terminalRerunBrowserReceiptName(phase)', http)
        self.assertIn('"copilot-rerun-"+name+"-receipt.json"', http)
        self.assertIn('case "outcome", "owner-outcome":', receipts)
        self.assertIn('"browser-owner-output-observed"', receipts)
        self.assertIn('r.OwnerOutcomeComplete && !r.BrowserOutcomeComplete', receipts)
        # The existing outer deadline and phase bounds are retained, not renewed.
        self.assertIn('deadline, ok := f.ctx.Deadline()', http)
        self.assertIn('context.WithTimeout(f.ctx, 50*time.Second)', http)
        self.assertIn('cmd.WaitDelay = 3 * time.Second', http)

    def test_terminal_target_counts_are_authoritative_and_post_drain(self):
        source = (ROOT / "internal/agent/taskqueue/real_container_terminal_rerun_acceptance_test.go").read_text()
        counts = (ROOT / "internal/agent/taskqueue/real_container_terminal_rerun_counts_test.go").read_text()
        joined = source.index('services.StopTerminalRerun(stopCtx)')
        drained = source.index('watermark := terminalRerunQueueDrained(t, f)', joined)
        counted = source.index('targetCounts := terminalRerunAssertTargetCounts(', drained)
        self.assertLess(drained, counted)
        self.assertIn('len(intents) != 4', counts)
        self.assertIn('browserRequestIntents != 1', counts)
        self.assertIn('counts[key] != 3', counts)
        self.assertIn('intent.NewRunID == cancelled.NewRunID', counts)
        self.assertIn('want = 0', counts)
        for model in ('PipelineRun', 'JobRun', 'CopilotRerunAdmissionGate', 'CopilotRerunSchedule'):
            self.assertIn('&model.' + model + '{}', counts)
        self.assertIn('db.WithContext(ctx).Model(item.row).Where(item.filter).Count(&count)', counts)

    def test_existing_security_and_time_boundaries_retained(self):
        runtime = (ROOT / ".github/workflows/containerd-acceptance.yml").read_text()
        self.assertIn("timeout-minutes: 25", runtime)
        self.assertIn("timeout-minutes: 19", runtime)
        self.assertIn("contents: read", runtime)
        self.assertNotIn("${{ secrets.", runtime)
        self.assertNotIn("self-hosted]", runtime)
        self.assertIn("persist-credentials: false", runtime)
        guard = (ROOT / "scripts/acceptance/containerd_runtime_guard.py").read_text()
        self.assertIn("MAX_LIFETIME_SECONDS = 18 * 60", guard)


if __name__ == "__main__":
    unittest.main()
