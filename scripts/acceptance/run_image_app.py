#!/usr/bin/env python3
"""Run the shared actual build journey using exact local application image IDs."""
import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import sys
import tempfile
import time

import run_actual_app as actual
from image_runtime import ImageRuntime, validate_manifest, fullmatch, JOURNEY_SECONDS

PROFILE = 'image-build-v1'
REPOSITORY = 'git://git:9418/acceptance.git'


def guard(manifest, env):
    actual.validate_environment(env)
    if (env.get('ARCENTRA_IMAGE_JOURNEY') != '1' or env.get('RUNNER_OS') != 'Linux'
            or env.get('RUNNER_ARCH') != 'X64' or platform.machine() != 'x86_64'):
        raise ValueError('requires explicit disposable Linux X64 image journey opt-in')
    if any(env.get(k) for k in ('DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH')):
        raise ValueError('remote/custom Docker endpoints forbidden')
    if any(manifest[k] != env.get(v) for k, v in
           (('runId', 'GITHUB_RUN_ID'), ('attempt', 'GITHUB_RUN_ATTEMPT'), ('sourceSha', 'GITHUB_SHA'))):
        raise ValueError('manifest is not owned by this CI run/attempt/checkout')


def write_runtime_summary(runtime, cleanup_status):
    """Explicit public allowlist; never include arbitrary errors, paths or config."""
    if cleanup_status not in ('confirmed', 'unknown'):
        raise ValueError('invalid cleanup summary status')
    manifest = runtime.manifest
    actual.atomic_json(runtime.intent_path.parent / 'image-runtime-summary.json', {
        'schemaVersion': 1, 'profile': PROFILE, 'sourceSha': manifest['sourceSha'],
        'runId': manifest['runId'], 'attempt': manifest['attempt'],
        'images': {role: {'id': value['id'], 'kind': 'test-toolchain-derivative' if role == 'agent' else 'stock'}
                   for role, value in manifest['images'].items()},
        'containers': [{'role': record['role'], 'id': record['id'],
                        'removal': 'confirmed' if record['removed'] else 'unknown'} for record in runtime.records],
        'cleanupStatus': cleanup_status,
    })


class ImageFixture(actual.Fixture):
    """Override transport only; keep the complete shared build assertions."""
    def __init__(self, args, root, runtime):
        super().__init__(args, root)
        self.runtime = runtime
        for role in ('control', 'agent', 'cli'):
            for kind in ('config', 'state'):
                (root / f'image-{role}-{kind}').mkdir(mode=0o700)
        for name in ('home', 'tmp', 'go-cache'):
            (self.state('agent') / name).mkdir(mode=0o700)
        self.runtime_verified = False

    def config(self, role):
        return self.root / f'image-{role}-config'

    def state(self, role):
        return self.root / f'image-{role}-state'

    def verify_runtime_toolchain(self):
        self.runtime.verify_inputs()
        handle = self.runtime.create('toolchain')
        if handle.wait(30) != 0:
            raise RuntimeError('test-only Agent derivative does not contain the exact pinned toolchain')
        self.capture_log(handle.record)
        self.runtime.remove(handle.record)
        self.runtime_verified = True
        self.note('Pinned Go/Python verified inside the test-only Agent derivative; application entrypoints unchanged')

    def write_control_configuration(self, jwt_secret):
        content = actual.control_config(Path('/var/lib/arcentra'), jwt_secret)
        content = content.replace('host = "127.0.0.1"', 'host = "0.0.0.0"')
        for old, new in (('127.0.0.1:19092', 'kafka:9092'), ('127.0.0.1:13306', 'mysql:3306'),
                         ('127.0.0.1:16379', 'redis:6379'), ('/var/lib/arcentra/server.pem', '/conf.d/server.pem'),
                         ('/var/lib/arcentra/server.key', '/conf.d/server.key')):
            content = content.replace(old, new)
        plugins = '[builtins.shell]\nshell = "/bin/sh"\ntimeout = 120\n[plugins.git]\ngitPath = "git"\ntimeout = 60\nshallow = false\n'
        self.write_private('image-control-config/config.toml', content)
        self.write_private('image-control-config/plugins.toml', plugins)
        for name in ('server.pem', 'server.key'):
            self.write_private('image-control-config/' + name, (self.root / name).read_text())
        # CLI needs only database configuration, not JWT or TLS signing material.
        self.write_private('image-cli-config/config.toml', '''[database]
driver = "mysql"
[database.mysql]
dsn = "root:ci-only-mysql-password@tcp(mysql:3306)/arcentra_acceptance?charset=utf8mb4&parseTime=True&loc=UTC"
[database.options]
output = false
maxOpenConns = 5
maxIdleConns = 2
[log]
output = "stdout"
level = "INFO"
''')

    def write_agent_configuration(self, registration_token):
        content = actual.agent_config(Path('/var/lib/arcentra'), registration_token)
        for old, new in (('host = "127.0.0.1"', 'host = "0.0.0.0"'),
                         ('127.0.0.1:19092', 'kafka:9092'), ('127.0.0.1:19090', 'control:19090'),
                         ('/var/lib/arcentra/ca.pem', '/conf.d/ca.pem')):
            content = content.replace(old, new)
        self.write_private('image-agent-config/agent.toml', content)
        self.write_private('image-agent-config/ca.pem', (self.root / 'ca.pem').read_text())

    def run_cli(self, name, arguments):
        handle = self.runtime.create('cli', config=self.config('cli'), state=self.state('cli'), arguments=arguments)
        if handle.wait(120) != 0:
            raise RuntimeError('real embedded CLI action failed')
        self.capture_log(handle.record)
        self.runtime.remove(handle.record)

    def start_control(self, name):
        handle = self.runtime.create('control', config=self.config('control'), state=self.state('control'))
        self.wait_until('image Control readiness', lambda: self.probe(actual.BACKEND_URL + '/ready') == 200, handle)
        return handle

    def start_agent(self, name):
        return self.runtime.create('agent', config=self.config('agent'), state=self.state('agent'))

    def stop_application(self, handle):
        self.capture_log(handle.record)
        self.runtime.remove(handle.record)

    def identity_path(self):
        return self.state('agent') / 'identity/identity.json'

    def repository_url(self):
        return REPOSITORY

    def start_git_server(self, repositories):
        handle = self.runtime.create('git', repositories=repositories)
        def reachable():
            result = subprocess.run(['git', 'ls-remote', actual.REPO_URL, 'refs/heads/main'],
                                    env=self.env, capture_output=True, text=True, timeout=5)
            return result.returncode == 0 and self.commit in result.stdout
        self.wait_until('image Git source via loopback probe', reachable, handle, timeout=20)

    def storage_configuration(self):
        return super().storage_configuration() | {'endpoint': 'minio:9000'}

    def browser_environment(self, phase):
        return super().browser_environment(phase) | {'ARCENTRA_ACCEPTANCE_PROFILE': PROFILE}

    def runtime_evidence(self):
        return {'profile': PROFILE, 'images': self.runtime.manifest['images'],
                'toolchainVerifiedInAgentImage': self.runtime_verified,
                'applicationEntrypoints': 'unchanged-image-defaults',
                'jobExecution': 'trusted-baremetal-inside-agent-test-container'}

    def runtime_limitations(self):
        return ['Trusted baremetal job commands execute inside the test-only Agent container.',
                'The stock Agent image does not ship the fixture compiler/Python toolchain.',
                'This does not establish containerd job isolation or arbitrary OCI build support.',
                'The manifest is an unsigned integrity record, not a supply-chain attestation.',
                'Organization creation is not implemented; the fixture uses a synthetic orgId.']

    def cleanup(self, error):
        try:
            super().cleanup(error)
            write_runtime_summary(self.runtime, 'confirmed')
        except BaseException:
            # A browser success alone must never survive failed runtime cleanup.
            directory = self.args.evidence_dir
            directory.mkdir(parents=True, exist_ok=True)
            (directory / 'browser/project-build-verification.json').unlink(missing_ok=True)
            actual.atomic_json(directory / 'actual-app-result.json', {
                'status': 'failed', 'error': 'image runtime cleanup or evidence finalization failed',
                'contract': actual.BUILD_CONTRACT, 'runtime': {'profile': PROFILE, 'cleanup': 'failed'},
            })
            write_runtime_summary(self.runtime, 'unknown')
            raise

    def capture_log(self, record, cleanup=False):
        if record.get('removed'):
            return
        self.runtime.inspect_owned(record, cleanup=cleanup)
        value = self.runtime.command(['container', 'logs', '--tail', '10000', record['id'] or record['name']], cleanup=cleanup)
        path = self.write_private(record['name'] + '.log', value)
        handle = path.open('ab')
        self.logs.append((path, handle))

    def cleanup_runtime(self):
        self.runtime.cleanup_deadline = time.monotonic() + 180
        errors = []
        try:
            for record in self.runtime.records:
                if not record.get('removed'):
                    try:
                        self.capture_log(record, cleanup=True)
                    except Exception:
                        errors.append('container diagnostic capture failed')
            self.runtime.cleanup()
        finally:
            super().cleanup_runtime()
        if errors:
            raise RuntimeError('; '.join(errors))


def cleanup_only(args, manifest):
    # Independent invocation needs no browser, toolchain or application readiness.
    if not args.ownership_file.exists():
        from types import SimpleNamespace
        write_runtime_summary(SimpleNamespace(manifest=manifest, intent_path=args.ownership_file, records=[]), 'confirmed')
        return 0
    value = json.loads(actual.read_private_bytes(args.ownership_file, 65536), object_pairs_hook=actual.no_duplicate_keys)
    if (set(value) != {'schemaVersion', 'manifest', 'root', 'containers'} or value['schemaVersion'] != 1
            or value['manifest'] != manifest or not isinstance(value['containers'], list)):
        raise ValueError('cleanup ownership manifest mismatch')
    root = Path(value['root'])
    runtime = ImageRuntime(manifest, args.ownership_file, root)
    for i, record in enumerate(value['containers']):
        if (not isinstance(record, dict) or set(record) != {'name', 'id', 'role', 'image', 'removed', 'createState', 'removeState'}
                or record['role'] not in ('control', 'agent', 'cli', 'git', 'toolchain')
                or record['name'] != f"arcentra-image-{manifest['runId']}-{manifest['attempt']}-{record['role']}-{i}"
                or record['image'] != manifest['images']['agent' if record['role'] in ('agent', 'toolchain') else 'control']['id']
                or (record['id'] is not None and not fullmatch(r'[a-f0-9]{64}', record['id']))
                or record['createState'] not in ('not-submitted', 'pending', 'uncertain', 'confirmed')
                or record['removeState'] not in ('not-submitted', 'pending', 'uncertain', 'confirmed')
                or type(record['removed']) is not bool):
            raise ValueError('invalid owned container record')
    runtime.records = value['containers']
    try:
        runtime.cleanup()
    except BaseException:
        write_runtime_summary(runtime, 'unknown')
        raise
    write_runtime_summary(runtime, 'confirmed')
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image-manifest', type=Path, required=True)
    parser.add_argument('--ownership-file', type=Path, required=True)
    parser.add_argument('--cleanup-only', action='store_true')
    parser.add_argument('--browser-cwd', type=Path)
    parser.add_argument('--evidence-dir', type=Path)
    parser.add_argument('browser_command', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    manifest = validate_manifest(actual.read_private_json(args.image_manifest))
    guard(manifest, os.environ)
    args.ownership_file = args.ownership_file.resolve()
    os.umask(0o077)
    if args.cleanup_only:
        return cleanup_only(args, manifest)
    if args.ownership_file.exists():
        parser.error('ownership file already exists; run independent cleanup first')
    if args.browser_command[:1] == ['--']:
        args.browser_command = args.browser_command[1:]
    if not args.browser_command or not args.browser_cwd or not args.evidence_dir:
        parser.error('browser cwd, evidence directory and real browser command are required')
    for name in ('git', 'openssl', 'docker'):
        if not shutil.which(name):
            parser.error(name + ' must be preinstalled from official runner sources')
    args.browser_cwd = args.browser_cwd.resolve(strict=True)
    args.evidence_dir = args.evidence_dir.resolve()
    args.ownership_file.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='arcentra-image-app-') as directory:
        root = Path(directory)
        runtime = ImageRuntime(manifest, args.ownership_file, root)
        fixture = ImageFixture(args, root, runtime)
        error = None
        def interrupted(signum, _frame):
            runtime.blocked = True
            raise RuntimeError(f'image acceptance interrupted by signal {signum}')
        signal.signal(signal.SIGTERM, interrupted)
        signal.signal(signal.SIGINT, interrupted)
        signal.signal(signal.SIGALRM, interrupted)
        signal.setitimer(signal.ITIMER_REAL, JOURNEY_SECONDS)
        try:
            fixture.exercise()
        except (Exception, KeyboardInterrupt) as exc:
            error = str(exc) or 'image acceptance interrupted'
            print('[image-app] FAILED: ' + error, file=sys.stderr)
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGTERM, signal.SIG_IGN)
            signal.signal(signal.SIGINT, signal.SIG_IGN)
            try:
                fixture.cleanup(error)
            except Exception as exc:
                error = 'cleanup incomplete: ' + str(exc)
                print('[image-app] FAILED: ' + error, file=sys.stderr)
        return 1 if error else 0


if __name__ == '__main__':
    sys.exit(main())
