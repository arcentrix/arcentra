"""Strict independent persisted evidence contract for the visual definition journey."""
import copy
import unittest
from visual_git_fixture import INITIAL_SPEC, MARKER, MAX_VISUAL_LOG_BYTES, MAX_VISUAL_LOG_ENTRIES, VISUAL_STEPS, saved_spec, validate_visual_receipt, validate_visual_execution, validate_visual_pipeline


class VisualEvidenceTests(unittest.TestCase):
    def test_save_failure_categories_never_return_untrusted_error_text(self):
        from visual_git_fixture import classify_visual_save_failure
        synthetic = 'fixture-private-url-token'
        self.assertEqual(classify_visual_save_failure('git commit failed: Committer identity unknown ' + synthetic), 'git-identity-missing')
        self.assertEqual(classify_visual_save_failure('git commit failed: Author identity unknown'), 'git-identity-missing')
        self.assertEqual(classify_visual_save_failure('git push failed: ' + synthetic), 'git-push-failed')
        self.assertEqual(classify_visual_save_failure('clone repo failed: ' + synthetic), 'git-clone-failed')
        for value in [None, {}, synthetic, 'git push failed:' + ('x' * 16385)]:
            self.assertEqual(classify_visual_save_failure(value), 'unclassified')

    def test_save_failure_diagnostic_is_scoped_read_only_and_redacted(self):
        import run_actual_app as actual
        from pathlib import Path
        from types import SimpleNamespace
        from unittest.mock import patch
        fixture = actual.Fixture.__new__(actual.Fixture)
        fixture.args = SimpleNamespace(visual_editor_acceptance=True, evidence_dir=Path('/synthetic'))
        fixture.visual_project_id = 'project'
        messages, requests = [], []
        fixture.note = messages.append
        def request(path):
            requests.append(path)
            return {'pipelineId': 'pipeline', 'projectId': 'project',
                    'lastSyncMessage': 'git push failed: fixture-private-url-token'}
        fixture.request = request
        summary = {'stage': 'visual-ci-save', 'visualCI': {'projectId': 'project', 'pipelineId': 'pipeline'}}
        with patch.object(actual, 'read_private_json', return_value=summary):
            fixture.visual_save_failure_diagnostic()
        self.assertEqual(requests, ['/pipelines/pipeline'])
        self.assertEqual(messages, ['Visual save failure diagnostic (persisted sync state only): git-push-failed'])
        requests.clear(); messages.clear()
        summary['visualCI']['projectId'] = 'other-project'
        with patch.object(actual, 'read_private_json', return_value=summary):
            fixture.visual_save_failure_diagnostic()
        self.assertEqual(requests, [])
        self.assertEqual(messages, ['Visual save failure diagnostic (persisted sync state only): unavailable'])

    def receipt(self):
        return {'journey': 'visual-ci-v1', 'completed': True, 'steps': list(VISUAL_STEPS),
                'projectId': 'project', 'pipelineId': 'pipeline', 'runId': 'run',
                'baseDefinitionCommit': 'a' * 40, 'savedDefinitionCommit': 'b' * 40}

    def fixture(self):
        receipt = self.receipt()
        run = {'runId': 'run', 'pipelineId': 'pipeline', 'status': 3,
               'definitionCommitSha': 'b' * 40, 'allowDeployments': False}
        jobs = {'total': 2, 'list': []}
        for index, expected in enumerate(saved_spec()['jobs']):
            identity = f'job-{index}'
            jobs['list'].append({'jobName': expected['name'], 'jobRunId': identity,
                                'pipelineId': 'pipeline', 'pipelineRunId': 'run', 'agentId': 'agent', 'status': 4,
                                'steps': [{'name': step['name'], 'stepRunId': f'step-{index}-{n}',
                                           'jobRunId': identity, 'status': 4, 'exitCode': 0}
                                          for n, step in enumerate(expected['steps'])]})
        logs = {'truncated': False, 'entries': [{'message': MARKER + '_' + suffix + '\n', 'jobId': job, 'stepId': step,
                                               'stream': 'stdout', 'lineNumber': 1}
                                             for suffix, job, step in [('EDITED', 'job-0', 'step-0-0'),
                                                                       ('VERIFY', 'job-0', 'step-0-1'),
                                                                       ('PACKAGE', 'job-1', 'step-1-0')]]}
        return receipt, run, jobs, logs

    def test_saved_definition_retains_unedited_metadata_and_declares_order(self):
        expected = saved_spec()
        self.assertEqual(expected['variables'], INITIAL_SPEC['variables'])
        self.assertEqual(expected['jobs'][0]['description'], INITIAL_SPEC['jobs'][0]['description'])
        self.assertEqual(expected['jobs'][1]['dependsOn'], ['visual-build'])
        self.assertNotIn('source', expected['jobs'][0])

    def test_complete_exact_receipt_and_independent_execution(self):
        values = self.fixture()
        self.assertEqual(validate_visual_receipt(values[0], 'project', 'a' * 40), values[0])
        validate_visual_execution(*values, 'agent')

    def test_missing_partial_foreign_or_untrusted_receipt_rejected(self):
        for patch in ({'completed': False}, {'completed': 1}, {'steps': VISUAL_STEPS[:-1]},
                      {'projectId': 'other'}, {'baseDefinitionCommit': 'c' * 40},
                      {'savedDefinitionCommit': 'a' * 40}, {'savedDefinitionCommit': 'main'},
                      {'pipelineId': '../other'}, {'extra': 'unreviewed'}):
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                validate_visual_receipt(self.receipt() | patch, 'project', 'a' * 40)

    def test_wrong_project_or_definition_origin_rejected(self):
        value = {'pipelineId': 'pipeline', 'projectId': 'project', 'repoUrl': 'git://127.0.0.1:19418/visual.git',
                 'defaultBranch': 'main', 'pipelineFilePath': 'pipeline.json'}
        validate_visual_pipeline(self.receipt(), value, value['repoUrl'])
        for key in value:
            with self.subTest(key=key), self.assertRaises(ValueError):
                validate_visual_pipeline(self.receipt(), value | {key: 'foreign'}, value['repoUrl'])

    def test_log_markers_require_exact_owned_step_and_complete_single_output(self):
        for kind in ('foreign-job', 'foreign-step', 'wrong-step', 'truncated', 'duplicate', 'combined-line'):
            receipt, run, jobs, logs = self.fixture()
            if kind == 'foreign-job': logs['entries'][0]['jobId'] = 'foreign'
            if kind == 'foreign-step': logs['entries'][0]['stepId'] = 'foreign'
            if kind == 'wrong-step': logs['entries'][0]['stepId'] = 'step-0-1'
            if kind == 'truncated': logs['truncated'] = True
            if kind == 'duplicate': logs['entries'].append(copy.deepcopy(logs['entries'][0]))
            if kind == 'combined-line': logs['entries'][0]['message'] = ' '.join(MARKER + '_' + s for s in ('EDITED', 'VERIFY', 'PACKAGE'))
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                validate_visual_execution(receipt, run, jobs, logs, 'agent')

    def test_log_chunks_reconstruct_stdout_in_step_position_order(self):
        receipt, run, jobs, logs = self.fixture()
        original = logs['entries'].pop(0)
        logs['entries'].extend([
            original | {'lineNumber': 3, 'message': 'EDITED\n'},
            original | {'lineNumber': 2, 'stream': 'stderr', 'message': 'synthetic diagnostic\n'},
            original | {'lineNumber': 1, 'message': MARKER + '_'},
        ])
        validate_visual_execution(receipt, run, jobs, logs, 'agent')

    def test_log_positions_streams_and_complete_exact_lines_required(self):
        patches = [
            {'stream': 'stderr'}, {'stream': 'unknown'}, {'stream': None},
            {'lineNumber': None}, {'lineNumber': True}, {'lineNumber': 0},
            {'lineNumber': -1}, {'lineNumber': 1.0}, {'lineNumber': '1'}, {'lineNumber': 2},
            {'message': MARKER + '_EDITED'},
            {'message': MARKER + '_EDITED\nunfinished'},
            {'message': ' ' + MARKER + '_EDITED\n'},
            {'message': MARKER + '_EDITED\r\n'},
            {'message': MARKER + '_EDITED\n' + MARKER + '_EDITED\n'},
            {'message': MARKER + '_EDITED\n' + MARKER + '_VERIFY\n'},
            {'message': '\ud800'},
        ]
        for patch in patches:
            receipt, run, jobs, logs = self.fixture()
            logs['entries'][0] |= patch
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                validate_visual_execution(receipt, run, jobs, logs, 'agent')
        for number in (1, 3):
            receipt, run, jobs, logs = self.fixture()
            first = logs['entries'][0]
            logs['entries'].append(first | {'lineNumber': number, 'message': 'diagnostic\n'})
            with self.subTest(number=number), self.assertRaises(ValueError):
                validate_visual_execution(receipt, run, jobs, logs, 'agent')

    def test_stdout_fragments_cannot_be_completed_by_stderr_or_another_step(self):
        for other_step in (False, True):
            receipt, run, jobs, logs = self.fixture()
            first, second = logs['entries'][:2]
            first['message'] = MARKER + '_'
            completion = (second if other_step else first) | {
                'lineNumber': 2, 'stream': 'stdout' if other_step else 'stderr', 'message': 'EDITED\n'}
            logs['entries'].append(completion)
            with self.subTest(other_step=other_step), self.assertRaises(ValueError):
                validate_visual_execution(receipt, run, jobs, logs, 'agent')

    def test_log_entry_and_utf8_byte_budgets_are_bounded(self):
        receipt, run, jobs, logs = self.fixture()
        first = logs['entries'][0]
        logs['entries'].extend(first | {'lineNumber': number, 'stream': 'stderr', 'message': ''}
                               for number in range(2, MAX_VISUAL_LOG_ENTRIES - 1))
        self.assertEqual(len(logs['entries']), MAX_VISUAL_LOG_ENTRIES)
        validate_visual_execution(receipt, run, jobs, logs, 'agent')
        logs['entries'].append(first | {'lineNumber': MAX_VISUAL_LOG_ENTRIES - 1, 'message': ''})
        with self.assertRaisesRegex(ValueError, 'complete persisted logs'):
            validate_visual_execution(receipt, run, jobs, logs, 'agent')

        receipt, run, jobs, logs = self.fixture()
        baseline_bytes = sum(len(entry['message'].encode('utf-8')) for entry in logs['entries'])
        padding_bytes = MAX_VISUAL_LOG_BYTES - baseline_bytes
        padding = 'é' * (padding_bytes // 2) + 'x' * (padding_bytes % 2)
        extra = logs['entries'][0] | {'lineNumber': 2, 'stream': 'stderr', 'message': padding}
        logs['entries'].append(extra)
        validate_visual_execution(receipt, run, jobs, logs, 'agent')
        extra['message'] += 'x'
        with self.assertRaisesRegex(ValueError, 'byte budget'):
            validate_visual_execution(receipt, run, jobs, logs, 'agent')

    def test_status_revision_run_and_deployment_mismatch_rejected(self):
        for patch in ({'status': 4}, {'runId': 'foreign'}, {'pipelineId': 'foreign'},
                      {'definitionCommitSha': 'a' * 40}, {'allowDeployments': True}):
            receipt, run, jobs, logs = self.fixture()
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                validate_visual_execution(receipt, run | patch, jobs, logs, 'agent')

    def test_missing_duplicate_foreign_failed_or_fabricated_checkout_job_rejected(self):
        for patch in ({'jobName': 'visual-package'}, {'agentId': 'other'}, {'pipelineRunId': 'other'},
                      {'pipelineId': 'other'}, {'status': 5}, {'jobRunId': 'job-1'},
                      {'sourceRepository': 'unexpected'}, {'sourceResolvedCommit': 'b' * 40},
                      {'sourceProvenanceState': 'reported'}, {'steps': []}):
            receipt, run, jobs, logs = self.fixture()
            jobs['list'][0] |= patch
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                validate_visual_execution(receipt, run, jobs, logs, 'agent')
        receipt, run, jobs, logs = self.fixture()
        jobs['list'].pop()
        with self.assertRaises(ValueError):
            validate_visual_execution(receipt, run, jobs, logs, 'agent')

    def test_foreign_or_failed_step_and_missing_logs_rejected(self):
        for patch in ({'stepRunId': 'job-0'}, {'jobRunId': 'foreign'}, {'name': 'other'},
                      {'status': 5}, {'exitCode': 1}):
            receipt, run, jobs, logs = self.fixture()
            jobs['list'][0]['steps'][0] |= patch
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                validate_visual_execution(receipt, run, jobs, logs, 'agent')
        for logs in ({}, {'entries': []}, {'entries': [{'message': MARKER + '_EDITED'}]}):
            receipt, run, jobs, _ = self.fixture()
            with self.subTest(logs=logs), self.assertRaises(ValueError):
                validate_visual_execution(receipt, run, jobs, logs, 'agent')

class VisualFixtureBindingTests(unittest.TestCase):
    def setUp(self):
        import json
        import subprocess
        import tempfile
        from pathlib import Path
        from types import SimpleNamespace
        from unittest import mock
        import run_actual_app as app
        from visual_git_fixture import prepare_visual_repository
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        evidence = self.root / 'evidence'
        (evidence / 'browser').mkdir(parents=True)
        self.fixture = app.Fixture(SimpleNamespace(visual_editor_acceptance=True, evidence_dir=evidence), self.root)
        self.fixture.env.update(GIT_AUTHOR_NAME='Synthetic fixture', GIT_AUTHOR_EMAIL='fixture@example.invalid',
                                GIT_COMMITTER_NAME='Synthetic fixture', GIT_COMMITTER_EMAIL='fixture@example.invalid')
        (self.root / 'repositories').mkdir()
        def run(_name, command, cwd=None):
            return subprocess.run(command, cwd=cwd or self.root, env=self.fixture.env,
                                  capture_output=True, text=True, check=True, timeout=10)
        self.run = run
        bare = prepare_visual_repository(self.root, run)
        original = run('head', ['git', 'rev-parse', 'HEAD'], bare).stdout.strip()
        source = self.root / 'visual-definition-source'
        (source / 'pipeline.json').write_text(json.dumps(saved_spec()))
        run('add', ['git', 'add', 'pipeline.json'], source)
        run('commit', ['git', 'commit', '-m', 'Synthetic visual graph edit'], source)
        run('push', ['git', 'push', str(bare), 'HEAD:refs/heads/main'], source)
        saved = run('head', ['git', 'rev-parse', 'HEAD'], source).stdout.strip()
        receipt, result, jobs, logs = VisualEvidenceTests().fixture()
        receipt |= {'baseDefinitionCommit': original, 'savedDefinitionCommit': saved}
        result['definitionCommitSha'] = saved
        self.fixture.visual_project_id = 'project'
        self.fixture.visual_definition_commit = original
        self.fixture.agent_id = 'agent'
        app.atomic_json(evidence / 'browser/summary.json', {'visualCI': receipt})
        self.values = {'/pipelines/pipeline': {'pipelineId': 'pipeline', 'projectId': 'project',
                                               'repoUrl': self.fixture.visual_repository_url(), 'defaultBranch': 'main',
                                               'pipelineFilePath': 'pipeline.json'},
                       f'/pipelines/pipeline/runs/run': result,
                       f'/pipelines/pipeline/runs/run/jobs': jobs,
                       f'/pipelines/pipeline/runs/run/logs?limit=1000': logs,
                       '/projects/project/artifacts?runId=run&pageSize=100': {'total': 0, 'list': []}}
        self.fixture.request = mock.Mock(side_effect=lambda path: self.values[path])
        self.fixture.require_observer_session = mock.Mock()
        self.fixture.expect_rejected = mock.Mock()

    def test_independent_git_definition_api_execution_and_receipt_are_bound(self):
        import json
        self.fixture.verify_visual_ci()
        self.assertTrue(self.fixture.visual_verified)
        self.fixture.require_observer_session.assert_called_once_with()
        self.fixture.expect_rejected.assert_called_once_with('/pipelines/pipeline/trigger', 403, 403, {}, 'POST')
        self.assertFalse((self.root / 'evidence/browser/visual-ci-verification.json').exists())
        self.fixture.cleanup(None)
        proof = json.loads((self.root / 'evidence/browser/visual-ci-verification.json').read_text())
        self.assertTrue(proof['completed'])
        self.assertEqual((proof['jobs'], proof['steps']), (2, 3))
        self.assertEqual(proof['sourceCheckout'], 'not-configured')

    def test_later_failure_removes_stale_visual_success_receipt(self):
        self.fixture.verify_visual_ci()
        path = self.root / 'evidence/browser/visual-ci-verification.json'
        path.write_text('{"completed":true}')
        self.fixture.cleanup('later independent acceptance check failed')
        self.assertFalse(path.exists())

    def test_changed_tip_fails_before_api_and_no_success_receipt(self):
        bare = self.root / 'repositories/visual.git'
        self.run('tamper', ['git', 'update-ref', 'refs/heads/main', self.fixture.visual_definition_commit], bare)
        with self.assertRaisesRegex(RuntimeError, 'unexpected repository'):
            self.fixture.verify_visual_ci()
        self.fixture.request.assert_not_called()
        self.assertFalse(self.fixture.visual_verified)
        self.assertFalse((self.root / 'evidence/browser/visual-ci-verification.json').exists())

    def test_disabled_or_preflight_browser_does_not_get_write_fixture_config(self):
        self.fixture.observer_id = 'observer'
        self.assertNotIn('ARCENTRA_ACCEPTANCE_VISUAL_CI', self.fixture.browser_environment('preflight'))
        self.assertNotIn('ARCENTRA_ACCEPTANCE_VISUAL_CI', self.fixture.browser_environment('storage-setup'))
        self.assertIn('ARCENTRA_ACCEPTANCE_VISUAL_CI', self.fixture.browser_environment('build'))
        self.fixture.args.visual_editor_acceptance = False
        self.assertNotIn('ARCENTRA_ACCEPTANCE_VISUAL_CI', self.fixture.browser_environment('build'))
