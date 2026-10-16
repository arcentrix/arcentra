"""Pure fake subprocess fixtures: no Docker daemon, pulls or build execution."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import build_image_journey as build

SHA = 'a' * 40
ENV = {'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '2', 'GITHUB_SHA': SHA}


def metadata():
    return '\n'.join(['payload: go1.25.6'] + ['\tbuild\t' + item for item in
        ['GOOS=linux', 'GOARCH=amd64', 'CGO_ENABLED=1', '-trimpath=true',
         'vcs=git', 'vcs.revision=' + SHA, 'vcs.modified=false']])


class Fake:
    def __init__(self):
        self.calls, self.images = [], {}
        self.builder = None
        self.fail_build = False
        self.network_id = 'b' * 64
        self.bad_prefix = False
        self.bad_labels = False
        self.foreign_network = False
        self.bad_metadata = False

    def __call__(self, command, **kwargs):
        self.calls.append((command, kwargs))
        b = self.builder
        if command[:3] == ['git', 'rev-parse', 'HEAD']:
            return SHA
        if command[:2] == ['git', 'ls-tree']:
            return 'conf.d/config.toml\nconf.d/agent.toml\n'
        if command[:2] == ['git', 'show']:
            return '# tracked fixture\n'
        if command[1:3] == ['version', '-m']:
            return metadata().replace('vcs.modified=false', 'vcs.modified=true') if self.bad_metadata else metadata()
        if command[:3] == ['docker', 'network', 'inspect']:
            return json.dumps([{'Id': self.network_id, 'Name': b.project + '_default',
                'Driver': 'bridge', 'Scope': 'local', 'Ingress': False,
                'Labels': {'com.docker.compose.project': 'foreign' if self.foreign_network else b.project,
                           'com.docker.compose.network': 'default'}}])
        if command[:2] == ['docker', 'build']:
            intent = json.loads(b.ownership.read_text())
            assert intent['images'][-1]['state'] == 'pending'
            record = b.records[-1]
            role = record['role']
            number = {'control': 'c', 'agentBase': 'd', 'agent': 'e'}[role]
            labels = b.labels(role)
            if self.bad_labels:
                labels[build.LABEL + '.sha'] = 'foreign'
            self.images[record['tag']] = {'Id': 'sha256:' + number * 64, 'Os': 'linux',
                'Architecture': 'amd64', 'Config': {'Labels': labels,
                'Entrypoint': build.CONTROL_ENTRYPOINT if role == 'control' else build.AGENT_ENTRYPOINT,
                'Cmd': None, 'WorkingDir': '/var/lib/arcentra'},
                'RootFS': {'Layers': ['base'] + (['extra'] if role == 'agent' else [])}}
            if self.bad_prefix and role == 'agent':
                self.images[record['tag']]['RootFS']['Layers'][0] = 'unrelated'
            if self.fail_build:
                raise subprocess.TimeoutExpired(command, 1)
            return ''
        if command[:3] == ['docker', 'image', 'inspect']:
            image = next(v for k, v in self.images.items() if command[3] in (k, v['Id']))
            return json.dumps([image])
        if command[:3] == ['docker', 'image', 'ls']:
            if '--filter' in command:
                return '\n'.join(v['Id'] for v in self.images.values())
            return self.images.get(command[-1], {}).get('Id', '')
        if command[:3] == ['docker', 'image', 'rm']:
            self.images = {k: v for k, v in self.images.items() if v['Id'] != command[-1]}
            return command[-1]
        raise AssertionError(command)


class BuilderTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.fake = Fake()
        self.builder = build.Builder(ENV, self.root / 'ownership.json', self.root / 'manifest.json', self.fake)
        self.fake.builder = self.builder
        self.payload = self.root / 'payload'
        self.payload.mkdir()
        for name in build.BINARIES:
            (self.payload / name).write_bytes(b'fixture')
        self.which = patch('build_image_journey.shutil.which', return_value='/opt/go/bin/go')
        self.which.start()
        self.addCleanup(self.which.stop)

    def test_success_exact_manifest_and_defaults(self):
        self.builder.build(self.payload)
        manifest = build.validate_manifest(json.loads(self.builder.manifest.read_text()))
        self.assertEqual(manifest['networkId'], 'b' * 64)
        self.assertEqual(self.builder.manifest.stat().st_mode & 0o777, 0o600)
        commands = [c for c, _ in self.fake.calls if c[:2] == ['docker', 'build']]
        self.assertEqual(len(commands), 3)
        self.assertIn('arcentra', commands[0])
        self.assertIn('arcentra-agent', commands[1])
        self.assertIn('AGENT_BASE=arcentra-image-123-2-agentbase:local', commands[2])
        self.assertTrue(all(c[-1] == str(self.builder.context) for c in commands))
        self.assertFalse(any('push' in c or '--network=host' in c for c in commands))
        context = sorted(str(p.relative_to(self.builder.context)) for p in self.builder.context.rglob('*') if p.is_file())
        self.assertEqual(context, ['.dockerignore', 'Dockerfile.ci', 'dist/linux/amd64/arcentra', 'dist/linux/amd64/arcentra-agent',
            'dist/linux/amd64/arcentra-cli', 'dist/linux/amd64/conf.d/agent.toml',
            'dist/linux/amd64/conf.d/config.toml', 'image-agent.Dockerfile'])

    def test_no_ambient_credentials_or_docker_endpoint(self):
        self.builder.build(self.payload)
        for command, kwargs in self.fake.calls:
            self.assertEqual(set(kwargs['env']), {'PATH', 'HOME', 'LANG', 'DOCKER_BUILDKIT'})
            self.assertLessEqual(kwargs['timeout'], 600)

    def test_bad_metadata_stops_before_context_or_mutation(self):
        self.fake.bad_metadata = True
        with self.assertRaises(ValueError):
            self.builder.build(self.payload)
        self.assertFalse(self.builder.context.exists())
        self.assertFalse(any(c[:2] == ['docker', 'build'] for c, _ in self.fake.calls))

    def test_symlink_payload_rejected(self):
        (self.payload / 'arcentra').unlink()
        (self.payload / 'arcentra').symlink_to(self.payload / 'arcentra-agent')
        with self.assertRaises(ValueError):
            self.builder.build(self.payload)

    def test_foreign_network_no_mutation(self):
        self.fake.foreign_network = True
        with self.assertRaises(ValueError):
            self.builder.build(self.payload)
        self.assertFalse(self.builder.ownership.exists())

    def test_bad_layer_prefix_no_manifest(self):
        self.fake.bad_prefix = True
        with self.assertRaises(ValueError):
            self.builder.build(self.payload)
        self.assertFalse(self.builder.manifest.exists())
        self.assertTrue(self.builder.blocked)

    def test_bad_image_labels_no_manifest(self):
        self.fake.bad_labels = True
        with self.assertRaises(ValueError):
            self.builder.build(self.payload)
        self.assertFalse(self.builder.manifest.exists())

    def test_interrupted_build_records_intent_and_stops_admission(self):
        self.fake.fail_build = True
        with self.assertRaises(RuntimeError):
            self.builder.build(self.payload)
        self.assertEqual(len(self.builder.records), 1)
        self.assertEqual(self.builder.records[0]['state'], 'pending')
        with self.assertRaises(RuntimeError):
            self.builder.command(['docker', 'info'])
        with self.assertRaisesRegex(RuntimeError, 'daemon build completion'):
            self.builder.cleanup()
        self.assertFalse(self.builder.manifest.exists())
        self.assertFalse(self.builder.context.exists())
        # Retrying cleanup must not erase its uncertainty.
        with self.assertRaisesRegex(RuntimeError, 'daemon build completion'):
            self.builder.cleanup()

    def test_independent_cleanup_owned_ids_only(self):
        self.builder.build(self.payload)
        fresh = build.Builder(ENV, self.builder.ownership, self.builder.manifest, self.fake)
        fresh.load()
        fresh.cleanup()
        self.assertFalse(self.fake.images)
        self.assertFalse(self.builder.context.exists())
        removes = [c for c, _ in self.fake.calls if c[:3] == ['docker', 'image', 'rm']]
        self.assertEqual([c[-1] for c in removes], ['sha256:' + x * 64 for x in 'edc'])
        self.assertTrue(all(len(c) == 4 for c in removes))
        self.assertFalse(any('prune' in c for c, _ in self.fake.calls))

    def test_cleanup_refuses_foreign_labels(self):
        self.builder.build(self.payload)
        for image in self.fake.images.values():
            image['Config']['Labels'][build.LABEL + '.run'] = 'other'
        with self.assertRaisesRegex(RuntimeError, 'cleanup unverified'):
            self.builder.cleanup()
        self.assertEqual(len(self.fake.images), 3)

    def test_cleanup_rejects_foreign_run_file(self):
        self.builder.build(self.payload)
        foreign = build.Builder(dict(ENV, GITHUB_RUN_ID='999'), self.builder.ownership, self.builder.manifest, self.fake)
        with self.assertRaises(ValueError):
            foreign.load()

    def test_cleanup_rejects_mutated_id(self):
        self.builder.build(self.payload)
        self.builder.records[0]['id'] = 'sha256:' + '9' * 64
        with self.assertRaises(RuntimeError):
            self.builder.cleanup()
        self.assertIn('arcentra-image-123-2-control:local', self.fake.images)

    def test_context_symlink_not_removed(self):
        self.builder.build(self.payload)
        import shutil
        shutil.rmtree(self.builder.context)
        self.builder.context.symlink_to(self.payload, target_is_directory=True)
        with self.assertRaisesRegex(RuntimeError, 'context cleanup'):
            self.builder.cleanup()
        self.assertTrue((self.payload / 'arcentra').exists())

    def test_json_duplicate_keys_rejected(self):
        self.builder.ownership.write_text('{"runId": "1", "runId": "2"}')
        self.builder.ownership.chmod(0o600)
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            build.read_json(self.builder.ownership)

    def test_deadline_prevents_new_commands(self):
        self.builder.deadline = 0
        with self.assertRaises(RuntimeError):
            self.builder.build(self.payload)
        self.assertFalse(self.fake.calls)

    def test_dockerfile_preserves_application_defaults(self):
        source = Path(build.__file__).with_name('image-agent.Dockerfile').read_text()
        for key in ('goSourceDigest', 'pythonSourceDigest'):
            self.assertIn(build.TOOLCHAIN[key], source)
        self.assertIn('COPY --from=go-tools /usr/local/go /usr/local/go', source)
        self.assertIn('COPY --from=python-tools /usr/local/ /usr/local/', source)
        for line in source.splitlines():
            self.assertFalse(line.startswith(('ENTRYPOINT ', 'CMD ', 'WORKDIR ', 'VOLUME ', 'ONBUILD ')))
        self.assertIn('dpkg-query', source)
        self.assertIn('NOT hermetic', source)


class BoundaryTests(unittest.TestCase):
    def test_guard_requires_own_opt_in_and_local_nonroot_runner(self):
        env = dict(ENV, ARCENTRA_IMAGE_JOURNEY='1', ARCENTRA_REAL_STACK='1', GITHUB_ACTIONS='true',
                   RUNNER_ENVIRONMENT='github-hosted', RUNNER_OS='Linux', RUNNER_ARCH='X64')
        with patch.object(build.platform, 'machine', return_value='x86_64'), \
                patch.object(build.os, 'getuid', return_value=1001), patch.object(build.os, 'getgid', return_value=1001):
            build.guard(env)
            for key, value in [('ARCENTRA_IMAGE_JOURNEY', ''), ('DOCKER_HOST', 'tcp://remote'),
                               ('GITHUB_RUN_ATTEMPT', '0'), ('GITHUB_SHA', 'short')]:
                with self.assertRaises(ValueError):
                    build.guard(dict(env, **{key: value}))

    def test_bounded_process_kills_and_reaps_group_on_excess_output(self):
        from unittest.mock import MagicMock
        process = MagicMock()
        process.pid = 12345
        selector = MagicMock()
        selector.__enter__.return_value = selector
        selector.get_map.return_value = {1: 'pipe'}
        selector.select.return_value = [(MagicMock(fileobj=process.stdout), 1)]
        with patch.object(build.subprocess, 'Popen', return_value=process) as popen, \
                patch.object(build.selectors, 'DefaultSelector', return_value=selector), \
                patch.object(build.os, 'read', return_value=b'x' * 9), \
                patch.object(build.os, 'killpg') as kill, patch.object(build, 'MAX_OUTPUT', 8):
            with self.assertRaisesRegex(RuntimeError, 'output limit'):
                build.bounded(['docker', 'build'], cwd='.', env={}, timeout=1)
            self.assertTrue(popen.call_args.kwargs['start_new_session'])
            kill.assert_called_once_with(12345, build.signal.SIGKILL)
            process.wait.assert_called_once_with(timeout=10)
            process.stdout.close.assert_called_once()

    def test_bounded_process_kills_group_on_timeout(self):
        from unittest.mock import MagicMock
        process = MagicMock()
        process.pid = 12345
        selector = MagicMock()
        selector.__enter__.return_value = selector
        selector.get_map.return_value = {1: 'pipe'}
        with patch.object(build.subprocess, 'Popen', return_value=process), \
                patch.object(build.selectors, 'DefaultSelector', return_value=selector), \
                patch.object(build.time, 'monotonic', side_effect=[0, 10]), \
                patch.object(build.os, 'killpg') as kill:
            with self.assertRaisesRegex(RuntimeError, 'deadline'):
                build.bounded(['docker', 'build'], cwd='.', env={}, timeout=1)
            kill.assert_called_once_with(12345, build.signal.SIGKILL)
            process.wait.assert_called_once_with(timeout=10)


if __name__ == '__main__':
    unittest.main()
