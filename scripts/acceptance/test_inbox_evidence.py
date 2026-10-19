import copy
import unittest
import inbox_evidence as evidence


class InboxEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.failed = {'projectId': 'project', 'pipelineId': 'pipeline', 'runId': 'failed', 'status': 'failed'}
        self.successful = self.failed | {'runId': 'success', 'status': 'success'}
        def item(expected, read):
            return {'id': 'entry-' + expected['runId'], 'kind': 'run_result', 'status': expected['status'],
                    'occurredAt': '2026-10-18T19:00:00Z', 'deliveredAt': '2026-10-18T19:00:05Z',
                    'readAt': '2026-10-18T19:00:10Z' if read else None,
                    'target': {'type': 'run', **{key: expected[key] for key in ('projectId', 'pipelineId', 'runId')}},
                    'availability': 'available'}
        self.receipt = {'journey': 'personal-inbox-v1', 'phase': 'read', 'completed': True,
                        'steps': evidence.READ_STEPS.copy(), 'userId': 'actor',
                        'readEntry': item(self.successful, True), 'unreadEntry': item(self.failed, False)}

    def validate(self, receipt):
        return evidence.validate_receipt(receipt, 'read', 'actor', self.failed, self.successful)

    def test_exact_read_and_restart(self):
        self.validate(self.receipt)
        resumed = self.receipt | {'phase': 'resume', 'steps': evidence.RESUME_STEPS.copy()}
        evidence.validate_receipt(resumed, 'resume', 'actor', self.failed, self.successful, self.receipt)
        evidence.validate_primary_snapshot({'list': [self.receipt['unreadEntry'], self.receipt['readEntry']],
                                            'nextCursor': None, 'scanIncomplete': False}, self.receipt)

    def test_foreign_actor_partial_stages_and_extra_payload_rejected(self):
        for patch in ({'userId': 'other'}, {'completed': 1}, {'completed': False}, {'phase': 'resume'},
                      {'steps': evidence.READ_STEPS[:-1]}, {'secret': 'synthetic-not-allowed'}, {'journey': 'other'}):
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                self.validate(self.receipt | patch)

    def test_exact_scope_and_terminal_read_state(self):
        for key in ('projectId', 'pipelineId', 'runId', 'type'):
            value = copy.deepcopy(self.receipt);value['readEntry']['target'][key] = 'other'
            with self.subTest(key=key), self.assertRaises(ValueError): self.validate(value)
        for patch in ({'status': 'failed'}, {'kind': 'approval_result'}, {'availability': 'unavailable'},
                      {'target': None}, {'readAt': None}, {'readAt': '2026-10-18T19:00:00'},
                      {'ciphertext': 'synthetic-not-allowed'}, {'id': '../foreign'}):
            value = copy.deepcopy(self.receipt);value['readEntry'].update(patch)
            with self.subTest(patch=patch), self.assertRaises(ValueError): self.validate(value)
        value = copy.deepcopy(self.receipt);value['unreadEntry']['readAt'] = '2026-10-18T19:00:10Z'
        with self.assertRaises(ValueError): self.validate(value)

    def test_restart_requires_same_delivery_and_original_read_timestamp(self):
        for field, changed in [('id', 'new-delivery'), ('readAt', '2026-10-18T19:00:20Z'),
                               ('deliveredAt', '2026-10-18T19:00:20Z')]:
            value = copy.deepcopy(self.receipt);value.update(phase='resume', steps=evidence.RESUME_STEPS.copy())
            value['readEntry'][field] = changed
            with self.subTest(field=field), self.assertRaises(ValueError):
                evidence.validate_receipt(value, 'resume', 'actor', self.failed, self.successful, self.receipt)
        value = self.receipt | {'phase': 'resume', 'steps': evidence.RESUME_STEPS.copy()}
        with self.assertRaises(ValueError):
            evidence.validate_receipt(value, 'resume', 'actor', self.failed, self.successful)

    def test_primary_snapshot_rejects_missing_duplicate_hidden_or_incomplete(self):
        base = {'list': [self.receipt['readEntry'], self.receipt['unreadEntry']], 'nextCursor': None, 'scanIncomplete': False}
        for patch in ({'list': base['list'][:1]}, {'list': base['list'] + base['list']},
                      {'scanIncomplete': True}, {'nextCursor': 'remaining'}, {'extra': 'not-projected'}):
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                evidence.validate_primary_snapshot(base | patch, self.receipt)
        value = copy.deepcopy(base);value['list'][0]['readAt'] = None
        with self.assertRaises(ValueError): evidence.validate_primary_snapshot(value, self.receipt)
