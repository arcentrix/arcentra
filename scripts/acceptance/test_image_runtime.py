"""Pure command/response fixture tests; never invoke Docker or privileged operations."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import image_runtime as ir
import run_image_app as app
import run_actual_app as actual


def manifest():
    sha = 'a' * 40
    return {'schemaVersion': 1, 'runId': '123', 'attempt': '2', 'sourceSha': sha,
            'composeProject': 'arcentra-image-123-2', 'networkId': 'b' * 64,
            'images': {role: {'id': 'sha256:' + char * 64, 'sourceSha': sha}
                       for role, char in [('control', 'c'), ('agent', 'd'), ('agentBase', 'e')]},
            'toolchain': dict(ir.TOOLCHAIN)}


class FakeDocker:
    def __init__(self):
        self.calls, self.containers = [], {}
        self.runtime = None
        self.fail_create = False
        self.timeout_create = False

    def __call__(self, command, **kwargs):
        self.calls.append((command, kwargs))
        args = command[1:]
        output = ''
        if args[:2] == ['container', 'create']:
            record = self.runtime.records[-1]
            intent = json.loads(self.runtime.intent_path.read_text())
            assert intent['containers'][-1]['name'] == record['name']
            assert intent['containers'][-1]['id'] is None
            identifier = str(len(self.containers) + 1).zfill(64)
            self.containers[record['name']] = {'Id': identifier, 'Name': '/' + record['name'],
                'Image': record['image'], 'Config': {'Labels': self.runtime.labels(record['role'])},
                'State': {'Status': 'created', 'Running': False, 'ExitCode': 0}}
            if self.timeout_create:
                raise subprocess.TimeoutExpired(command, 20)
            if self.fail_create:
                return SimpleNamespace(returncode=1, stdout='', stderr='private error')
            output = identifier + '\n'
        elif args[:2] == ['container', 'inspect']:
            match = next(v for k, v in self.containers.items() if args[2] in (k, v['Id']))
            output = json.dumps([match])
        elif args[:2] in (['container', 'start'], ['container', 'stop']):
            identifier = args[-1]
            match = next(v for v in self.containers.values() if v['Id'] == identifier)
            running = args[1] == 'start'
            match['State'] = {'Status': 'running' if running else 'exited', 'Running': running, 'ExitCode': 0}
        elif args[:2] == ['container', 'ls']:
            name = args[-1].removeprefix('name=^/').removesuffix('$')
            if name in self.containers:
                output = self.containers[name]['Id'] + '\n'
        elif args[:2] == ['container', 'rm']:
            self.containers = {k: v for k, v in self.containers.items() if v['Id'] != args[2]}
        elif args[:2] == ['container', 'logs']:
            output = 'fixture log\n'
        else:
            raise AssertionError(command)
        return SimpleNamespace(returncode=0, stdout=output, stderr='')


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.fake = FakeDocker()
        with patch('image_runtime.os.getuid', return_value=1001), patch('image_runtime.os.getgid', return_value=1001):
            self.runtime = ir.ImageRuntime(manifest(), self.root / 'ownership.json', self.root, self.fake)
        self.fake.runtime = self.runtime
        for role in ('control', 'agent', 'cli'):
            for kind in ('config', 'state'):
                (self.root / f'image-{role}-{kind}').mkdir(mode=0o700)
        (self.root / 'repositories').mkdir(mode=0o700)

    def create(self, role='control'):
        return self.runtime.create(role, config=self.root / f'image-{role}-config', state=self.root / f'image-{role}-state')

    def test_bounded_reader_kills_group_even_when_parent_already_exited(self):
        from unittest.mock import Mock
        process = Mock(pid=12345)
        process.poll.return_value = 0
        selector = Mock()
        selector.__enter__ = Mock(return_value=selector)
        selector.__exit__ = Mock(return_value=False)
        selector.get_map.return_value = {'still-open-child-pipe': True}
        with patch('image_runtime.subprocess.Popen', return_value=process), \
             patch('image_runtime.selectors.DefaultSelector', return_value=selector), \
             patch('image_runtime.time.monotonic', side_effect=[0, 2]), \
             patch('image_runtime.os.killpg') as kill:
            with self.assertRaises(subprocess.TimeoutExpired):
                ir.bounded_run(['docker'], env={}, capture_output=True, text=True, timeout=1, check=False)
        kill.assert_called_once_with(12345, ir.signal.SIGKILL)
        process.wait.assert_called_once_with(timeout=2)

    def test_manifest_exact_ids_and_fields(self):
        self.assertEqual(ir.validate_manifest(manifest()), manifest())
        for mutation in [lambda m: m.update(extra=True), lambda m: m['images']['agent'].update(id='agent:latest'),
                         lambda m: m.update(composeProject='other'), lambda m: m['toolchain'].update(pythonVersion='3.11'),
                         lambda m: m['images']['control'].update(sourceSha='f' * 40)]:
            value = manifest(); mutation(value)
            with self.assertRaises(ValueError):
                ir.validate_manifest(value)

    def test_default_entrypoint_and_bounds(self):
        handle = self.create()
        command = self.fake.calls[0][0]
        self.assertEqual(command[-1], manifest()['images']['control']['id'])
        for forbidden in ('--entrypoint', '--privileged', '--device', '--pid', '--ipc'):
            self.assertNotIn(forbidden, command)
        for required in ('--read-only', '--cap-drop', 'ALL', 'no-new-privileges:true', '--memory', '512m',
                         '--cpus', '1', '--pids-limit', '128', '127.0.0.1:18080:18080', '--pull', 'never'):
            self.assertIn(required, command)
        self.assertIsNone(handle.poll())
        self.assertEqual(self.fake.calls[0][1]['env'], {'PATH': '/usr/bin:/bin', 'HOME': str(self.root), 'LANG': 'C.UTF-8'})
        self.assertEqual(self.runtime.intent_path.stat().st_mode & 0o777, 0o600)

    def test_single_file_local_log_explicitly_disables_compression(self):
        self.runtime.create('toolchain')
        command = self.fake.calls[0][0]
        options = [command[i + 1] for i, item in enumerate(command) if item == '--log-opt']
        self.assertEqual(options, ['max-size=8m', 'max-file=1', 'compress=false'])
        self.assertEqual(command[command.index('--log-driver') + 1], 'local')
        contract = ir.toolchain_contract()
        self.assertEqual(contract['logMaxSize'], '8m')
        self.assertEqual(contract['logMaxFile'], '1')
        self.assertIs(contract['logCompress'], False)

    def test_agent_mount_excludes_ca_private_and_browser(self):
        self.create('agent')
        command = self.fake.calls[0][0]
        mounts = [command[i+1] for i, x in enumerate(command) if x == '--mount']
        self.assertEqual(len(mounts), 2)
        self.assertTrue(mounts[0].endswith('dst=/conf.d,readonly'))
        self.assertIn('image-agent-state', mounts[1])
        self.assertNotIn('ca.key', str(command))
        self.assertNotIn('coordination', str(command))
        self.assertNotIn('/var/run/docker.sock', str(command))

    def test_role_mount_rejection(self):
        with self.assertRaises(ValueError):
            self.runtime.create('agent', config=self.root, state=self.root / 'image-agent-state')
        with self.assertRaises(ValueError):
            self.runtime.create('control', arguments=['-help'])
        self.assertEqual(self.fake.calls, [])

    def test_cli_only_bounded_override(self):
        handle = self.runtime.create('cli', config=self.root / 'image-cli-config', state=self.root / 'image-cli-state', arguments=['migrate'])
        self.assertIn('/usr/local/bin/arcentra-cli', self.fake.calls[0][0])
        self.runtime.remove(handle.record)
        with self.assertRaises(ValueError):
            self.runtime.create('cli', config=self.root / 'image-cli-config', state=self.root / 'image-cli-state', arguments=['shell'])

    def test_restart_removes_original_and_reuses_only_state(self):
        first = self.create('agent')
        with self.assertRaises(RuntimeError):
            self.create('agent')
        self.runtime.remove(first.record)
        second = self.create('agent')
        self.assertNotEqual(first.record['name'], second.record['name'])
        creates = [c for c, _ in self.fake.calls if c[1:3] == ['container', 'create']]
        self.assertEqual([x for x in creates[0] if 'type=bind' in x], [x for x in creates[1] if 'type=bind' in x])

    def test_timeout_create_intent_cleanup_independent(self):
        self.fake.timeout_create = True
        with self.assertRaises(RuntimeError):
            self.create()
        self.assertTrue(self.runtime.blocked)
        self.assertIsNone(self.runtime.records[0]['id'])
        with self.assertRaises(RuntimeError):
            self.runtime.create('git', repositories=self.root / 'repositories')
        self.runtime.cleanup()
        self.assertFalse(self.fake.containers)
        self.assertTrue(all(item['removed'] for item in self.runtime.records))
        self.assertFalse(any('prune' in c for c, _ in self.fake.calls))

    def test_unknown_actual_state_stops_admission(self):
        handle = self.create()
        self.fake.containers[handle.record['name']]['State'] = {'Status': 'restarting', 'Running': True}
        with self.assertRaises(RuntimeError):
            handle.poll()
        self.assertTrue(self.runtime.blocked)

    def test_wrong_ownership_never_removed(self):
        handle = self.create()
        self.fake.containers[handle.record['name']]['Config']['Labels'][ir.LABEL + '.run'] = '999'
        with self.assertRaises(RuntimeError):
            self.runtime.cleanup()
        self.assertFalse(any(c[1:3] == ['container', 'rm'] for c, _ in self.fake.calls))

    def test_absent_partial_create_is_clean(self):
        record = {'name': 'arcentra-image-123-2-control-0', 'id': None, 'role': 'control',
                  'image': manifest()['images']['control']['id'], 'removed': False,
                  'createState': 'not-submitted', 'removeState': 'not-submitted'}
        self.runtime.records = [record]
        self.runtime.cleanup()
        self.assertTrue(record['removed'])

    def test_deadline_stops_new_calls(self):
        self.runtime.deadline = 0
        with self.assertRaises(RuntimeError):
            self.create()
        self.assertEqual(self.fake.calls, [])

    def test_unresolved_create_empty_lookup_never_clean(self):
        self.fake.timeout_create = True
        with self.assertRaises(RuntimeError): self.create()
        self.fake.containers.clear()  # Simulate daemon operation still pending, not settled absence.
        with self.assertRaises(RuntimeError): self.runtime.cleanup()
        self.assertFalse(self.runtime.records[0]['removed'])
        self.assertEqual(self.runtime.records[0]['createState'], 'uncertain')
        saved = json.loads(self.runtime.intent_path.read_text())
        self.assertEqual(saved['containers'][0]['createState'], 'uncertain')

    def test_successful_rm_requires_observed_absence(self):
        handle = self.create()
        real_execute = self.runtime.execute
        def ineffective_rm(command, **kwargs):
            if command[1:3] == ['container', 'rm']:
                return SimpleNamespace(returncode=0, stdout='', stderr='')
            return real_execute(command, **kwargs)
        self.runtime.execute = ineffective_rm
        with self.assertRaises(RuntimeError): self.runtime.remove(handle.record)
        self.assertFalse(handle.record['removed'])
        self.assertTrue(self.runtime.blocked)
        with self.assertRaises(RuntimeError): self.create()

    def test_remaining_budget_clamps_command(self):
        self.runtime.deadline = 101
        with patch('image_runtime.time.monotonic', return_value=100):
            self.runtime.command(['container', 'ls', '--filter', 'name=^/absent$'], timeout=20)
        self.assertEqual(self.fake.calls[-1][1]['timeout'], 1)
        with self.assertRaises(RuntimeError):
            self.runtime.command(['container', 'ls'], cleanup=True)

    def test_oversized_output_stops_admission(self):
        self.runtime.execute = lambda *a, **k: SimpleNamespace(returncode=0, stdout='x' * (ir.MAX_DOCKER_OUTPUT + 1), stderr='')
        with self.assertRaises(RuntimeError): self.runtime.command(['container', 'ls'])
        self.assertTrue(self.runtime.blocked)

    def test_static_failure_diagnostic_survives_independent_cleanup(self):
        handle = self.runtime.create('toolchain')
        sentinel = 'PRIVATE_TOKEN_SENTINEL_/secret/path'
        execute = self.runtime.execute
        self.runtime.execute = lambda *a, **k: SimpleNamespace(returncode=128, stdout=sentinel,
                                                               stderr='OCI runtime create failed: permission denied ' + sentinel)
        with self.assertRaises(RuntimeError) as caught:
            self.runtime.command(['container', 'start', handle.record['id']])
        expected = {'operation': 'container-start', 'phase': 'command', 'role': 'toolchain',
                    'returnCode': 128, 'reason': 'permission-denied'}
        self.assertEqual(self.runtime.failure, expected)
        self.assertNotIn(sentinel, str(caught.exception))
        self.assertNotIn(sentinel, self.runtime.intent_path.read_text())
        self.runtime.cleanup_deadline = ir.time.monotonic() + 60
        with self.assertRaises(RuntimeError):
            self.runtime.command(['container', 'logs', handle.record['id']], cleanup=True, phase='capture')
        self.assertEqual(self.runtime.failure, expected)
        self.runtime.execute = execute
        self.runtime.failure = None  # Model fresh independent cleanup process.
        with patch.object(app, 'ImageRuntime', return_value=self.runtime):
            app.cleanup_only(SimpleNamespace(ownership_file=self.runtime.intent_path), manifest())
        summary = json.loads((self.root / 'image-runtime-summary.json').read_text())
        self.assertEqual(summary['firstFailure'], expected)
        self.assertEqual(summary['toolchainStartDiagnostic']['stateErrorStatus'], 'unavailable')
        self.assertIn('permission', summary['toolchainStartDiagnostic']['stderr']['head'])
        self.assertEqual(summary['cleanupStatus'], 'confirmed')
        self.assertNotIn(sentinel, json.dumps(summary))

    def test_unknown_failure_is_generic_and_fail_closed(self):
        sentinel = 'PRIVATE_UNKNOWN_SENTINEL'
        self.runtime.execute = lambda *a, **k: SimpleNamespace(returncode=1, stdout='', stderr=sentinel)
        with self.assertRaises(RuntimeError) as caught:
            self.runtime.command(['container', 'inspect', 'unreported-path'], phase='wait')
        self.assertEqual(self.runtime.failure['reason'], 'unknown')
        self.assertEqual(self.runtime.failure['phase'], 'wait')
        self.assertNotIn(sentinel, str(caught.exception))
        self.assertNotIn('unreported-path', str(caught.exception))
        self.assertTrue(self.runtime.blocked)
        with self.assertRaises(RuntimeError): self.runtime.create('toolchain')

    def test_failure_enums_reject_arbitrary_input(self):
        valid = {'operation': 'container-start', 'phase': 'command', 'role': 'toolchain',
                 'returnCode': 128, 'reason': 'unknown'}
        for key in ('operation', 'phase', 'role', 'reason', 'returnCode'):
            for bad in ('PRIVATE_SENTINEL', ['unknown'], {}):
                value = valid | {key: bad}
                with self.assertRaises(ValueError): ir.validate_failure(value)
        with self.assertRaises(ValueError): ir.validate_failure(valid | {'extra': 'secret'})
        with self.assertRaises(ValueError): ir.validate_failure(valid | {'returnCode': True})
        self.assertEqual(ir.failure_reason('some private diagnostics'), 'unknown')
        self.assertEqual(ir.failure_reason('read-only file system /private'), 'readonly-filesystem')

    def test_projection_redacts_credentials_addresses_paths_and_keeps_tail(self):
        sentinel = 'PRIVATE_SECRET_SENTINEL'
        raw = ('prefix ' * 2000 + ' failed to mount tmpfs invalid option uid=1001 ' +
               sentinel + ' Bearer eyJ.private.signature https://private.invalid/token /private/config ' +
               '127.0.0.1:1234 password=secret 87654321')
        value = ir.safe_error_projection(raw)
        serialized = json.dumps(value)
        for secret in (sentinel, 'eyJ.private.signature', 'private.invalid', '/private/config', '127.0.0.1', '87654321', 'uid=1001'):
            self.assertNotIn(secret, serialized)
        self.assertIn('tmpfs', value['tail'])
        self.assertIn('invalid', value['tail'])
        self.assertTrue(value['truncated'])
        self.assertTrue(value['features']['uid'])
        self.assertEqual(ir.validate_projection(value), value)
        with self.assertRaises(ValueError): ir.validate_projection(value | {'tail': [sentinel]})
        with self.assertRaises(ValueError): ir.validate_projection(value | {'features': {'arbitrary': True}})

    def test_owned_state_error_projection_does_not_block_cleanup(self):
        handle = self.runtime.create('toolchain')
        execute = self.runtime.execute
        self.runtime.execute = lambda *a, **k: SimpleNamespace(returncode=1, stdout='', stderr='failed to start')
        with self.assertRaises(RuntimeError): self.runtime.command(['container', 'start', handle.record['id']])
        self.runtime.execute = execute
        self.fake.containers[handle.record['name']]['State']['Error'] = 'invalid tmpfs option uid=1001 /private/SENTINEL'
        self.runtime.cleanup()
        self.assertTrue(handle.record['removed'])
        self.assertEqual(self.runtime.start_diagnostic['stateErrorStatus'], 'available')
        self.assertIn('tmpfs', self.runtime.start_diagnostic['stateError']['head'])
        self.assertNotIn('SENTINEL', json.dumps(self.runtime.start_diagnostic))

    def test_detailed_diagnostic_forbidden_for_application_roles(self):
        handle = self.create('control')
        self.runtime.execute = lambda *a, **k: SimpleNamespace(returncode=1, stdout='', stderr='PRIVATE_SECRET')
        with self.assertRaises(RuntimeError): self.runtime.command(['container', 'start', handle.record['id']])
        self.assertIsNone(self.runtime.start_diagnostic)
        diagnostic = {'stdout': ir.safe_error_projection(''), 'stderr': ir.safe_error_projection(''),
                      'stateErrorStatus': 'pending', 'stateError': None}
        with self.assertRaises(ValueError): ir.validate_start_diagnostic(diagnostic, self.runtime.failure)
        self.assertEqual(ir.toolchain_contract()['tmpfs']['mode'], '0700')
        self.assertEqual(ir.toolchain_contract()['bindMounts'], [])

    def test_pure_input_inspection(self):
        def inspect(kind, identifier, cleanup=False):
            if kind == 'network':
                return {'Id': manifest()['networkId'], 'Name': manifest()['composeProject'] + '_default',
                        'Driver': 'bridge', 'Scope': 'local', 'Ingress': False,
                        'Labels': {'com.docker.compose.project': manifest()['composeProject'], 'com.docker.compose.network': 'default'}}
            role = next(k for k,v in manifest()['images'].items() if v['id'] == identifier)
            return {'Id': identifier, 'Os': 'linux', 'Architecture': 'amd64',
                    'Config': {'Entrypoint': ir.CONTROL_ENTRYPOINT if role == 'control' else ir.AGENT_ENTRYPOINT,
                               'Cmd': None, 'WorkingDir': '/var/lib/arcentra', 'Labels': self.runtime.labels(role)},
                    'RootFS': {'Layers': ['base', 'tools'] if role == 'agent' else ['base']}}
        with patch.object(self.runtime, 'inspect_json', side_effect=inspect), patch.object(Path, 'read_text', return_value='MemAvailable: 4194304 kB\n'):
            self.runtime.verify_inputs()
        def bad(kind, identifier, cleanup=False):
            value = inspect(kind, identifier)
            if kind == 'image': value['Config']['Entrypoint'] = ['/bin/sh']
            return value
        with patch.object(self.runtime, 'inspect_json', side_effect=bad):
            with self.assertRaises(ValueError): self.runtime.verify_inputs()


class FixtureTests(unittest.TestCase):
    def test_shared_exercise_and_assertions(self):
        for name in ('exercise', 'run_snapshot', 'verify_downloads', 'fix_repository_after_failed_run', 'run_build_browser'):
            self.assertIs(getattr(app.ImageFixture, name), getattr(actual.Fixture, name))

    def test_cleanup_failure_revokes_success_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence = root / 'evidence'
            fixture = app.ImageFixture(SimpleNamespace(evidence_dir=evidence), root,
                                       SimpleNamespace(manifest=manifest(), intent_path=root / 'ownership.json', records=[]))
            receipt = evidence / 'browser/project-build-verification.json'
            receipt.parent.mkdir(parents=True)
            receipt.write_text('{"status":"passed"}')
            with patch.object(fixture, 'cleanup_runtime', side_effect=RuntimeError('cleanup blocked')):
                with self.assertRaises(RuntimeError): fixture.cleanup(None)
            self.assertFalse(receipt.exists())
            result = json.loads((evidence / 'actual-app-result.json').read_text())
            self.assertEqual(result['status'], 'failed')
            self.assertEqual(result['runtime']['cleanup'], 'failed')
            summary = json.loads((root / 'image-runtime-summary.json').read_text())
            self.assertEqual(summary['cleanupStatus'], 'unknown')
            self.assertNotIn('error', summary)
            self.assertNotIn(str(root), json.dumps(summary))
            self.assertEqual(summary['images']['agent']['kind'], 'test-toolchain-derivative')

    def test_normal_log_capture_never_bypasses_deadline(self):
        from unittest.mock import Mock
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runtime = Mock()
            runtime.command.return_value = 'log'
            fixture = app.ImageFixture(SimpleNamespace(evidence_dir=root / 'evidence'), root, runtime)
            fixture.capture_log({'name': 'owned', 'id': 'c' * 64, 'removed': False})
            self.assertIs(runtime.inspect_owned.call_args.kwargs['cleanup'], False)
            self.assertIs(runtime.command.call_args.kwargs['cleanup'], False)
            for _, handle in fixture.logs: handle.close()

    def test_private_config_routes_and_tls(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = SimpleNamespace(evidence_dir=root / 'evidence')
            fixture = app.ImageFixture(args, root, SimpleNamespace(manifest=manifest()))
            for name in ('server.pem', 'server.key', 'ca.pem'):
                (root / name).write_text(name)
            fixture.write_control_configuration('jwt-synthetic')
            fixture.write_agent_configuration('synthetic-token')
            control = (fixture.config('control') / 'config.toml').read_text()
            agent = (fixture.config('agent') / 'agent.toml').read_text()
            cli = (fixture.config('cli') / 'config.toml').read_text()
            self.assertIn('mysql:3306', control)
            self.assertIn('serverAddr = "control:19090"', agent)
            self.assertIn('serverName = "arcentra-acceptance.local"', agent)
            self.assertEqual({p.name for p in fixture.config('agent').iterdir()}, {'agent.toml', 'ca.pem'})
            self.assertNotIn('jwt-synthetic', cli)
            self.assertNotIn('server.key', cli)
            fixture.write_agent_configuration('')
            self.assertIn('registrationToken = ""', (fixture.config('agent') / 'agent.toml').read_text())
            env = fixture.browser_environment('build')
            self.assertEqual(env['ARCENTRA_ACCEPTANCE_PROFILE'], app.PROFILE)
            self.assertEqual(env['ARCENTRA_ACCEPTANCE_REPO_URL'], app.REPOSITORY)
            self.assertEqual(env['ARCENTRA_ACCEPTANCE_BACKEND_URL'], actual.BACKEND_URL)
            self.assertEqual(fixture.identity_path(), root / 'image-agent-state/identity/identity.json')


if __name__ == '__main__':
    unittest.main()
