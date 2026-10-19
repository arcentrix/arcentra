import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
from copilot_history_journey import run_private_copilot
from test_copilot_history_evidence import fixture as evidence_fixture


class Running:
    returncode = 0
    def poll(self): return None


class Browser:
    returncode = 0
    def poll(self): return 0


class Model:
    def __init__(self, *args): self.closed = False
    def evidence(self): return dict(provider='local-tls-synthetic', requests=2, failures=0, requestDigests=['a'*64, 'b'*64], userPromptDigests=['c'*64, 'd'*64], rawPromptRetained=False)
    def close(self): self.closed = True


class Fixture:
    def __init__(self, root):
        self.root = root
        self.args = SimpleNamespace(browser_command=['node','tests/actual-backend-acceptance.mjs'], browser_cwd=root, evidence_dir=root/'evidence')
        self.build_verified = True
        self.failed_run = dict(projectId='project', pipelineId='pipeline', runId='run')
        self.secret_initial_value = 'synthetic-initial'
        self.secret_rotated_value = 'synthetic-rotated'
        self.control_secret_key = 'synthetic-aes'
        self.redactions = []
        self.calls = []
        self.bad = False
        self.owner = True
        (root/'control.toml').write_text('[server]\nport = 18080\n')
    def login(self): self.owner = True
    def login_observer(self): self.owner = False
    def request(self, path):
        assert self.owner
        _, receipt, detail, plan = evidence_fixture()
        if self.bad: detail['turns'][0]['text'] = 'wrong'
        if path == '/users/fetch': return dict(username='ci-admin', userId='actor')
        if path.endswith('/jobs'): return dict(total=1,list=[dict(steps=[dict(name='test',status=5,stepRunId='step')])])
        if '/copilot/conversations/' in path:
            return plan if '/plans/' in path else detail
        if '/artifacts?' in path or '/runs?' in path: return dict(list=[],total=0)
        raise AssertionError(path)
    def write_private(self, name, data): (self.root/name).write_text(data)
    def stop_application(self, process): self.calls.append('stop')
    def start_control(self, name): self.calls.append(name); return Running()
    def wait_until(self, name, predicate, *args, **kwargs): assert predicate(), name
    def probe(self, url): return 200
    def browser_environment(self, phase): assert phase == 'preflight'; return {}
    def start(self, name, command, cwd, env):
        assert command == ['node','tests/actual-copilot-history-acceptance.mjs']
        assert 'ARCENTRA_TEST_COPILOT_KEY' not in env and 'SSL_CERT_FILE' not in env
        contract = json.loads(env['ARCENTRA_COPILOT_HISTORY_CONTRACT'])
        _, receipt, _, _ = evidence_fixture()
        receipt.update(phase=contract['phase'],sendCount=2 if contract['phase']=='create' else 0)
        output = Path(env['UI_TEST_OUTPUT_DIR'])
        (output/'copilot-history-receipt.json').write_text(json.dumps(receipt))
        (output/'summary.json').write_text(json.dumps(dict(kind='actual-backend',status='passed',
            **{key:0 for key in ('pageErrorCount','secretExposureCount','consoleInspectionFailureCount',
                                'lateConsoleEventCount','unexpectedConsoleErrorCount','blockedRequestCount','unhandledRouteCount')})))
        return Browser()
    def expect_rejected(self, path, status, code):
        assert not self.owner and status == code == 404
        self.calls.append('foreign-denied')
    def note(self, message): pass


class HistoryJourneyTest(unittest.TestCase):
    def test_image_profile_rejects_host_only_model_fixture(self):
        import run_actual_app
        from run_image_app import ImageFixture
        with patch.object(run_actual_app.Fixture, '__init__', lambda fixture, *args: setattr(fixture, 'copilot_history_enabled', True)):
            with self.assertRaisesRegex(ValueError, 'host acceptance profile'):
                ImageFixture(None, None, None)

    def test_restart_exact_primary_and_no_key_in_browser(self):
        with tempfile.TemporaryDirectory() as root, patch('copilot_history_journey.SyntheticHistoryModel', Model):
            f = Fixture(Path(root))
            result = run_private_copilot(f, Running(), Running(), read_json=lambda p:json.loads(p.read_text()))
            self.assertIsInstance(result, Running)
            self.assertEqual(f.calls.count('foreign-denied'),4)
            self.assertIn('control-private-history-restart',f.calls)
            self.assertEqual(f.copilot_evidence['provider']['requests'],2)
            self.assertFalse(f.copilot_evidence['executionEnabled'])
            self.assertIsNone(f.copilot_model)
    def test_provider_bytes_must_match_consented_previews(self):
        class WrongPromptModel(Model):
            def evidence(self):
                return super().evidence() | dict(userPromptDigests=['e'*64, 'f'*64])
        with tempfile.TemporaryDirectory() as root, patch('copilot_history_journey.SyntheticHistoryModel', WrongPromptModel):
            f = Fixture(Path(root))
            with self.assertRaisesRegex(RuntimeError, 'approved previews'):
                run_private_copilot(f, Running(), Running(), read_json=lambda p:json.loads(p.read_text()))
            self.assertFalse(hasattr(f, 'copilot_evidence'))

    def test_primary_hash_failure_cannot_emit_success(self):
        with tempfile.TemporaryDirectory() as root, patch('copilot_history_journey.SyntheticHistoryModel', Model):
            f=Fixture(Path(root)); f.bad=True
            with self.assertRaises(RuntimeError):
                run_private_copilot(f,Running(),Running(),read_json=lambda p:json.loads(p.read_text()))
            self.assertFalse(hasattr(f,'copilot_evidence'))
            self.assertIsNotNone(f.copilot_model) # caller cleanup retains ownership

if __name__ == '__main__': unittest.main()
