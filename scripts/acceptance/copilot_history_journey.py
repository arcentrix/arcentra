"""Real Control history persistence against a local synthetic TLS model only."""
import json
import secrets

from copilot_history_evidence import validate_primary, validate_receipt
from copilot_model_fixture import MODEL, SyntheticHistoryModel
from inbox_journey import canonical_digest, require_browser_summary


def run_private_copilot(fixture, control, agent, *, read_json):
    if not fixture.build_verified or not fixture.failed_run or fixture.args.browser_command != ['node', 'tests/actual-backend-acceptance.mjs']:
        raise RuntimeError('history acceptance requires the fixed verified build journey')
    fixture.login()
    actor = fixture.request('/users/fetch')
    if actor.get('username') != 'ci-admin' or not actor.get('userId'):
        raise RuntimeError('history fixture actor mismatch')
    run = fixture.failed_run
    base = '/pipelines/' + run['pipelineId'] + '/runs/' + run['runId']
    jobs = fixture.request(base + '/jobs')
    if jobs.get('total') != 1 or len(jobs.get('list', [])) != 1:
        raise RuntimeError('history requires one verified failed job')
    steps = [step for step in jobs['list'][0].get('steps', []) if step.get('name') == 'test' and step.get('status') == 5]
    if len(steps) != 1 or not steps[0].get('stepRunId'):
        raise RuntimeError('history requires the actual failed compiler test step')
    model_token = 'synthetic-model-' + secrets.token_urlsafe(24)
    fixture.redactions.append(model_token)
    forbidden = (fixture.secret_initial_value, fixture.secret_rotated_value, fixture.control_secret_key, model_token)
    model = SyntheticHistoryModel(fixture.root, model_token, forbidden, fixture.secret_rotated_value)
    fixture.copilot_model = model
    config = ('\n[copilot]\nenabled = true\nallowedProjectIds = ' + json.dumps([run['projectId']]) + '\n'
              '[[copilot.providers]]\nid = "history-fixture"\nkind = "openai-compatible"\n'
              'baseUrl = "https://127.0.0.1:19443/v1"\napiKeyEnv = "ARCENTRA_TEST_COPILOT_KEY"\n'
              'models = ' + json.dumps([MODEL]) + '\n[copilot.history]\nenabled = true\n')
    config_path = fixture.root / 'control.toml'
    original = config_path.read_text()
    if '[copilot' in original:
        raise RuntimeError('history fixture cannot override an existing provider configuration')
    fixture.write_private('control.toml', original + config)
    fixture.control_extra_env = {'ARCENTRA_TEST_COPILOT_KEY': model_token,
                                 'SSL_CERT_FILE': str(fixture.root / 'ca.pem')}
    fixture.stop_application(control)
    control = fixture.start_control('control-private-history')
    fixture.login()
    previous = None
    receipts = []
    before_runs = fixture.request('/pipelines/' + run['pipelineId'] + '/runs?page=1&pageSize=100')
    before_artifacts = fixture.request('/projects/' + run['projectId'] + '/artifacts?pageSize=100')
    for phase in ('create', 'resume'):
        if phase == 'resume':
            fixture.stop_application(control)
            control = fixture.start_control('control-private-history-restart')
        fixture.wait_until('Agent ready during private history',
                           lambda: fixture.probe('http://127.0.0.1:18081/ready') == 200, agent)
        output = fixture.args.evidence_dir / ('copilot-history-' + phase)
        output.mkdir(mode=0o700, parents=True, exist_ok=True)
        contract = dict(actorId=actor['userId'], projectId=run['projectId'], pipelineId=run['pipelineId'],
                        runId=run['runId'], stepId=steps[0]['stepRunId'], phase=phase, previous=previous)
        env = fixture.browser_environment('preflight') | {
            'ARCENTRA_COPILOT_HISTORY_CONTRACT': json.dumps(contract, separators=(',', ':')),
            'UI_TEST_OUTPUT_DIR': str(output), 'ARCENTRA_ACCEPTANCE_EVIDENCE_DIR': str(output)}
        browser = fixture.start('browser-copilot-history-' + phase,
                                ['node', 'tests/actual-copilot-history-acceptance.mjs'], fixture.args.browser_cwd, env)
        fixture.wait_until('private history browser ' + phase, lambda: browser.poll() is not None, timeout=150)
        if browser.returncode != 0 or control.poll() is not None or agent.poll() is not None:
            raise RuntimeError('private history browser or application failed')
        require_browser_summary(read_json(output / 'summary.json'))
        receipt = validate_receipt(read_json(output / 'copilot-history-receipt.json'), contract)
        fixture.login()
        path = '/projects/' + run['projectId'] + '/copilot/conversations/' + receipt['conversationId']
        validate_primary(fixture.request(path + '?after=0&limit=20'),
                         fixture.request(path + '/plans/' + receipt['plan']['planId']), receipt, forbidden)
        if fixture.request('/pipelines/' + run['pipelineId'] + '/runs?page=1&pageSize=100') != before_runs:
            raise RuntimeError('private review unexpectedly changed pipeline runs')
        if fixture.request('/projects/' + run['projectId'] + '/artifacts?pageSize=100') != before_artifacts:
            raise RuntimeError('private review unexpectedly changed artifacts')
        fixture.login_observer()
        fixture.expect_rejected(path, 404, 404)
        fixture.expect_rejected(path + '/plans/' + receipt['plan']['planId'], 404, 404)
        fixture.login()
        receipts.append(receipt)
        previous = receipt
        fixture.note('Real private Copilot ' + phase + ' verified encrypted-history API identity and inert reviewed plan')
    provider = model.evidence()
    if provider.get("userPromptDigests") != receipts[0]["previewDigests"]:
        raise RuntimeError("provider received different bytes from the two approved previews")
    model.close()
    fixture.copilot_model = None
    fixture.copilot_evidence = {'journey': 'copilot-private-history-v1', 'status': 'passed',
        'actualBrowserPhases': ['create', 'resume'], 'realControlRestart': True,
        'syntheticModelListenerStopped': True,
        'privateOwnerAccessVerified': True, 'primaryAPIHashesVerified': True,
        'executionEnabled': False, 'realExternalProviderVerified': False,
        'provider': provider, 'receiptDigests': [canonical_digest(receipt) for receipt in receipts]}
    return control
