"""Bounded loopback TLS text-only model for disposable history acceptance.

Only hashes and fixed verdicts leave this process. Request bodies, headers,
synthetic credentials and selected logs are never logged or saved.
"""
import hashlib
import hmac
import http.server
import json
import ssl
import threading

MODEL = 'synthetic-history-model'
MAX_BODY = 65536


def validate_request(body, forbidden_values):
    if not isinstance(body, dict) or set(body) != {'model', 'messages', 'max_completion_tokens', 'stream', 'store'}:
        raise ValueError('unexpected model request fields')
    if body['model'] != MODEL or body['stream'] is not False or body['store'] is not False:
        raise ValueError('model request storage or streaming contract changed')
    if type(body['max_completion_tokens']) is not int or not 1 <= body['max_completion_tokens'] <= 4096:
        raise ValueError('model token bound invalid')
    messages = body['messages']
    if not isinstance(messages, list) or len(messages) != 2:
        raise ValueError('model message roles invalid')
    for message, role in zip(messages, ('system', 'user')):
        if not isinstance(message, dict) or set(message) != {'role', 'content'} or message['role'] != role:
            raise ValueError('model message envelope invalid')
        if not isinstance(message['content'], str) or not message['content']:
            raise ValueError('model content missing')
    content = messages[0]['content'] + messages[1]['content']
    if len(content.encode()) > 32768 or any(value and value in content for value in forbidden_values):
        raise ValueError('model content confidentiality or size check failed')
    return hashlib.sha256(json.dumps(body, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


class SyntheticHistoryModel:
    def __init__(self, root, token, forbidden_values, response_canary):
        self.token = token
        self.forbidden_values = tuple(forbidden_values)
        self.response_canary = response_canary
        self.digests = []
        self.user_digests = []
        self.failures = 0
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = 'HTTP/1.0'
            def log_message(self, *_args):
                pass
            def do_POST(self):
                try:
                    self.connection.settimeout(5)
                    if self.path != '/v1/chat/completions' or not hmac.compare_digest(
                            self.headers.get('Authorization', ''), 'Bearer ' + owner.token):
                        raise ValueError('model route or authentication invalid')
                    if self.headers.get('Transfer-Encoding') or self.headers.get('Content-Type') != 'application/json':
                        raise ValueError('model content framing invalid')
                    length = int(self.headers.get('Content-Length', '0'))
                    if not 0 < length <= MAX_BODY or len(owner.digests) >= 2:
                        raise ValueError('model request bound exceeded')
                    data = self.rfile.read(length)
                    if len(data) != length:
                        raise ValueError('model request incomplete')
                    body = json.loads(data)
                    digest = validate_request(body, owner.forbidden_values)
                    owner.digests.append(digest)
                    owner.user_digests.append(hashlib.sha256(body["messages"][1]["content"].encode()).hexdigest())
                    answer = ('SYNTHETIC_HISTORY_REPLY_' + str(len(owner.digests)) + '\n'
                              'Untrusted model suggestion: retry or deploy requires explicit application controls.\n'
                              'Redaction canary: ' + owner.response_canary)
                    payload = json.dumps({'choices': [{'message': {'role': 'assistant', 'content': answer},
                                                       'finish_reason': 'stop'}]}).encode()
                    self.send_response(200)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Content-Length', str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)
                except Exception:
                    owner.failures += 1
                    self.send_error(400, 'Synthetic model contract rejected')
            def do_GET(self):
                owner.failures += 1
                self.send_error(405, 'POST required')

        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        context.load_cert_chain(root / 'server.pem', root / 'server.key')
        class BoundedTLSHTTPServer(http.server.HTTPServer):
            def get_request(self):
                raw, address = self.socket.accept()
                raw.settimeout(5)
                try:
                    return context.wrap_socket(raw, server_side=True), address
                except Exception:
                    raw.close()
                    raise
        self.server = BoundedTLSHTTPServer(('127.0.0.1', 19443), Handler)
        self.server.timeout = 5
        self.thread = threading.Thread(target=self.server.serve_forever,
                                       kwargs={'poll_interval': 0.2}, name='synthetic-history-model')
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=6)
        if self.thread.is_alive():
            raise RuntimeError('synthetic model did not stop')

    def evidence(self):
        if self.failures or len(self.digests) != 2 or len(set(self.digests)) != 2:
            raise RuntimeError('synthetic model exact request evidence incomplete')
        return {'provider': 'local-tls-synthetic', 'requests': 2, 'failures': 0,
                'requestDigests': list(self.digests), 'userPromptDigests': list(self.user_digests), 'rawPromptRetained': False}
