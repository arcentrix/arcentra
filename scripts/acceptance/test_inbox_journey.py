import copy
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from inbox_evidence import READ_STEPS, RESUME_STEPS
from inbox_journey import require_browser_summary, run_personal_inbox


class Handle:
    returncode = 0
    def poll(self):
        return 0


class Running(Handle):
    def poll(self):
        return None


class Fixture:
    def __init__(self, root):
        self.args = SimpleNamespace(browser_command=['node', 'tests/actual-backend-acceptance.mjs'],
                                    browser_cwd=root, evidence_dir=root)
        self.build_verified = True
        self.failed_run = dict(projectId='p', pipelineId='pipe', runId='failed')
        self.successful_run = dict(projectId='p', pipelineId='pipe', runId='success')
        self.calls = []
        self.receipt = {'readEntry': dict(id='success', kind='run_result', status='success', occurredAt='2026-10-18T00:00:00Z', deliveredAt='2026-10-18T00:00:01Z', readAt=None, target=dict(type='run', projectId='p', pipelineId='pipe', runId='success'), availability='available'),
                        'unreadEntry': dict(id='failed', kind='run_result', status='failed', occurredAt='2026-10-18T00:00:00Z', deliveredAt='2026-10-18T00:00:01Z', readAt=None, target=dict(type='run', projectId='p', pipelineId='pipe', runId='failed'), availability='available')}
        self.bad_resume = False
        self.bad_browser = False
    def login(self):
        self.calls.append('login')
    def request(self, path):
        if path == '/users/fetch':
            return dict(userId='actor', username='ci-admin')
        self.calls.append('primary-snapshot')
        return dict(list=[self.receipt['readEntry'], self.receipt['unreadEntry']], nextCursor=None, scanIncomplete=False)
    def stop_application(self, control):
        self.calls.append('stop-control')
    def start_control(self, name):
        self.calls.append('restart-control')
        return Running()
    def wait_until(self, name, predicate, *args, **kwargs):
        if not predicate():
            raise RuntimeError('not ready')
    def probe(self, url):
        return 200
    def browser_environment(self, phase):
        assert phase == 'preflight'
        return {}
    def start(self, name, command, cwd, env):
        import json
        contract = json.loads(env['ARCENTRA_INBOX_CONTRACT'])
        phase = contract['phase']
        self.calls.append('browser-' + phase)
        def row(run, status, read):
            return dict(id=run, kind='run_result', status=status, occurredAt='2026-10-18T00:00:00Z',
                        deliveredAt='2026-10-18T00:00:01Z', readAt='2026-10-18T00:00:02Z' if read else None,
                        target=dict(type='run', projectId='p', pipelineId='pipe', runId=run), availability='available')
        self.receipt = dict(journey='personal-inbox-v1', phase=phase, completed=True, userId='actor',
                            steps=READ_STEPS if phase == 'read' else RESUME_STEPS,
                            readEntry=row('success', 'success', True), unreadEntry=row('failed', 'failed', False))
        if phase == 'resume' and self.bad_resume:
            self.receipt['readEntry']['id'] = 'recreated'
        return Handle()
    def require_observer_session(self):
        self.calls.append('observer')
    def expect_rejected(self, *args):
        assert args[1:] == (404, 404, {}, 'POST')
        self.calls.append('foreign-denied')
    def note(self, note):
        pass
    def read(self, path):
        if path.name == 'inbox-receipt.json':
            return copy.deepcopy(self.receipt)
        return dict(kind='actual-backend', status='passed', pageErrorCount=int(self.bad_browser), secretExposureCount=0,
                    consoleInspectionFailureCount=0, lateConsoleEventCount=0, unexpectedConsoleErrorCount=0,
                    blockedRequestCount=0, unhandledRouteCount=0)


class InboxJourneyTests(unittest.TestCase):
    def test_exact_restart_order_and_foreign_denial(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Fixture(Path(directory))
            run_personal_inbox(fixture, Running(), Running(), read_json=fixture.read)
            self.assertEqual(fixture.calls, ['login', 'primary-snapshot', 'browser-read', 'login', 'primary-snapshot', 'observer',
                             'foreign-denied', 'foreign-denied', 'login', 'stop-control', 'restart-control',
                             'browser-resume', 'login', 'primary-snapshot'])
            self.assertEqual(len(fixture.inbox_evidence['receiptDigests']), 2)
            self.assertFalse(fixture.inbox_evidence['approvalBrowserFlowVerified'])
    def test_restart_changed_identity_and_browser_errors_never_publish_evidence(self):
        for option in ('bad_resume', 'bad_browser'):
            with self.subTest(option=option), tempfile.TemporaryDirectory() as directory:
                fixture = Fixture(Path(directory))
                setattr(fixture, option, True)
                with self.assertRaises((RuntimeError, ValueError)):
                    run_personal_inbox(fixture, Running(), Running(), read_json=fixture.read)
                self.assertFalse(hasattr(fixture, 'inbox_evidence'))
    def test_unverified_build_and_unreviewed_command_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Fixture(Path(directory))
            fixture.build_verified = False
            with self.assertRaises(RuntimeError):
                run_personal_inbox(fixture, Running(), Running(), read_json=fixture.read)
            fixture.build_verified = True
            fixture.args.browser_command = ['sh', '-c', 'node anything']
            with self.assertRaises(RuntimeError):
                run_personal_inbox(fixture, Running(), Running(), read_json=fixture.read)
            self.assertEqual(fixture.calls, [])
    def test_missing_safety_summary_never_passes(self):
        with self.assertRaises(RuntimeError):
            require_browser_summary(dict(kind='actual-backend', status='passed'))


if __name__ == '__main__':
    unittest.main()
