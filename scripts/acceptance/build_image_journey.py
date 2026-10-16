#!/usr/bin/env python3
"""Build local owned stock images and a test-only Agent derivative; never push.

CLI output is deliberately generic. The manifest is an unsigned integrity record.
A lost build response can leave daemon-side work/cache: cleanup reports that
uncertainty, and never prunes global cache or removes official source images.
"""
import argparse
import json
import os
from pathlib import Path
import platform
import selectors
import shutil
import signal
import subprocess
import sys
import time

from image_runtime import (AGENT_ENTRYPOINT, CONTROL_ENTRYPOINT, LABEL, OWNER,
                           TOOLCHAIN, fullmatch, private_json, validate_manifest)
from run_install_image_smoke import validate_build_metadata

ROOT = Path(__file__).resolve().parents[2]
BINARIES = ('arcentra', 'arcentra-agent', 'arcentra-cli')
MAX_OUTPUT = 4 * 1024 * 1024
MAX_PAYLOAD = 512 * 1024 * 1024


def bounded(command, *, cwd, env, timeout):
    """Drain bounded pipes; kill/reap the complete client process group on failure."""
    process = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, start_new_session=True)
    output = bytearray()
    deadline = time.monotonic() + timeout
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise RuntimeError('subprocess deadline exceeded')
                for key, _ in selector.select(min(remaining, 0.25)):
                    chunk = os.read(key.fileobj.fileno(), 65536)
                    if not chunk:
                        selector.unregister(key.fileobj)
                    else:
                        output.extend(chunk)
                        if len(output) > MAX_OUTPUT:
                            raise RuntimeError('subprocess output limit exceeded')
        code = process.wait(timeout=max(0.01, deadline - time.monotonic()))
        if code:
            raise RuntimeError('subprocess failed')
        return output.decode('utf-8', errors='strict')
    except BaseException:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=10)
        raise
    finally:
        process.stdout.close()


def guard(env):
    required = {'ARCENTRA_IMAGE_JOURNEY': '1', 'ARCENTRA_REAL_STACK': '1',
                'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted',
                'RUNNER_OS': 'Linux', 'RUNNER_ARCH': 'X64'}
    if any(env.get(k) != v for k, v in required.items()):
        raise ValueError('requires explicit disposable GitHub-hosted image journey opt-in')
    if platform.machine() != 'x86_64' or os.getuid() == 0 or os.getgid() == 0:
        raise ValueError('requires non-root Linux amd64 runner')
    if not fullmatch(r'[a-f0-9]{40}', env.get('GITHUB_SHA')):
        raise ValueError('requires exact full source SHA')
    if any(env.get(k) for k in ('DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH')):
        raise ValueError('custom Docker endpoint/settings forbidden')
    if not all(fullmatch(r'[1-9][0-9]{0,19}', env.get(k)) for k in ('GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT')):
        raise ValueError('invalid run identity')


def read_json(path):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o077 or path.stat().st_size > 65536:
        raise ValueError('requires private bounded regular ownership file')
    def pairs(items):
        result = {}
        for k, v in items:
            if k in result:
                raise ValueError('duplicate JSON key')
            result[k] = v
        return result
    return json.loads(path.read_text(), object_pairs_hook=pairs)


class Builder:
    def __init__(self, env, ownership, manifest, execute=None):
        self.run, self.attempt, self.sha = (env[k] for k in ('GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT', 'GITHUB_SHA'))
        self.project = f'arcentra-image-{self.run}-{self.attempt}'
        self.ownership, self.manifest = Path(ownership), Path(manifest)
        if self.ownership.absolute() == self.manifest.absolute():
            raise ValueError('manifest and ownership must be distinct files')
        self.context = self.ownership.parent / (self.project + '-build-context')
        self.execute = execute or bounded
        self.deadline, self.blocked = time.monotonic() + 600, False
        self.records = []
        self.context_owned = False
        self.env = {'PATH': '/usr/bin:/bin', 'HOME': str(self.context / 'client-home'), 'LANG': 'C.UTF-8',
                    'DOCKER_BUILDKIT': '1'}

    def labels(self, role):
        return {LABEL: 'v1', LABEL + '.owner': OWNER, LABEL + '.run': self.run,
                LABEL + '.attempt': self.attempt, LABEL + '.sha': self.sha, LABEL + '.role': role}

    def command(self, args, cleanup=False):
        if self.blocked and not cleanup:
            raise RuntimeError('new build admission stopped')
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            self.blocked = True
            raise RuntimeError('build/cleanup deadline exceeded')
        try:
            return self.execute(args, cwd=ROOT, env=self.env,
                                timeout=min(remaining, 600 if args[:2] == ['docker', 'build'] else 30))
        except BaseException:
            self.blocked = True
            raise RuntimeError('operation failed or uncertain; admission stopped') from None

    def save(self):
        private_json(self.ownership, {'schemaVersion': 1, 'runId': self.run, 'attempt': self.attempt,
                     'sourceSha': self.sha, 'context': str(self.context), 'contextOwned': self.context_owned,
                     'images': self.records})

    def inspect(self, kind, identifier, cleanup=False):
        value = json.loads(self.command(['docker', kind, 'inspect', identifier], cleanup))
        if not isinstance(value, list) or len(value) != 1 or not isinstance(value[0], dict):
            raise ValueError('invalid Docker inspection')
        return value[0]

    def network(self):
        value = self.inspect('network', self.project + '_default')
        if (not fullmatch(r'[a-f0-9]{64}', value.get('Id')) or value.get('Name') != self.project + '_default'
                or value.get('Driver') != 'bridge' or value.get('Scope') != 'local' or value.get('Ingress')
                or value.get('Labels', {}).get('com.docker.compose.project') != self.project
                or value.get('Labels', {}).get('com.docker.compose.network') != 'default'):
            raise ValueError('requires existing exact run-owned Compose default bridge')
        return value['Id']

    def prepare(self, payload):
        if self.ownership.exists() or self.manifest.exists() or self.context.exists() or self.context.is_symlink():
            raise ValueError('fresh ownership, manifest and context paths required')
        if self.command(['git', 'rev-parse', 'HEAD']).strip() != self.sha:
            raise ValueError('checkout does not match source SHA')
        payload = Path(payload)
        if payload.is_symlink() or payload.resolve() != payload.absolute():
            raise ValueError('symlink payload path forbidden')
        go = shutil.which('go')
        if not go:
            raise ValueError('official runner Go tool required to inspect payload')
        for name in BINARIES:
            path = payload / name
            if path.is_symlink() or not path.is_file() or path.stat().st_size > MAX_PAYLOAD:
                raise ValueError('requires bounded regular application payload')
            validate_build_metadata(self.command([go, 'version', '-m', str(path)]), self.sha)
        self.save()  # Persist intent before creating context or calling build.
        self.context.mkdir(mode=0o700)
        self.context_owned = True
        self.save()
        (self.context / 'client-home').mkdir(mode=0o700)
        # Docker/buildx may write client metadata under HOME. Never send it.
        (self.context / '.dockerignore').write_text('**\n!Dockerfile.ci\n!image-agent.Dockerfile\n!dist/\n!dist/**\n')
        dist = self.context / 'dist/linux/amd64'
        dist.mkdir(parents=True, mode=0o700)
        for name in BINARIES:
            shutil.copyfile(payload / name, dist / name)
            if (dist / name).stat().st_size > MAX_PAYLOAD:
                raise ValueError('copied payload exceeds size bound')
            (dist / name).chmod(0o755)
            # Verify the copied payload, not just the potentially changing source.
            validate_build_metadata(self.command([go, 'version', '-m', str(dist / name)]), self.sha)
        tracked = self.command(['git', 'ls-tree', '-r', '--name-only', self.sha, '--', 'conf.d']).splitlines()
        if not tracked or len(tracked) > 64:
            raise ValueError('invalid tracked configuration set')
        for name in tracked:
            if not fullmatch(r'conf\.d/[a-zA-Z0-9_.-]+', name):
                raise ValueError('unexpected configuration path')
            destination = dist / name
            destination.parent.mkdir(exist_ok=True)
            destination.write_text(self.command(['git', 'show', self.sha + ':' + name]))
        for name, source in [('Dockerfile.ci', 'Dockerfile.ci'),
                             ('image-agent.Dockerfile', 'scripts/acceptance/image-agent.Dockerfile')]:
            (self.context / name).write_text(self.command(['git', 'show', self.sha + ':' + source]))

    def verify_image(self, info, record):
        config = info.get('Config', {})
        if (not fullmatch(r'sha256:[a-f0-9]{64}', info.get('Id'))
                or (record['id'] and info['Id'] != record['id'])
                or info.get('Os') != 'linux' or info.get('Architecture') != 'amd64'
                or config.get('Entrypoint') != (CONTROL_ENTRYPOINT if record['role'] == 'control' else AGENT_ENTRYPOINT)
                or config.get('Cmd') not in (None, []) or config.get('WorkingDir') != '/var/lib/arcentra'
                or config.get('Volumes') or config.get('OnBuild')
                or any(config.get('Labels', {}).get(k) != v for k, v in self.labels(record['role']).items())):
            raise ValueError('image identity/ownership/packaging contract mismatch')
        return info

    def build_one(self, role, base=None):
        tag = self.project + '-' + role.lower() + ':local'
        # Refuse overwriting an existing tag, even one claiming our labels.
        if self.command(['docker', 'image', 'ls', '--no-trunc', '--quiet', tag]).strip():
            raise ValueError('owned build tag already exists')
        record = {'role': role, 'tag': tag, 'id': None, 'state': 'pending'}
        self.records.append(record)
        self.save()
        args = ['docker', 'build', '--platform', 'linux/amd64', '--network', 'default',
                '--progress', 'plain', '--tag', tag]
        for k, v in self.labels(role).items():
            args += ['--label', k + '=' + v]
        if role == 'agent':
            self.verify_image(self.inspect('image', base['tag']), base)
            args += ['--build-arg', 'AGENT_BASE=' + base['tag'], '-f', str(self.context / 'image-agent.Dockerfile')]
        else:
            args += ['--target', 'arcentra' if role == 'control' else 'arcentra-agent',
                     '--build-arg', 'TARGETARCH=amd64', '-f', str(self.context / 'Dockerfile.ci')]
        self.command(args + [str(self.context)])
        info = self.verify_image(self.inspect('image', tag), record)
        record['id'], record['state'] = info['Id'], 'built'
        self.save()
        return info

    def build(self, payload):
        try:
            network = self.network()
            self.prepare(payload)
            self.build_one('control')
            base = self.build_one('agentBase')
            derived = self.build_one('agent', self.records[1])
            layers = base.get('RootFS', {}).get('Layers', [])
            derivative = derived.get('RootFS', {}).get('Layers', [])
            if not layers or len(derivative) <= len(layers) or derivative[:len(layers)] != layers:
                raise ValueError('derivative must retain exact stock Agent rootfs prefix')
            if self.network() != network:
                raise ValueError('network identity changed during build')
            value = validate_manifest({'schemaVersion': 1, 'runId': self.run, 'attempt': self.attempt,
                'sourceSha': self.sha, 'composeProject': self.project, 'networkId': network,
                'images': {r['role']: {'id': r['id'], 'sourceSha': self.sha} for r in self.records},
                'toolchain': dict(TOOLCHAIN)})
            private_json(self.manifest, value)
        except BaseException:
            self.blocked = True
            raise

    def load(self):
        data = read_json(self.ownership)
        expected = {'schemaVersion', 'runId', 'attempt', 'sourceSha', 'context', 'contextOwned', 'images'}
        if (not isinstance(data, dict) or set(data) != expected
                or type(data['schemaVersion']) is not int or data['schemaVersion'] != 1 or data['runId'] != self.run
                or data['attempt'] != self.attempt or data['sourceSha'] != self.sha
                or data['context'] != str(self.context) or type(data['contextOwned']) is not bool
                or not isinstance(data['images'], list) or len(data['images']) > 3):
            raise ValueError('ownership file identity mismatch')
        for role, record in zip(('control', 'agentBase', 'agent'), data['images']):
            if (not isinstance(record, dict) or set(record) != {'role', 'tag', 'id', 'state'} or record['role'] != role
                    or record['tag'] != self.project + '-' + role.lower() + ':local'
                    or record['state'] not in ('pending', 'built', 'removed')
                    or (record['id'] is not None and not fullmatch(r'sha256:[a-f0-9]{64}', record['id']))
                    or (record['state'] == 'built' and record['id'] is None)):
                raise ValueError('invalid owned image record')
        self.records, self.context_owned = data['images'], data['contextOwned']

    def cleanup(self):
        self.blocked, self.deadline = True, time.monotonic() + 120
        failures = []
        for record in reversed(self.records):
            if record['state'] == 'removed':
                continue
            uncertain = record['state'] == 'pending'
            if uncertain:
                failures.append('daemon build completion/cache cannot be established')
            try:
                ids = self.command(['docker', 'image', 'ls', '--no-trunc', '--quiet', record['tag']], True).splitlines()
                if ids:
                    if len(ids) != 1 or not fullmatch(r'sha256:[a-f0-9]{64}', ids[0]):
                        raise ValueError('uncertain image lookup')
                    info = self.verify_image(self.inspect('image', ids[0], True), record)
                    # Never force removal; live containers and foreign aliases must block it.
                    self.command(['docker', 'image', 'rm', info['Id']], True)
                    if self.command(['docker', 'image', 'ls', '--no-trunc', '--quiet', record['tag']], True).strip():
                        raise ValueError('image removal not verified')
                elif record['id']:
                    # Tag absence alone does not prove immutable image absence.
                    if self.command(['docker', 'image', 'ls', '--no-trunc', '--quiet',
                                     '--filter', 'label=' + LABEL + '.run=' + self.run,
                                     '--filter', 'label=' + LABEL + '.attempt=' + self.attempt], True).splitlines().count(record['id']):
                        raise ValueError('owned image persists without expected tag')
                if not uncertain:
                    record['state'] = 'removed'
                self.save()
            except Exception:
                failures.append(record['role'] + ' image cleanup unverified')
        if not self.context_owned and (self.context.exists() or self.context.is_symlink()):
            failures.append('context creation completion uncertain')
        if self.context_owned:
            try:
                if self.context.is_symlink() or self.context.resolve() != self.context.absolute():
                    raise ValueError('context path changed')
                if self.context.exists():
                    shutil.rmtree(self.context)
                self.context_owned = False
                self.save()
            except Exception:
                failures.append('owned context cleanup unverified')
        if failures:
            raise RuntimeError('; '.join(failures))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--payload-dir', type=Path, default=ROOT / 'dist/linux/amd64')
    parser.add_argument('--manifest', '--image-manifest', type=Path, required=True)
    parser.add_argument('--ownership-file', type=Path, required=True)
    parser.add_argument('--cleanup-only', action='store_true')
    parser.add_argument('--evidence-dir', type=Path)
    args = parser.parse_args()
    guard(os.environ)
    os.umask(0o077)
    for path in (args.manifest, args.ownership_file):
        if path.is_symlink() or path.absolute() != path.resolve():
            raise ValueError('symlink state paths forbidden')
        if not path.parent.is_dir():
            raise ValueError('state parent must already exist')
    builder = Builder(os.environ, args.ownership_file.absolute(), args.manifest.absolute())
    evidence = args.evidence_dir or args.manifest.parent / 'image-build-evidence'
    if evidence.is_symlink() or evidence.absolute() != evidence.resolve():
        raise ValueError('symlink evidence directory forbidden')
    evidence.mkdir(mode=0o700, exist_ok=True)
    succeeded = False
    try:
        if args.cleanup_only:
            if args.ownership_file.exists():
                builder.load()
                builder.cleanup()
            elif builder.context.exists() or builder.context.is_symlink():
                raise RuntimeError('context exists without ownership record')
        else:
            def interrupted(_signum, _frame):
                builder.blocked = True
                raise RuntimeError('build interrupted')
            signal.signal(signal.SIGTERM, interrupted)
            signal.signal(signal.SIGINT, interrupted)
            builder.build(args.payload_dir.absolute())
        succeeded = True
    finally:
        # No raw subprocess output, host paths, configuration, or credentials.
        receipt = {'schemaVersion': 1, 'runId': builder.run, 'attempt': builder.attempt,
            'sourceSha': builder.sha, 'operation': 'cleanup' if args.cleanup_only else 'build',
            'status': 'passed' if succeeded else 'failed',
            'images': [{'role': r['role'], 'id': r['id'], 'state': r['state']} for r in builder.records],
            'toolchain': dict(TOOLCHAIN),
            'limits': {'totalBuildSeconds': 600, 'cleanupSeconds': 120, 'outputBytes': MAX_OUTPUT},
            'limitations': ['Unsigned integrity record, not supply-chain attestation.',
                'Debian apt dependencies are not hermetic; installed versions recorded inside test image at /usr/local/share/arcentra-image-journey/debian-packages.tsv.',
                'No registry push/login or global cache prune; official source images and build cache are retained.',
                'Lost build responses cannot establish daemon-side completion; cleanup fails on pending intent.']}
        private_json(evidence / ('cleanup-summary.json' if args.cleanup_only else 'build-summary.json'), receipt)

    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception as error:
        print('[image-build] FAILED: ' + str(error), file=sys.stderr)
        sys.exit(1)
