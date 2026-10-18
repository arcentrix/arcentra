"""Require every selected Inbox MySQL case, never infer success from exit alone."""
from pathlib import Path
import sys
from verify_containerd_test_log import verify


def expected(suite):
    collations = ('utf8mb4_0900_ai_ci', 'utf8mb4_bin')
    if suite == 'provenance':
        root = 'TestManualInboxMySQLProvenanceBothCollations'
        return [root, *[f'{root}/{c}' for c in collations]]
    if suite == 'migration':
        root = 'TestInboxMySQLRealOldShapeMigrationAndLargeHistoryPlans'
        return [root, *[f'{root}/{c}' for c in collations]]
    if suite == 'service':
        root = 'TestInboxMySQLVerifiedRetryServiceJourney'
        return [root, *[f'{root}/{c}/{mode}/{outcome}' for c in collations
                       for mode in ('whole_job', 'failed_step') for outcome in ('approved', 'cancelled')]]
    raise ValueError('Unknown Inbox MySQL suite')


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit('usage: verify_inbox_mysql_log.py LOG SUITE')
    verify(Path(sys.argv[1]).read_text(), 1, expected(sys.argv[2]))
