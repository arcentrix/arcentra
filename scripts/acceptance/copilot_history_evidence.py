"""Independent primary API checks for the real private-history browser journey."""
import hashlib
import re

DIGEST = re.compile(r'^[0-9a-f]{64}$')
IDENTITY = re.compile(r'^[A-Za-z0-9_-]{1,160}$')


def digest_text(value):
    if not isinstance(value, str) or not value:
        raise RuntimeError('history text is missing')
    return hashlib.sha256(value.encode()).hexdigest()


def validate_receipt(receipt, contract):
    expected = {'version', 'phase', 'actorId', 'projectId', 'pipelineId', 'runId',
                'conversationId', 'turns', 'plan', 'historySelections', 'previewDigests', 'uncheckedHistoryExcluded', 'sendCount', 'executionMutationCount'}
    if not isinstance(receipt, dict) or set(receipt) != expected or receipt['version'] != 'copilot-history-v1':
        raise RuntimeError('history receipt shape mismatch')
    for field in ('phase', 'actorId', 'projectId', 'pipelineId', 'runId'):
        if receipt[field] != contract[field]:
            raise RuntimeError('history receipt identity mismatch')
    if not isinstance(receipt['conversationId'], str) or not IDENTITY.fullmatch(receipt['conversationId']):
        raise RuntimeError('history receipt conversation missing')
    if (type(receipt['sendCount']) is not int or type(receipt['executionMutationCount']) is not int
            or receipt['sendCount'] != (2 if contract['phase'] == 'create' else 0) or receipt['executionMutationCount'] != 0):
        raise RuntimeError('history side effect counts mismatch')
    if (receipt['uncheckedHistoryExcluded'] is not True or not isinstance(receipt['previewDigests'], list)
            or len(receipt['previewDigests']) != 2 or len(set(receipt['previewDigests'])) != 2
            or any(not isinstance(digest, str) or not DIGEST.fullmatch(digest) for digest in receipt['previewDigests'])):
        raise RuntimeError('history approved preview evidence missing')
    turns = receipt['turns']
    if not isinstance(turns, list) or len(turns) != 2:
        raise RuntimeError('history receipt needs exactly two turns')
    for number, turn in enumerate(turns, 1):
        if (not isinstance(turn, dict) or set(turn) != {'turnId', 'requestId', 'sequence', 'questionSha256', 'textSha256', 'expiresAt'}
                or type(turn['sequence']) is not int or turn['sequence'] != number
                or not isinstance(turn['turnId'], str) or not IDENTITY.fullmatch(turn['turnId'])
                or not isinstance(turn['requestId'], str) or not IDENTITY.fullmatch(turn['requestId'])
                or not DIGEST.fullmatch(turn['questionSha256']) or not DIGEST.fullmatch(turn['textSha256'])
                or not isinstance(turn['expiresAt'], str) or not turn['expiresAt']):
            raise RuntimeError('history turn evidence invalid')
    if len({turn['turnId'] for turn in turns}) != 2 or len({turn['requestId'] for turn in turns}) != 2:
        raise RuntimeError('history turn identity duplicated')
    if receipt['historySelections'] != [[], [turns[0]['turnId']]]:
        raise RuntimeError('history explicit selection evidence missing')
    plan = receipt['plan']
    if (not isinstance(plan, dict) or set(plan) != {'planId', 'turnId', 'kind', 'state', 'version', 'planDigest', 'expiresAt', 'executionEnabled'}
            or plan['kind'] != 'new_run_review' or plan['state'] != 'reviewed' or plan['executionEnabled'] is not False
            or type(plan['version']) is not int or plan['version'] < 2 or not DIGEST.fullmatch(plan['planDigest'])
            or plan['turnId'] not in {turn['turnId'] for turn in turns}
            or not isinstance(plan['planId'], str) or not IDENTITY.fullmatch(plan['planId']) or not plan['expiresAt']):
        raise RuntimeError('history inert plan evidence invalid')
    if contract['phase'] == 'resume':
        previous = contract['previous']
        for field in ('conversationId', 'turns', 'plan', 'historySelections', 'previewDigests', 'uncheckedHistoryExcluded'):
            if receipt[field] != previous[field]:
                raise RuntimeError('history restart changed persisted evidence')
    return receipt


def validate_primary(detail, plan, receipt, forbidden_values):
    if not isinstance(detail, dict) or detail.get('nextAfterSequence', 0) != 0:
        raise RuntimeError('history primary page incomplete')
    conversation = detail.get('conversation', {})
    for field in ('conversationId', 'projectId', 'pipelineId', 'runId'):
        if conversation.get(field) != receipt[field]:
            raise RuntimeError('primary conversation binding mismatch')
    turns = detail.get('turns')
    if not isinstance(turns, list) or len(turns) != 2:
        raise RuntimeError('primary turn count mismatch')
    for turn, expected in zip(turns, receipt['turns']):
        for field in ('turnId', 'requestId', 'sequence', 'expiresAt'):
            if turn.get(field) != expected[field]:
                raise RuntimeError('primary turn identity mismatch')
        if turn.get('state') != 'completed' or turn.get('contentAvailable') is not True:
            raise RuntimeError('primary persisted answer unavailable')
        for field in ('question', 'text'):
            if digest_text(turn.get(field)) != expected[field + 'Sha256']:
                raise RuntimeError('primary history body hash mismatch')
            if any(secret and secret in turn[field] for secret in forbidden_values):
                raise RuntimeError('primary history body secret exposure')
        if 'SYNTHETIC_HISTORY_REPLY_' + str(expected['sequence']) not in turn['text']:
            raise RuntimeError('primary model answer marker missing')
    for field, expected in receipt['plan'].items():
        if plan.get(field) != expected:
            raise RuntimeError('primary reviewed plan mismatch')
    if plan.get('executionStatus') != 'Not executed':
        raise RuntimeError('primary plan unexpectedly executable')
