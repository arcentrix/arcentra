"""Require every named composed approval/step test in its isolated MySQL lane."""
import sys
from pathlib import Path
from verify_containerd_test_log import verify


def expected(suite):
    if suite == "repo":
        prefix = "TestStepRetryApprovalMySQL"
        names = [prefix + suffix for suffix in (
            "PendingQuorumAndStartup", "RevocationAndHistoricalACK",
            "CancellationExactLockOrdering", "ExpiryAfterAdmissionWriteRollsBack",
            "AuthorityChangesAcrossExactJobLock", "RevocationBlocksLaterLogicalStep",
            "SuccessfulProducerHistoricalReplyLockOrder",
        )]
        names += [prefix + "RevocationAndHistoricalACK/" + subject + "/" + phase
                  for subject in ("actor", "voter")
                  for phase in ("first", "successor", "running")]
        names += [prefix + "CancellationExactLockOrdering/cancel_first_" + value
                  for value in ("true", "false")]
        names += [prefix + "ExpiryAfterAdmissionWriteRollsBack/" + bound
                  for bound in ("approval", "boundary")]
        names += [prefix + "AuthorityChangesAcrossExactJobLock/" + change
                  for change in ("requester", "voter", "approval_expiry")]
        names += [prefix + "SuccessfulProducerHistoricalReplyLockOrder/" + operation
                  for operation in ("admit", "inspect", "approval_observe", "approval_admit", "generation_admit")]
        return names
    if suite == "service":
        prefix = "TestStepRetryApprovalMySQLService"
        return [prefix + "CurrentGraph", prefix + "CrossedGraphs"] + [
            prefix + "CurrentGraph/" + kind + "/" + stage
            for kind in ("role", "grant", "team", "binding")
            for stage in ("first-admission", "running")
        ]
    raise ValueError("unknown isolated MySQL suite")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("usage: verify_step_approval_mysql_log.py LOG repo|service")
    verify(Path(sys.argv[1]).read_text(), 1, expected(sys.argv[2]))
