"""Strict, metadata-only evidence for the disposable personal Inbox journey."""
import datetime
import re

READ_STEPS = ['run-results-delivered', 'failed-run-visible', 'successful-run-visible',
              'successful-run-marked-read', 'read-state-reloaded']
RESUME_STEPS = ['same-requester-login', 'persisted-read-entry', 'persisted-unread-entry']
ITEM_KEYS = {'id', 'kind', 'status', 'occurredAt', 'deliveredAt', 'readAt', 'target', 'availability'}


def identifier(value):
    if not isinstance(value, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', value):
        raise ValueError('invalid Inbox evidence identity')
    return value


def instant(value):
    if not isinstance(value, str) or len(value) > 64:
        raise ValueError('invalid Inbox evidence timestamp')
    try:
        parsed = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
    except ValueError:
        raise ValueError('invalid Inbox evidence timestamp') from None
    if parsed.tzinfo is None:
        raise ValueError('Inbox evidence timestamp requires a timezone')
    return value


def validate_run_item(item, expected, *, read):
    """Only the exact independently verified run is eligible, never a marker/name."""
    if not isinstance(item, dict) or set(item) != ITEM_KEYS:
        raise ValueError('Inbox evidence requires exact metadata projection')
    identifier(item['id'])
    if (item['kind'] != 'run_result' or item['status'] != expected['status']
            or item['availability'] != 'available'):
        raise ValueError('Inbox run result is not the exact available terminal fact')
    target = item['target']
    wanted = {'type': 'run', 'projectId': expected['projectId'],
              'pipelineId': expected['pipelineId'], 'runId': expected['runId']}
    if target != wanted:
        raise ValueError('Inbox run result has a different source or scope')
    for field in ('projectId', 'pipelineId', 'runId'):
        identifier(target[field])
    instant(item['occurredAt']); instant(item['deliveredAt'])
    if read:
        instant(item['readAt'])
    elif item['readAt'] is not None:
        raise ValueError('Inbox unread evidence was already marked read')
    return item


def validate_receipt(value, phase, actor_id, failed, successful, previous=None):
    keys = {'journey', 'phase', 'completed', 'steps', 'userId', 'readEntry', 'unreadEntry'}
    if (not isinstance(value, dict) or set(value) != keys or value['journey'] != 'personal-inbox-v1'
            or value['phase'] != phase or value['completed'] is not True
            or value['userId'] != actor_id or phase not in ('read', 'resume')):
        raise ValueError('Inbox browser receipt is incomplete or belongs to another requester')
    identifier(actor_id)
    if value['steps'] != (READ_STEPS if phase == 'read' else RESUME_STEPS):
        raise ValueError('Inbox browser receipt is missing a required stage')
    if failed['runId'] == successful['runId'] or failed['status'] != 'failed' or successful['status'] != 'success':
        raise ValueError('Inbox fixtures must identify distinct independently verified outcomes')
    read = validate_run_item(value['readEntry'], successful, read=True)
    unread = validate_run_item(value['unreadEntry'], failed, read=False)
    if read['id'] == unread['id']:
        raise ValueError('Inbox outcomes require distinct delivery identities')
    if phase == 'resume':
        if not isinstance(previous, dict) or previous.get('phase') != 'read':
            raise ValueError('Inbox restart proof requires prior browser read evidence')
        if value['readEntry'] != previous.get('readEntry') or value['unreadEntry'] != previous.get('unreadEntry'):
            raise ValueError('Inbox restart changed persisted delivery or read metadata')
    return value


def validate_primary_snapshot(value, receipt):
    """The independent authenticated API snapshot must contain both exact rows."""
    if (not isinstance(value, dict) or set(value) != {'list', 'nextCursor', 'scanIncomplete'}
            or not isinstance(value['list'], list) or len(value['list']) > 50
            or value['scanIncomplete'] is not False or value['nextCursor'] is not None):
        raise ValueError('Inbox fixture requires one complete bounded primary snapshot')
    rows = value['list']
    identities = [row.get('id') if isinstance(row, dict) else None for row in rows]
    if any(not isinstance(i, str) for i in identities) or len(set(identities)) != len(identities):
        raise ValueError('Inbox snapshot has missing or duplicate delivery identities')
    for key in ('readEntry', 'unreadEntry'):
        expected = receipt[key]
        matches = [row for row in rows if row['id'] == expected['id']]
        if matches != [expected]:
            raise ValueError('Inbox primary snapshot does not preserve exact browser evidence')
    return True
