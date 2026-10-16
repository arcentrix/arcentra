"""Bounded, owned Docker lifecycle for the disposable image journey only."""
import json
import os
from pathlib import Path
import re
import selectors
import signal
import subprocess
import time

CONTROL_ENTRYPOINT = ['/arcentra', '-conf', '/conf.d/config.toml', '-plugin-conf', '/conf.d/plugins.toml']
AGENT_ENTRYPOINT = ['/arcentra-agent', '-conf', '/conf.d/agent.toml']
LABEL = 'io.arcentra.acceptance.image'
OWNER = 'image-journey-v1'
TOOLCHAIN = {'goVersion': 'go1.25.6', 'pythonVersion': '3.11.14',
             'goSourceDigest': 'sha256:f4490d7b261d73af4543c46ac6597d7d101b6e1755bcdd8c5159fda7046b6b3e',
             'pythonSourceDigest': 'sha256:65a93d69fa75478d554f4ad27c85c1e69fa184956261b4301ebaf6dbb0a3543d'}
JOURNEY_SECONDS = 300
MAX_DOCKER_OUTPUT = 2 * 1024 * 1024


def bounded_run(command, *, env, capture_output, text, timeout, check):
    """Drain both pipes within a byte/time ceiling, killing only this CLI group."""
    process = subprocess.Popen(command, env=env, stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    chunks = {'stdout': bytearray(), 'stderr': bytearray()}
    deadline = time.monotonic() + timeout
    completed = False
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ, 'stdout')
            selector.register(process.stderr, selectors.EVENT_READ, 'stderr')
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise subprocess.TimeoutExpired(command, timeout)
                for key, _ in selector.select(min(remaining, 0.1)):
                    data = os.read(key.fileobj.fileno(), 65536)
                    if not data:
                        selector.unregister(key.fileobj)
                        continue
                    if sum(len(v) for v in chunks.values()) + len(data) > MAX_DOCKER_OUTPUT:
                        raise RuntimeError('Docker output exceeded byte ceiling')
                    chunks[key.data].extend(data)
            code = process.wait(timeout=max(0.001, deadline - time.monotonic()))
        completed = True
        return subprocess.CompletedProcess(command, code, chunks['stdout'].decode(errors='replace'),
                                           chunks['stderr'].decode(errors='replace'))
    finally:
        if not completed:
            # The CLI parent may have exited while a child still holds a pipe.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=2)
        process.stdout.close()
        process.stderr.close()


ROLES = {'control': ('512m', '1', '128'), 'agent': ('1536m', '2', '256'),
         'cli': ('256m', '0.5', '64'), 'git': ('256m', '0.5', '64'), 'toolchain': ('256m', '0.5', '64')}


DIAGNOSTIC_OPERATIONS = {'network-inspect', 'image-inspect', 'container-create', 'container-inspect',
                         'container-start', 'container-logs', 'container-ls', 'container-stop', 'container-rm', 'unknown'}
DIAGNOSTIC_PHASES = {'command', 'inspect', 'wait', 'capture'}
DIAGNOSTIC_REASONS = {'permission-denied', 'readonly-filesystem', 'missing-executable', 'missing-library',
                      'missing-path', 'container-missing', 'network-missing', 'resource-limit',
                      'invalid-argument', 'unsupported-option', 'oci-runtime', 'unknown',
                      'timeout', 'interrupted', 'client-error', 'output-limit'}


def failure_reason(stderr):
    """Map bounded private diagnostics to static categories; never return input."""
    lowered = stderr.lower()
    predicates = (
        ('permission denied', 'permission-denied'), ('read-only file system', 'readonly-filesystem'),
        ('executable file not found', 'missing-executable'), ('error while loading shared libraries', 'missing-library'),
        ('no such container', 'container-missing'), ('network not found', 'network-missing'),
        ('no such file or directory', 'missing-path'), ('cannot allocate memory', 'resource-limit'),
        ('resource temporarily unavailable', 'resource-limit'), ('unknown flag', 'unsupported-option'),
        ('invalid argument', 'invalid-argument'), ('oci runtime', 'oci-runtime'),
    )
    return next((reason for text, reason in predicates if text in lowered), 'unknown')


def validate_failure(value):
    if value is None:
        return None
    if (not isinstance(value, dict) or set(value) != {'operation', 'phase', 'role', 'returnCode', 'reason'}
            or not isinstance(value['operation'], str) or value['operation'] not in DIAGNOSTIC_OPERATIONS
            or not isinstance(value['phase'], str) or value['phase'] not in DIAGNOSTIC_PHASES
            or (value['role'] is not None and (not isinstance(value['role'], str) or value['role'] not in ROLES))
            or (value['returnCode'] is not None and (type(value['returnCode']) is not int or not -255 <= value['returnCode'] <= 255))
            or not isinstance(value['reason'], str) or value['reason'] not in DIAGNOSTIC_REASONS):
        raise ValueError('invalid static failure diagnostic')
    return dict(value)


# Toolchain probe has no configuration/credential mounts. Still never retain raw
# daemon text: every emitted token must belong to this fixed generic vocabulary.
SAFE_ERROR_WORDS = set("""error response from daemon failed failure to create start initialize initialization
container containers task shim runtime oci runc exec executable format binary shell
invalid unsupported unknown option options argument arguments flag flags value values
mount mounting tmpfs filesystem file directory path rootfs root read-only readonly
rw ro nosuid nodev noexec uid gid mode size numeric number integer permission denied
operation not permitted allowed found no such does exist exists missing cannot unable
network networking endpoint bridge interface sandbox port address already use in
cgroup cgroups memory swap cpu cpus pids limit limits quota period resource resources
sysctl kernel seccomp apparmor capabilities privilege privileges security policy
logging log driver local initialize initialized read reading write writing supported
configuration config device space left on busy too many open files unavailable
out of range is are must be and or for the with setting set parse parsing
invalidargument invalidparameter notfound internal failedprecondition rpc code desc
connection refused timeout timed out killed signal conflict cannotstart service
no-new-privileges max-size max-file""".split())
START_FEATURES = ('tmpfs', 'uid', 'gid', 'mode', 'size', 'cgroup', 'sysctl', 'network',
                  'logging', 'exec', 'rootfs', 'readonly', 'permission', 'invalid',
                  'unsupported', 'oci', 'seccomp', 'apparmor', 'numeric')


def safe_error_projection(value):
    data = value.encode(errors='replace')
    head = data[:2048].decode(errors='replace')
    tail = data[-2048:].decode(errors='replace')
    def tokens(text):
        result = []
        for item in text.split():
            item = item.lower().strip("\"'[](),.;:")
            result.append(item if item in SAFE_ERROR_WORDS else '[redacted]')
        return result
    sample = (head + ' ' + tail).lower()
    return {'head': tokens(head)[:80], 'tail': tokens(tail)[-80:],
            'truncated': len(data) > 4096,
            'features': {word: (bool(re.search(r'\d', sample)) if word == 'numeric' else word in sample)
                         for word in START_FEATURES}}


def validate_projection(value):
    if (not isinstance(value, dict) or set(value) != {'head', 'tail', 'truncated', 'features'}
            or type(value['truncated']) is not bool or not isinstance(value['features'], dict)
            or set(value['features']) != set(START_FEATURES)
            or any(type(flag) is not bool for flag in value['features'].values())):
        raise ValueError('invalid safe diagnostic projection')
    for key in ('head', 'tail'):
        if (not isinstance(value[key], list) or len(value[key]) > 80
                or any(not isinstance(token, str) or token not in SAFE_ERROR_WORDS | {'[redacted]'} for token in value[key])):
            raise ValueError('unreviewed diagnostic token')
    return value


def validate_start_diagnostic(value, failure):
    if value is None:
        return None
    if (not isinstance(value, dict) or set(value) != {'stdout', 'stderr', 'stateErrorStatus', 'stateError'}
            or failure is None or failure['operation'] != 'container-start' or failure['role'] != 'toolchain'
            or not isinstance(value['stateErrorStatus'], str) or value['stateErrorStatus'] not in ('pending', 'available', 'unavailable')):
        raise ValueError('toolchain start diagnostic outside fixed scope')
    validate_projection(value['stdout'])
    validate_projection(value['stderr'])
    if value['stateErrorStatus'] == 'available':
        validate_projection(value['stateError'])
    elif value['stateError'] is not None:
        raise ValueError('unexpected state error projection')
    return value


def toolchain_contract():
    return {'entrypoint': '/bin/sh', 'entrypointPermissions': 'not-observed',
            'readonlyRootfs': True, 'capDrop': ['ALL'], 'noNewPrivileges': True,
            'tmpfs': {'target': '/tmp', 'options': ['rw', 'nosuid', 'nodev'], 'sizeMiB': 128,
                      'uid': os.getuid(), 'gid': os.getgid(), 'mode': '0700'},
            'memoryMiB': 256, 'memorySwapMiB': 256, 'cpus': 0.5, 'pidsLimit': 64,
            'network': 'existing-run-compose-bridge', 'bindMounts': [], 'publishedPorts': [],
            'logDriver': 'local', 'logMaxSize': '8m', 'logMaxFile': '1'}


def fullmatch(pattern, value):
    return isinstance(value, str) and re.fullmatch(pattern, value) is not None


def validate_manifest(value):
    if not isinstance(value, dict) or set(value) != {'schemaVersion', 'runId', 'attempt', 'sourceSha', 'composeProject', 'networkId', 'images', 'toolchain'}:
        raise ValueError('invalid image manifest fields')
    if type(value['schemaVersion']) is not int or value['schemaVersion'] != 1:
        raise ValueError('unsupported image manifest version')
    if not all(fullmatch(r'[1-9][0-9]{0,19}', value[k]) for k in ('runId', 'attempt')):
        raise ValueError('invalid run/attempt')
    if not fullmatch(r'[a-f0-9]{40}', value['sourceSha']) or not fullmatch(r'[a-f0-9]{64}', value['networkId']):
        raise ValueError('full source SHA and network ID required')
    if value['composeProject'] != f"arcentra-image-{value['runId']}-{value['attempt']}":
        raise ValueError('network project is not the exact run-owned project')
    if not isinstance(value['images'], dict) or set(value['images']) != {'control', 'agent', 'agentBase'}:
        raise ValueError('exact control/agent/agentBase local IDs required')
    for image in value['images'].values():
        if (not isinstance(image, dict) or set(image) != {'id', 'sourceSha'}
                or not fullmatch(r'sha256:[a-f0-9]{64}', image['id'])
                or image['sourceSha'] != value['sourceSha']):
            raise ValueError('image tags, remote references or different source forbidden')
    if value['toolchain'] != TOOLCHAIN:
        raise ValueError('toolchain must match exact reviewed official source pins')
    if value['images']['agent']['id'] == value['images']['agentBase']['id']:
        raise ValueError('Agent must be an explicit test-toolchain derivative')
    return value


def private_json(path, value):
    path = Path(path)
    temporary = path.with_suffix('.tmp')
    fd = os.open(temporary, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as output:
        json.dump(value, output)
    temporary.replace(path)


class ContainerHandle:
    """Process-compatible facade whose truth comes from daemon inspection."""
    def __init__(self, runtime, record):
        self.runtime, self.record, self.returncode = runtime, record, None

    def poll(self):
        state = self.runtime.inspect_owned(self.record, phase='wait')['State']
        status = state.get('Status')
        if status == 'running' and state.get('Running') is True:
            return None
        if status in ('exited', 'dead') and state.get('Running') is False and type(state.get('ExitCode')) is int:
            self.returncode = state['ExitCode']
            return self.returncode
        self.runtime.blocked = True
        raise RuntimeError('container state is unknown or not running; new admission stopped')

    def wait(self, timeout):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            code = self.poll()
            if code is not None:
                return code
            time.sleep(0.25)
        self.runtime.blocked = True
        raise RuntimeError('container wait exceeded fixed bound')


class ImageRuntime:
    def __init__(self, manifest, intent_path, root, execute=None):
        self.manifest = validate_manifest(manifest)
        self.intent_path, self.root = Path(intent_path), Path(root).resolve()
        self.execute = execute or bounded_run
        self.records = []
        self.blocked = False
        self.failure = None
        self.start_diagnostic = None
        self.deadline = time.monotonic() + JOURNEY_SECONDS
        self.cleanup_deadline = None
        self.uid, self.gid = os.getuid(), os.getgid()
        if self.uid == 0 or self.gid == 0:
            raise ValueError('image journey requires the non-root runner UID/GID')

    def command(self, arguments, timeout=20, cleanup=False, phase='command'):
        if not cleanup and (self.blocked or time.monotonic() >= self.deadline):
            raise RuntimeError('new Docker admission stopped')
        if cleanup and self.cleanup_deadline is None:
            raise RuntimeError('cleanup bypass requires established cleanup deadline')
        remaining = (self.cleanup_deadline if cleanup else self.deadline) - time.monotonic()
        if remaining <= 0:
            raise RuntimeError('Docker operation deadline exceeded')
        timeout = min(timeout, remaining)
        # No ambient Docker endpoint/context/TLS settings or proxy credentials.
        env = {'PATH': '/usr/bin:/bin', 'HOME': str(self.root), 'LANG': 'C.UTF-8'}
        try:
            result = self.execute(['docker', *arguments], env=env, capture_output=True,
                                  text=True, timeout=timeout, check=False)
        except (subprocess.TimeoutExpired, KeyboardInterrupt, OSError, RuntimeError) as exc:
            self.blocked = True
            reason = ('timeout' if isinstance(exc, subprocess.TimeoutExpired) else
                      'interrupted' if isinstance(exc, KeyboardInterrupt) else 'client-error')
            diagnostic = self.record_failure(arguments, phase, None, reason)
            raise RuntimeError('Docker operation interrupted or uncertain; admission stopped: ' + diagnostic) from None
        if len(result.stdout.encode()) + len(result.stderr.encode()) > MAX_DOCKER_OUTPUT:
            self.blocked = True
            diagnostic = self.record_failure(arguments, phase, None, 'output-limit')
            raise RuntimeError('Docker output exceeded byte ceiling: ' + diagnostic)
        if result.returncode:
            self.blocked = True
            diagnostic = self.record_failure(arguments, phase, result.returncode, failure_reason(result.stderr), result.stdout, result.stderr)
            raise RuntimeError('Docker operation failed; admission stopped: ' + diagnostic)
        return result.stdout

    def record_failure(self, arguments, phase, code, reason, stdout="", stderr=""):
        operation = '-'.join(arguments[:2])
        if operation not in DIAGNOSTIC_OPERATIONS:
            operation = 'unknown'
        roles = {record['role'] for record in self.records
                 if record['name'] in arguments or (record['id'] is not None and record['id'] in arguments)}
        value = validate_failure({'operation': operation, 'phase': phase,
                                  'role': next(iter(roles)) if len(roles) == 1 else None,
                                  'returnCode': code if type(code) is int and -255 <= code <= 255 else None,
                                  'reason': reason})
        if self.failure is None:
            self.failure = value
            if operation == 'container-start' and value['role'] == 'toolchain':
                self.start_diagnostic = {'stdout': safe_error_projection(stdout), 'stderr': safe_error_projection(stderr),
                                         'stateErrorStatus': 'pending', 'stateError': None}
            self.save()  # Independent cleanup must retain the first diagnostic.
        return json.dumps(value, sort_keys=True)

    def labels(self, role):
        m = self.manifest
        return {LABEL: 'v1', LABEL + '.owner': OWNER, LABEL + '.run': m['runId'], LABEL + '.attempt': m['attempt'],
                LABEL + '.sha': m['sourceSha'], LABEL + '.role': role}

    def inspect_json(self, kind, identifier, cleanup=False, phase='inspect'):
        try:
            value = json.loads(self.command([kind, 'inspect', identifier], cleanup=cleanup, phase=phase))
            if not isinstance(value, list) or len(value) != 1 or not isinstance(value[0], dict):
                raise ValueError()
            return value[0]
        except (ValueError, TypeError):
            self.blocked = True
            raise RuntimeError('invalid Docker inspect response') from None

    def verify_inputs(self):
        network = self.inspect_json('network', self.manifest['networkId'])
        if (network.get('Id') != self.manifest['networkId'] or network.get('Driver') != 'bridge'
                or network.get('Scope') != 'local' or network.get('Ingress')
                or network.get('Labels', {}).get('com.docker.compose.project') != self.manifest['composeProject']
                or network.get('Labels', {}).get('com.docker.compose.network') != 'default'
                or network.get('Name') != self.manifest['composeProject'] + '_default'):
            raise ValueError('requires the exact existing run-owned Compose bridge')
        inspected = {}
        for role, metadata in self.manifest['images'].items():
            identifier = metadata['id']
            image = self.inspect_json('image', identifier)
            inspected[role] = image
            config = image.get('Config', {})
            expected = CONTROL_ENTRYPOINT if role == 'control' else AGENT_ENTRYPOINT
            if (image.get('Id') != identifier or config.get('Entrypoint') != expected
                    or config.get('Cmd') not in (None, []) or config.get('WorkingDir') != '/var/lib/arcentra'
                    or image.get('Os') != 'linux' or image.get('Architecture') != 'amd64'
                    or any(config.get('Labels', {}).get(k) != v for k, v in self.labels(role).items())
                    or config.get('Volumes') or config.get('OnBuild')):
                raise ValueError('image defaults differ from reviewed packaging contract')
        base = inspected['agentBase'].get('RootFS', {}).get('Layers', [])
        derived = inspected['agent'].get('RootFS', {}).get('Layers', [])
        if not base or len(derived) <= len(base) or derived[:len(base)] != base:
            raise ValueError('test Agent does not retain the tested stock Agent layers')
        memory = Path('/proc/meminfo').read_text()
        available = re.search(r'^MemAvailable:\s+(\d+) kB$', memory, re.M)
        if not available or int(available[1]) < 3 * 1024 * 1024:
            raise RuntimeError('at least 3 GiB available runner memory required')

    def save(self):
        private_json(self.intent_path, {'schemaVersion': 1, 'manifest': self.manifest,
                                       'root': str(self.root), 'containers': self.records, 'failure': validate_failure(self.failure),
                                       'startDiagnostic': validate_start_diagnostic(self.start_diagnostic, self.failure)})

    def mount(self, source, destination, readonly=True):
        requested = Path(source)
        source = requested.resolve(strict=True)
        if source != requested.absolute() or requested.is_symlink():
            raise ValueError('symlink mount paths forbidden')
        if not source.is_dir() or source == self.root or self.root not in source.parents:
            raise ValueError('mount must be a narrow fixture-owned directory')
        if any(character in str(source) for character in ',\n\r'):
            raise ValueError('unsafe mount path')
        return ['--mount', f'type=bind,src={source},dst={destination}' + (',readonly' if readonly else '')]

    def create(self, role, config=None, state=None, repositories=None, arguments=None):
        if self.blocked or time.monotonic() >= self.deadline:
            raise RuntimeError('new container admission stopped')
        if role not in ROLES or (role in ('control', 'agent') and arguments is not None):
            raise ValueError('unreviewed role or application entrypoint override')
        if role in ('control', 'agent', 'cli'):
            if config != self.root / f'image-{role}-config' or state != self.root / f'image-{role}-state' or repositories is not None:
                raise ValueError('role receives only its exact private config/state')
        elif config is not None or state is not None:
            raise ValueError('helper cannot mount application config/state')
        if role == 'git' and repositories != self.root / 'repositories':
            raise ValueError('Git receives only the fixture repository')
        if role != 'git' and repositories is not None:
            raise ValueError('repository mount restricted to Git helper')
        if role != 'cli' and arguments is not None:
            raise ValueError('helper command overrides forbidden')
        # No alias reuse until the original container has been removed and verified.
        if any(item['role'] == role and not item.get('removed') for item in self.records):
            raise RuntimeError('original role has not been removed')
        sequence = len(self.records)
        name = f"arcentra-image-{self.manifest['runId']}-{self.manifest['attempt']}-{role}-{sequence}"
        image = self.manifest['images']['agent' if role in ('agent', 'toolchain') else 'control']['id']
        record = {'name': name, 'id': None, 'role': role, 'image': image, 'removed': False, 'createState': 'not-submitted', 'removeState': 'not-submitted'}
        self.records.append(record)
        self.save()  # Ownership intent survives a lost/timeout create response.
        memory, cpus, pids = ROLES[role]
        command = ['container', 'create', '--name', name, '--pull', 'never', '--read-only',
                   '--user', f'{self.uid}:{self.gid}', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true',
                   '--memory', memory, '--memory-swap', memory, '--cpus', cpus, '--pids-limit', pids,
                   '--network', self.manifest['networkId'], '--restart', 'no',
                   '--log-driver', 'local', '--log-opt', 'max-size=8m', '--log-opt', 'max-file=1',
                   '--tmpfs', f'/tmp:rw,nosuid,nodev,size=128m,uid={self.uid},gid={self.gid},mode=0700']
        for key, value in self.labels(role).items():
            command += ['--label', f'{key}={value}']
        if role in ('control', 'agent', 'git'):
            command += ['--network-alias', role]
            ports = {'control': '18080:18080', 'agent': '18081:18081', 'git': '19418:9418'}
            command += ['--publish', '127.0.0.1:' + ports[role]]
        if role in ('control', 'agent', 'cli'):
            if config is None or state is None:
                raise ValueError('private configuration and state are mandatory')
            command += self.mount(config, '/conf.d') + self.mount(state, '/var/lib/arcentra', False)
        if role in ('agent', 'toolchain'):
            for key, value in {'PATH': '/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin', 'HOME': '/var/lib/arcentra/home' if role == 'agent' else '/tmp',
                               'TMPDIR': '/var/lib/arcentra/tmp' if role == 'agent' else '/tmp',
                               'GOCACHE': '/var/lib/arcentra/go-cache' if role == 'agent' else '/tmp/go-cache', 'GOTOOLCHAIN': 'local', 'GOPROXY': 'off',
                               'GOSUMDB': 'off', 'GOWORK': 'off', 'GOENV': 'off', 'CGO_ENABLED': '0',
                               'GOTELEMETRY': 'off', 'GOMAXPROCS': '2'}.items():
                command += ['--env', key + '=' + value]
        if role == 'cli':
            if arguments not in (['migrate'], ['bootstrap-admin', '--username', 'ci-admin']):
                raise ValueError('unreviewed CLI action')
            command += ['--entrypoint', '/usr/local/bin/arcentra-cli', image,
                        arguments[0], '--conf', '/conf.d/config.toml', *arguments[1:]]
        elif role == 'git':
            command += self.mount(repositories, '/repositories')
            command += ['--entrypoint', '/usr/bin/git', image, 'daemon', '--listen=0.0.0.0', '--port=9418',
                        '--export-all', '--base-path=/repositories', '/repositories']
        elif role == 'toolchain':
            command += ['--entrypoint', '/bin/sh', image, '-ec',
                        'test "$(go version)" = "go version go1.25.6 linux/amd64"; test "$(python3 --version)" = "Python 3.11.14"']
        else:
            command += [image]  # Deliberately no application arguments or override.
        record['createState'] = 'pending'
        self.save()
        try:
            identifier = self.command(command).strip()
        except BaseException:
            self.blocked = True
            record['createState'] = 'uncertain'
            self.save()
            raise
        if not fullmatch(r'[a-f0-9]{64}', identifier):
            self.blocked = True
            raise RuntimeError('create returned uncertain identity')
        record['id'] = identifier
        record['createState'] = 'confirmed'
        self.save()
        self.inspect_owned(record)
        self.command(['container', 'start', identifier])
        return ContainerHandle(self, record)

    def inspect_owned(self, record, cleanup=False, phase='inspect'):
        value = self.inspect_json('container', record['id'] or record['name'], cleanup, phase=phase)
        labels = value.get('Config', {}).get('Labels', {})
        if (value.get('Name') != '/' + record['name'] or value.get('Image') != record['image']
                or (record['id'] and value.get('Id') != record['id'])
                or not fullmatch(r'[a-f0-9]{64}', value.get('Id'))
                or any(labels.get(k) != v for k, v in self.labels(record['role']).items())):
            self.blocked = True
            raise RuntimeError('container ownership/immutable identity mismatch')
        if (cleanup and record['role'] == 'toolchain' and self.start_diagnostic is not None
                and self.start_diagnostic['stateErrorStatus'] == 'pending'):
            try:
                state_error = value.get('State', {}).get('Error')
                if not isinstance(state_error, str):
                    raise ValueError('state error unavailable')
                self.start_diagnostic['stateError'] = safe_error_projection(state_error)
                self.start_diagnostic['stateErrorStatus'] = 'available'
                self.save()
            except Exception:
                self.start_diagnostic['stateErrorStatus'] = 'unavailable'
                self.start_diagnostic['stateError'] = None
        return value

    def remove(self, record, cleanup=False):
        if record.get('removed'):
            return
        # An interrupted create/rm may have changed daemon state without a reply.
        # Discover only this exact intended name and ownership, never global prune.
        query = ['container', 'ls', '--all', '--no-trunc', '--format', '{{.ID}}',
                 '--filter', 'name=^/' + record['name'] + '$']
        candidates = self.command(query, cleanup=cleanup).splitlines()
        if not candidates:
            if record['createState'] in ('pending', 'uncertain') and record['id'] is None:
                self.blocked = True
                raise RuntimeError('unresolved create may still complete; empty lookup is not cleanup proof')
            record['removeState'] = 'confirmed'
            record['removed'] = True
            self.save()
            return
        if len(candidates) != 1 or not fullmatch(r'[a-f0-9]{64}', candidates[0]):
            self.blocked = True
            raise RuntimeError('uncertain owned container lookup')
        value = self.inspect_owned(record, cleanup)
        if value['Id'] != candidates[0]:
            self.blocked = True
            raise RuntimeError('container lookup identity changed')
        identifier = value['Id']
        record['id'] = identifier
        record['createState'] = 'confirmed'
        self.save()
        state = value.get('State', {})
        if state.get('Running') is True:
            self.command(['container', 'stop', '--time', '10', identifier], timeout=15, cleanup=cleanup)
        value = self.inspect_owned(record, cleanup)
        if value.get('State', {}).get('Running') is not False or value['State'].get('Status') not in ('created', 'exited', 'dead'):
            self.blocked = True
            raise RuntimeError('refusing removal/reuse in uncertain container state')
        record['removeState'] = 'pending'
        self.save()
        try:
            self.command(['container', 'rm', identifier], cleanup=cleanup)
        except BaseException:
            self.blocked = True
            record['removeState'] = 'uncertain'
            self.save()
            raise
        if self.command(query, cleanup=cleanup).splitlines():
            self.blocked = True
            raise RuntimeError('container still exists after removal; alias reuse forbidden')
        record['removeState'] = 'confirmed'
        record['removed'] = True
        self.save()

    def cleanup(self):
        self.blocked = True
        failures = []
        deadline = self.cleanup_deadline or (time.monotonic() + 180)
        self.cleanup_deadline = deadline
        for record in reversed(self.records):
            if time.monotonic() >= deadline:
                failures.append('cleanup deadline exceeded')
                break
            try:
                self.remove(record, cleanup=True)
            except Exception:
                failures.append(record['name'])
        if self.start_diagnostic is not None and self.start_diagnostic['stateErrorStatus'] == 'pending':
            self.start_diagnostic['stateErrorStatus'] = 'unavailable'
            try:
                self.save()
            except Exception:
                pass  # Diagnostic capture must not change the cleanup verdict.
        if failures:
            raise RuntimeError('owned container cleanup incomplete: ' + ', '.join(failures))
