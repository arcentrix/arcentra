"""Required-mode witness for the exact two-collation execution MySQL gate."""
import re
import sys
from pathlib import Path

ROOT = "TestCopilotExecutionMySQLRealRequiredGate"
COLLATIONS = ("utf8mb4_0900_ai_ci", "utf8mb4_bin")
SCENARIOS = (
    "cancel-wins", "confirm-wins", "duplicate-ack", "revocation-wait",
    "expiry-wait", "ordinary-vote", "plan-cancel-wait", "detached-history",
    "current-read-denial", "sparse-list", "scope-lock-deadlock", "username-login-identifiers",
)
MARKER = "execution scoped list actual-work and exact one-row content proof passed: "
PACKAGE = "github.com/arcentrix/arcentra/internal/control/repo"


def required_names():
    names = [ROOT]
    for collation in COLLATIONS:
        parent = ROOT + "/" + collation
        names.append(parent)
        names.extend(parent + "/" + scenario for scenario in SCENARIOS)
    return names


def verify(text):
    if not isinstance(text, str) or len(text.encode("utf-8")) > 8 * 1024 * 1024:
        raise ValueError("execution MySQL log invalid or oversized")
    if re.search(r"--- (?:FAIL|SKIP):|^FAIL(?:\s|$)|^panic:|^WARNING: DATA RACE", text, re.MULTILINE):
        raise ValueError("execution MySQL gate failed, skipped, panicked or raced")
    runs = re.findall(r"^\s*=== RUN\s+(\S+)\s*$", text, re.MULTILINE)
    passes = re.findall(r"^\s*--- PASS: (\S+) \([0-9]+(?:\.[0-9]+)?s\)\s*$", text, re.MULTILINE)
    expected = required_names()
    if sorted(runs) != sorted(expected) or sorted(passes) != sorted(expected):
        raise ValueError("execution MySQL cases must each run and pass exactly once")
    marker_lines = re.findall(
        r"^\s*repo_copilot_execution_mysql_test\.go:[0-9]+: "
        + re.escape(MARKER) + r"(\S+)\s*$", text, re.MULTILINE)
    if sorted(marker_lines) != sorted(COLLATIONS):
        raise ValueError("execution actual-work/content witness missing, duplicated or unexpected")
    # Also reject duplicates that have lost their Go source-line prefix.
    if len(re.findall(re.escape(MARKER), text)) != len(COLLATIONS):
        raise ValueError("execution actual-work/content witness malformed or duplicated")
    completions = re.findall(r"^ok\s+" + re.escape(PACKAGE) + r"\s+([0-9]+(?:\.[0-9]+)?s)\s*$", text, re.MULTILINE)
    if len(completions) != 1:
        raise ValueError("execution MySQL package must complete exactly once uncached")
    if len(re.findall(r"^PASS\s*$", text, re.MULTILINE)) != 1:
        raise ValueError("execution MySQL terminal PASS missing or duplicated")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("one execution MySQL log is required")
    path = Path(sys.argv[1])
    if not path.is_file() or path.stat().st_size > 8 * 1024 * 1024:
        raise SystemExit("execution MySQL log missing or oversized")
    verify(path.read_text(encoding="utf-8"))
    print("Verified exact bound-execution MySQL cases and actual-work witnesses in both collations")
