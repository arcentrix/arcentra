import copy
import unittest
from copilot_history_evidence import digest_text, validate_primary, validate_receipt


def fixture():
    contract = dict(actorId='actor', projectId='project', pipelineId='pipeline', runId='run',
                    stepId='step', phase='create', previous=None)
    turns = [dict(turnId='turn-' + str(n), requestId='request-' + str(n), sequence=n,
                  question='question ' + str(n), text='SYNTHETIC_HISTORY_REPLY_' + str(n) + ' [REDACTED]',
                  expiresAt='2026-11-18T00:00:00Z', state='completed', contentAvailable=True) for n in (1, 2)]
    plan = dict(planId='plan', turnId='turn-2', kind='new_run_review', state='reviewed', version=2,
                planDigest='a' * 64, expiresAt='2026-10-20T00:00:00Z', executionEnabled=False)
    receipt = dict(version='copilot-history-v1', **{key: contract[key] for key in ('phase', 'actorId', 'projectId', 'pipelineId', 'runId')},
                   conversationId='conversation', turns=[{**{k: t[k] for k in ('turnId', 'requestId', 'sequence', 'expiresAt')},
                       'questionSha256': digest_text(t['question']), 'textSha256': digest_text(t['text'])} for t in turns],
                   plan=plan, historySelections=[[], ['turn-1']], previewDigests=['c'*64, 'd'*64], uncheckedHistoryExcluded=True, sendCount=2, executionMutationCount=0)
    detail = dict(conversation={key: receipt[key] for key in ('conversationId', 'projectId', 'pipelineId', 'runId')}, turns=turns)
    return contract, receipt, detail, plan | {'executionStatus': 'Not executed'}


class HistoryEvidenceTest(unittest.TestCase):
    def test_exact_primary_and_restart(self):
        contract, receipt, detail, plan = fixture()
        validate_receipt(receipt, contract)
        validate_primary(detail, plan, receipt, ['synthetic-canary'])
        resume = copy.deepcopy(receipt)
        resume.update(phase='resume', sendCount=0)
        validate_receipt(resume, contract | dict(phase='resume', previous=receipt))
    def test_receipt_identity_counts_selection_and_execution(self):
        mutations = [lambda r: r.update(actorId='foreign'), lambda r: r.update(sendCount=1),
                     lambda r: r.update(executionMutationCount=1), lambda r: r.update(historySelections=[[], []]),
                     lambda r: r['plan'].update(executionEnabled=True),
                     lambda r: r['turns'][1].update(turnId='turn-1')]
        for mutation in mutations:
            contract, receipt, _, _ = fixture()
            mutation(receipt)
            with self.assertRaises(RuntimeError):
                validate_receipt(receipt, contract)
    def test_primary_missing_changed_secret_and_plan_fail(self):
        for kind in ('missing', 'changed', 'secret', 'plan', 'page'):
            _, receipt, detail, plan = fixture()
            if kind == 'missing': detail['turns'].pop()
            if kind == 'changed': detail['turns'][0]['text'] = 'changed'
            if kind == 'secret':
                detail['turns'][0]['text'] += ' synthetic-canary'
                receipt['turns'][0]['textSha256'] = digest_text(detail['turns'][0]['text'])
            if kind == 'plan': plan['executionStatus'] = 'executed'
            if kind == 'page': detail['nextAfterSequence'] = 2
            with self.subTest(kind=kind), self.assertRaises(RuntimeError):
                validate_primary(detail, plan, receipt, ['synthetic-canary'])
    def test_restart_cannot_replace_persisted_plan(self):
        contract, receipt, _, _ = fixture()
        resume = copy.deepcopy(receipt)
        resume.update(phase='resume', sendCount=0)
        resume['plan']['version'] = 3
        with self.assertRaises(RuntimeError):
            validate_receipt(resume, contract | dict(phase='resume', previous=receipt))

if __name__ == '__main__':
    unittest.main()
