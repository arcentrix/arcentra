"""Disposable real-browser Inbox persistence check, using existing fixture services.

No database seeding, new listener, or external delivery is introduced. The caller
must have independently verified both original real Agent build outcomes first.
"""
import hashlib
import json

from inbox_evidence import validate_primary_snapshot, validate_receipt, validate_run_item


def require_browser_summary(value):
    if not isinstance(value, dict) or value.get('kind') != 'actual-backend' or value.get('status') != 'passed':
        raise RuntimeError('Inbox browser did not pass its strict harness')
    for key in ('pageErrorCount', 'secretExposureCount', 'consoleInspectionFailureCount',
                'lateConsoleEventCount', 'unexpectedConsoleErrorCount', 'blockedRequestCount',
                'unhandledRouteCount'):
        if type(value.get(key)) is not int or value[key] != 0:
            raise RuntimeError('Inbox browser safeguards did not pass')


def canonical_digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def run_personal_inbox(fixture, control, agent, *, read_json):
    if not fixture.build_verified or not fixture.failed_run or not fixture.successful_run:
        raise RuntimeError('Inbox acceptance requires independently verified real build results')
    # Only the established Node fixture runner may be selected; do not transform
    # arbitrary shell commands or inherit a caller-selected alternate script.
    if fixture.args.browser_command != ['node', 'tests/actual-backend-acceptance.mjs']:
        raise RuntimeError('Inbox acceptance requires the fixed reviewed browser entrypoint')
    fixture.login()
    actor = fixture.request('/users/fetch')
    actor_id = actor.get('userId')
    if not isinstance(actor_id, str) or not actor_id or actor.get('username') != 'ci-admin':
        raise RuntimeError('Inbox requester identity was not verified')
    failed = {key: fixture.failed_run[key] for key in ('projectId', 'pipelineId', 'runId')}
    successful = {key: fixture.successful_run[key] for key in ('projectId', 'pipelineId', 'runId')}
    failed['status'], successful['status'] = 'failed', 'success'
    def delivered():
        page = fixture.request('/inbox?filter=all&limit=50')
        if (not isinstance(page, dict) or set(page) != {'list', 'nextCursor', 'scanIncomplete'}
                or not isinstance(page['list'], list) or page['nextCursor'] is not None
                or page['scanIncomplete'] is not False):
            raise RuntimeError('Inbox readiness requires a complete bounded primary snapshot')
        matched = []
        for expected in (failed, successful):
            rows = [row for row in page['list'] if isinstance(row, dict)
                    and row.get('kind') == 'run_result' and isinstance(row.get('target'), dict)
                    and row['target'].get('runId') == expected['runId']]
            if len(rows) > 1:
                raise RuntimeError('Inbox projector duplicated a terminal delivery')
            if rows:
                validate_run_item(rows[0], expected, read=False)
            matched.append(bool(rows))
        return all(matched)
    fixture.wait_until('authoritative Inbox run deliveries', delivered, control, timeout=45)
    previous = None
    receipts = []
    for phase in ('read', 'resume'):
        if phase == 'resume':
            fixture.stop_application(control)
            control = fixture.start_control('control-inbox-restart')
            fixture.wait_until('same Agent ready after Inbox Control restart',
                               lambda: fixture.probe('http://127.0.0.1:18081/ready') == 200, agent)
        output = fixture.args.evidence_dir / ('inbox-' + phase)
        output.mkdir(mode=0o700, parents=True, exist_ok=True)
        contract = {'phase': phase, 'actorId': actor_id, 'failed': failed, 'successful': successful,
                    'previous': previous}
        # Preflight environment deliberately excludes build-only secret values.
        environment = fixture.browser_environment('preflight') | {
            'ARCENTRA_INBOX_CONTRACT': json.dumps(contract, separators=(',', ':')),
            'UI_TEST_OUTPUT_DIR': str(output),
            'ARCENTRA_ACCEPTANCE_EVIDENCE_DIR': str(output),
        }
        browser = fixture.start('browser-inbox-' + phase,
                                ['node', 'tests/actual-inbox-acceptance.mjs'],
                                fixture.args.browser_cwd, environment)
        fixture.wait_until('Inbox browser ' + phase, lambda: browser.poll() is not None, timeout=120)
        if browser.returncode != 0 or control.poll() is not None or agent.poll() is not None:
            raise RuntimeError('Inbox actual browser or application exited unsuccessfully')
        require_browser_summary(read_json(output / 'summary.json'))
        receipt = validate_receipt(read_json(output / 'inbox-receipt.json'), phase,
                                   actor_id, failed, successful, previous)
        # Login owns a separate phase after the browser closes: it cannot replace
        # its live single-session token. This is a primary authorized API read.
        fixture.login()
        validate_primary_snapshot(fixture.request('/inbox?filter=all&limit=50'), receipt)
        if phase == 'read':
            fixture.require_observer_session()
            for item in (receipt['readEntry'], receipt['unreadEntry']):
                fixture.expect_rejected('/inbox/' + item['id'] + '/read', 404, 404, {}, 'POST')
            fixture.login()
        receipts.append(receipt)
        previous = receipt
        fixture.note('Real Inbox ' + phase + ' verified exact run outcomes and persisted read state')
    result = {'journey': 'personal-inbox-v1', 'status': 'passed',
              'actualBrowserPhases': ['read', 'resume'], 'realControlRestart': True,
              'exactDeliveryAndReadStatePreserved': True, 'foreignRecipientReadDenied': True,
              'receiptDigests': [canonical_digest(receipt) for receipt in receipts],
              'approvalBrowserFlowVerified': False, 'externalNotificationDeliveryVerified': False}
    # Final artifact is emitted by caller only after its complete cleanup succeeds.
    fixture.inbox_evidence = result
    return control
