"""Required-mode witness for the exact disposable two-collation history suite."""
import re
import sys
from pathlib import Path

ROOT = 'TestCopilotHistoryMySQLRealRequiredGate'


def verify(text):
    required = [ROOT, ROOT + '/utf8mb4_0900_ai_ci', ROOT + '/utf8mb4_bin']
    if re.search(r'--- (?:FAIL|SKIP):|^FAIL(?:\s|$)', text, re.MULTILINE):
        raise ValueError('history MySQL gate failed or skipped')
    for name in required:
        if len(re.findall(r'^\s*=== RUN\s+' + re.escape(name) + r'\s*$', text, re.MULTILINE)) != 1:
            raise ValueError('history MySQL case was not run exactly once')
        if len(re.findall(r'^\s*--- PASS: ' + re.escape(name) + r' \(', text, re.MULTILINE)) != 1:
            raise ValueError('history MySQL case did not pass exactly once')
    if not re.search(r'^ok\s+github.com/arcentrix/arcentra/internal/control/repo\s', text, re.MULTILINE):
        raise ValueError('history MySQL package did not complete')


if __name__ == '__main__':
    if len(sys.argv) != 2:
        raise SystemExit('one history MySQL log is required')
    path = Path(sys.argv[1])
    if not path.is_file() or path.stat().st_size > 8 * 1024 * 1024:
        raise SystemExit('history MySQL log missing or oversized')
    verify(path.read_text())
    print('Verified exact private-history MySQL cases in both collations')
