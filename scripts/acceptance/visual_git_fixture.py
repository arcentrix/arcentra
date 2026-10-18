"""Fixed disposable definition repository for actual visual-editor acceptance.

This creates no external remote or credential. Only this repository enables
receive-pack; the existing compiler source repository remains read-only.
"""
import json
from pathlib import Path
import shutil

GUARD = Path(__file__).with_name('visual_spec_pre_receive.sh')
INITIAL_SPEC = {
    'namespace': 'visual-editor-acceptance',
    'variables': {'PRESERVED_VISUAL_VALUE': 'retained'},
    'jobs': [{'name': 'visual-build', 'description': 'Original job',
              'steps': [{'name': 'original-step', 'uses': 'shell', 'action': 'run',
                         'runOnAgent': True, 'args': {'run': "printf 'VISUAL_ORIGINAL_STEP\\n'"}}]}],
}


def prepare_visual_repository(root, run):
    root = Path(root)
    source, bare = root / 'visual-definition-source', root / 'repositories/visual.git'
    if source.exists() or source.is_symlink() or bare.exists() or bare.is_symlink():
        raise ValueError('visual definition fixture already exists')
    if (root / 'repositories').resolve() != (root / 'repositories').absolute():
        raise ValueError('visual definition repository parent must be private and direct')
    source.mkdir()
    (source / 'pipeline.json').write_text(json.dumps(INITIAL_SPEC, indent=2) + '\n')
    run('visual-git-init', ['git', 'init', '--initial-branch=main', str(source)])
    run('visual-git-add', ['git', 'add', 'pipeline.json'], source)
    run('visual-git-commit', ['git', '-c', 'user.name=Acceptance Fixture', '-c',
                            'user.email=acceptance@example.invalid', 'commit', '-m',
                            'Initial synthetic visual definition'], source)
    run('visual-git-bare', ['git', 'clone', '--bare', str(source), str(bare)])
    for name, value in [('daemon.receivepack', 'true'), ('receive.denyDeletes', 'true'),
                        ('receive.denyNonFastForwards', 'true'), ('receive.maxInputSize', '2097152')]:
        run('visual-git-config-' + name, ['git', 'config', name, value], bare)
    run('visual-git-bind-base', ['git', 'update-ref', 'refs/arcentra/fixture-base', 'HEAD'], bare)
    hook = bare / 'hooks/pre-receive'
    shutil.copyfile(GUARD, hook)
    hook.chmod(0o700)
    return bare

MARKER = 'VISUAL_ACCEPTANCE'
MAX_VISUAL_LOG_ENTRIES = 1000
MAX_VISUAL_LOG_BYTES = 1024 * 1024
VISUAL_STEPS = ['pipeline-created', 'graph-edited', 'cycle-rejected', 'draft-validated',
                'definition-saved', 'definition-reloaded', 'run-triggered',
                'jobs-succeeded', 'logs-verified', 'source-unknown']


def saved_spec():
    import copy
    value = copy.deepcopy(INITIAL_SPEC)
    def step(name, suffix):
        return {'name': name, 'uses': 'shell', 'action': 'run', 'runOnAgent': True,
                'args': {'run': "printf '" + MARKER + '_' + suffix + "\\n'"}}
    value['jobs'][0]['steps'] = [step('original-step', 'EDITED'), step('visual-verify', 'VERIFY')]
    value['jobs'].append({'name': 'visual-package', 'dependsOn': ['visual-build'],
                          'steps': [step('step-1', 'PACKAGE')]})
    return value


def validate_visual_receipt(value, project_id, original_commit):
    import re
    keys = {'journey', 'completed', 'steps', 'projectId', 'pipelineId', 'runId',
            'baseDefinitionCommit', 'savedDefinitionCommit'}
    if (not isinstance(value, dict) or set(value) != keys or value['journey'] != 'visual-ci-v1'
            or value['completed'] is not True or value['steps'] != VISUAL_STEPS
            or value['projectId'] != project_id or value['baseDefinitionCommit'] != original_commit):
        raise ValueError('visual editor journey receipt is incomplete or belongs to another fixture')
    for key in ('projectId', 'pipelineId', 'runId'):
        if not isinstance(value[key], str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,64}', value[key]):
            raise ValueError('invalid visual editor identity')
    if (not isinstance(value['savedDefinitionCommit'], str)
            or not re.fullmatch(r'[a-f0-9]{40}', value['savedDefinitionCommit'])
            or value['savedDefinitionCommit'] == original_commit):
        raise ValueError('visual editor must save a distinct full definition commit')
    return value


def validate_visual_pipeline(receipt, pipeline, repository_url):
    if (pipeline.get('pipelineId') != receipt['pipelineId'] or pipeline.get('projectId') != receipt['projectId']
            or pipeline.get('repoUrl') != repository_url or pipeline.get('defaultBranch') != 'main'
            or pipeline.get('pipelineFilePath') != 'pipeline.json'):
        raise ValueError('visual pipeline does not belong to the fixed project and definition source')


def validate_visual_execution(receipt, run, jobs, logs, agent_id):
    if (run.get('runId') != receipt['runId'] or run.get('pipelineId') != receipt['pipelineId']
            or run.get('status') not in (3, 'PIPELINE_STATUS_SUCCESS')
            or run.get('definitionCommitSha') != receipt['savedDefinitionCommit']
            or run.get('allowDeployments', False)):
        raise ValueError('visual run is not successful for the exact saved definition')
    records = jobs.get('list')
    if not isinstance(records, list) or len(records) != 2 or jobs.get('total') != 2:
        raise ValueError('visual run requires exactly two actual jobs')
    identities = set()
    expected_outputs = {}
    for expected in saved_spec()['jobs']:
        matches = [job for job in records if job.get('jobName') == expected['name']]
        if len(matches) != 1:
            raise ValueError('visual run job identities differ')
        job = matches[0]
        if (job.get('pipelineId') != receipt['pipelineId'] or job.get('pipelineRunId') != receipt['runId']
                or job.get('agentId') != agent_id or job.get('status') != 4
                or job.get('sourceRepository') or job.get('sourceResolvedCommit')
                or job.get('sourceProvenanceState') == 'reported'):
            raise ValueError('visual job execution identity or source evidence differs')
        identity = job.get('jobRunId')
        if not isinstance(identity, str) or not identity or identity in identities:
            raise ValueError('visual jobs require distinct persisted identities')
        identities.add(identity)
        steps = job.get('steps', [])
        if [step.get('name') for step in steps] != [step['name'] for step in expected['steps']]:
            raise ValueError('visual steps differ from the saved definition')
        for step in steps:
            identity = step.get('stepRunId')
            if (not isinstance(identity, str) or not identity or identity in identities
                    or step.get('jobRunId') != job['jobRunId'] or step.get('status') != 4
                    or step.get('exitCode') != 0):
                raise ValueError('visual step is not a distinct successful Agent execution')
            identities.add(identity)
            suffix = {'original-step': 'EDITED', 'visual-verify': 'VERIFY', 'step-1': 'PACKAGE'}[step['name']]
            expected_outputs[(job['jobRunId'], identity)] = MARKER + '_' + suffix
    validate_visual_logs(expected_outputs, logs)


def validate_visual_logs(expected_outputs, logs):
    entries = logs.get('entries') if isinstance(logs, dict) else None
    if (not isinstance(entries, list) or logs.get('truncated') is not False
            or len(entries) > MAX_VISUAL_LOG_ENTRIES):
        raise ValueError('visual execution requires complete persisted logs')
    grouped = {identity: {} for identity in expected_outputs}
    total_bytes = 0
    for entry in entries:
        if (not isinstance(entry, dict) or not isinstance(entry.get('jobId'), str)
                or not isinstance(entry.get('stepId'), str)):
            raise ValueError('visual log does not belong to a verified job/step')
        identity = (entry.get('jobId'), entry.get('stepId'))
        message = entry.get('message')
        if identity not in expected_outputs or not isinstance(message, str):
            raise ValueError('visual log does not belong to a verified job/step')
        number, stream = entry.get('lineNumber'), entry.get('stream')
        if (type(number) is not int or not 1 <= number <= MAX_VISUAL_LOG_ENTRIES
                or number in grouped[identity] or stream not in ('stdout', 'stderr')):
            raise ValueError('visual log requires unique positive positions and a recognized stream')
        if len(message) > MAX_VISUAL_LOG_BYTES:
            raise ValueError('visual execution logs exceed the byte budget')
        try:
            total_bytes += len(message.encode('utf-8'))
        except UnicodeEncodeError:
            raise ValueError('visual execution logs require valid UTF-8') from None
        if total_bytes > MAX_VISUAL_LOG_BYTES:
            raise ValueError('visual execution logs exceed the byte budget')
        grouped[identity][number] = (stream, message)

    markers = set(expected_outputs.values())
    for identity, positions in grouped.items():
        # The Agent persists arbitrary Write chunks, not individual text lines.
        # Positions are step-wide across stdout/stderr; never join other steps
        # or let stderr supply a missing stdout marker.
        if sorted(positions) != list(range(1, len(positions) + 1)):
            raise ValueError('visual execution log positions are incomplete')
        stdout = ''.join(positions[number][1] for number in sorted(positions)
                         if positions[number][0] == 'stdout')
        lines = stdout.split('\n')
        if lines[-1] or lines[:-1].count(expected_outputs[identity]) != 1:
            raise ValueError('visual execution requires one complete exact command output in its own step')
        if any(line in markers and line != expected_outputs[identity] for line in lines[:-1]):
            raise ValueError('visual command output belongs to another step')


def classify_visual_save_failure(message):
    """Return fixed diagnostics only; never echo a Git URL, command or error body."""
    if not isinstance(message, str) or len(message) > 16384:
        return 'unclassified'
    if message.startswith('git commit failed:'):
        if 'Committer identity unknown' in message or 'Author identity unknown' in message:
            return 'git-identity-missing'
        return 'git-commit-failed'
    for prefix, category in (
        ('clone repo failed:', 'git-clone-failed'),
        ('read head failed:', 'git-read-head-failed'),
        ('create directory failed:', 'workspace-directory-failed'),
        ('write spec failed:', 'workspace-write-failed'),
        ('git add failed:', 'git-add-failed'),
        ('read commit failed:', 'git-read-commit-failed'),
        ('create branch failed:', 'git-create-branch-failed'),
        ('git push failed:', 'git-push-failed'),
        ('create pull request failed:', 'change-request-failed'),
    ):
        if message.startswith(prefix):
            return category
    return 'unclassified'
