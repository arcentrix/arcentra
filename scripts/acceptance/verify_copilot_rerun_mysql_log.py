"""Strict witness for all terminal rerun MySQL gates and both collations."""
import re
import sys
from pathlib import Path

COLLATIONS = ("utf8mb4_0900_ai_ci", "utf8mb4_bin")
PACKAGE = "github.com/arcentrix/arcentra/internal/control/repo"
SCENARIOS = {'TestCopilotRerunCancelActorMySQLRequiredGate': ['claim-first',
                                                  'cancel-first',
                                                  'actor-revocation-wait',
                                                  'grant-expiry-wait',
                                                  'pointer-fence-wait'],
 'TestCopilotRerunClaimMySQLRequiredGate': ['claim-first',
                                            'cancel-first',
                                            'same-incarnation',
                                            'different-claim',
                                            'restart-nonce',
                                            'expiry-wait',
                                            'revocation-wait',
                                            'rollback',
                                            'lost-commit-ack',
                                            'schedule-expiry-wait',
                                            'schedule-authority-expiry',
                                            'schedule-lease-takeover',
                                            'storage-pin',
                                            'storage-disabled-wait',
                                            'storage-rollback',
                                            'input-pin',
                                            'input-rollback',
                                            'input-schedule-expiry',
                                            'input-schedule-cancel',
                                            'generation-bootstrap',
                                            'generation-startup-expiry',
                                            'generation-startup-grant-expiry',
                                            'generation-vote-cancel'],
 'TestCopilotRerunConfirmMySQLRequiredGate': ['job-attachment-first',
                                              'job-confirm-first',
                                              'step-attachment-first',
                                              'step-confirm-first',
                                              'job-move-attachment-first',
                                              'job-move-confirm-first',
                                              'step-move-attachment-first',
                                              'step-move-confirm-first',
                                              'step-job-only-attachment-first',
                                              'step-job-only-confirm-first',
                                              'job-only-run-move',
                                              'job-step-index',
                                              'duplicate-confirm',
                                              'cancel-first',
                                              'confirm-first',
                                              'revocation-wait',
                                              'policy-wait',
                                              'expiry-wait',
                                              'rollback-inserts',
                                              'lost-commit-ack',
                                              'canonical-identity'],
 'TestCopilotRerunDispatchMySQLRequiredGate': [],
 'TestCopilotRerunExpiryMySQLRequiredGate': ['claim-first', 'expiry-first', 'lost-expiry-ack'],
 'TestCopilotRerunReportsMySQLRequiredGate': ['report-first',
                                              'cancel-first',
                                              'duplicate-finalizer',
                                              'artifact-rollback',
                                              'lost-finalizer-ack'],
 'TestCopilotRerunSchemaMySQLRequiredGate': ['artifact-indexes',
                                             'artifact-wrong-prefix',
                                             'artifact-wrong-full-index',
                                             'artifact-index-only',
                                             'artifact-project-index-only',
                                             'artifact-both-index-only',
                                             'nonce-upgrade',
                                             'nonce-gate-claimed',
                                             'nonce-job-running',
                                             'nonce-schedule-claimed',
                                             'fresh',
                                             'audit-preflight-order',
                                             'upgrade',
                                             'index-only',
                                             'complete-markers',
                                             'view-ledger',
                                             'partial-marker',
                                             'wrong-prefix',
                                             'late-preflight'],
 'TestCopilotRerunSealMySQLRequiredGate': ['seal-first',
                                           'cancel-first',
                                           'duplicate-seal',
                                           'lease-takeover',
                                           'expiry-wait',
                                           'revocation-wait',
                                           'rollback',
                                           'lost-commit-ack']}


def required_names():
    names = []
    for root, scenarios in SCENARIOS.items():
        names.append(root)
        for collation in COLLATIONS:
            parent = root + "/" + collation
            names.append(parent)
            names.extend(parent + "/" + scenario for scenario in scenarios)
            if root == "TestCopilotRerunSchemaMySQLRequiredGate":
                names.extend(parent + "/artifact-indexes/" + table for table in
                             ("step_run_artifact", "project_artifact"))
    return names


def verify(text):
    if not isinstance(text, str) or len(text.encode("utf-8")) > 16 * 1024 * 1024:
        raise ValueError("rerun MySQL log invalid or oversized")
    if re.search(r"--- (?:FAIL|SKIP):|^FAIL(?:\s|$)|^panic:|^WARNING: DATA RACE", text, re.MULTILINE):
        raise ValueError("rerun MySQL failed, skipped, panicked or raced")
    runs = re.findall(r"^\s*=== RUN\s+(\S+)\s*$", text, re.MULTILINE)
    passes = re.findall(r"^\s*--- PASS: (\S+) \([0-9]+(?:\.[0-9]+)?s\)\s*$", text, re.MULTILINE)
    expected = sorted(required_names())
    if sorted(runs) != expected or sorted(passes) != expected:
        raise ValueError("all rerun roots, collations and cases must run/pass exactly once")
    markers = (
        ("schema_copilot_rerun_artifact_mysql_test.go", "artifact exact absent lookup actual work: ", 4),
        ("repo_copilot_rerun_reports_mysql_test.go", "exact postclaim prefix wait, terminal aggregate and once-only output receipt verified", 6),
    )
    for source, marker, count in markers:
        prefixed = re.findall(r"^\s*" + re.escape(source) + r":[0-9]+: " + re.escape(marker), text, re.MULTILINE)
        if len(prefixed) != count or text.count(marker) != count:
            raise ValueError("required actual-work/terminal-output witness missing or duplicated")
    completions = re.findall(r"^ok\s+" + re.escape(PACKAGE) + r"\s+[0-9]+(?:\.[0-9]+)?s\s*$", text, re.MULTILINE)
    if len(completions) != 1 or len(re.findall(r"^PASS\s*$", text, re.MULTILINE)) != 1:
        raise ValueError("one uncached successful package completion required")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("one complete rerun MySQL log is required")
    path = Path(sys.argv[1])
    if not path.is_file() or path.stat().st_size > 16 * 1024 * 1024:
        raise SystemExit("rerun MySQL log missing or oversized")
    verify(path.read_text(encoding="utf-8"))
    print("Verified all terminal rerun MySQL cases and both collations")
