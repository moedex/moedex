"""Synthetic controller checks; these are not scored product comparisons."""
import base64
import json
from pathlib import Path
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
import unittest
from copy import deepcopy

import isolated_solver as solver


class Record:
    def __init__(self):
        self.attempts, self.responses, self.displays = [], [], []
    def begin_call(self, raw):
        self.attempts.append(raw)
        return len(self.attempts)
    def remaining_seconds(self):
        return 30
    def finish_call(self, ordinal, raw, receipt):
        self.responses.append((ordinal, raw, receipt))
    def display(self, raw):
        if len(raw) > 8192:
            raise ValueError('display cap')
        self.displays.append(raw)


class Native:
    timeout = 30
    def __init__(self):
        self.requests = []
    def exchange(self, request):
        self.requests.append(request)
        if request['method'] == 'initialize':
            result = {'serverInfo': {'name': 'synthetic'}}
        elif request['method'] == 'tools/list':
            result = {'tools': [{'name': 'allowed', 'inputSchema': {}},
                                {'name': 'forbidden', 'inputSchema': {}}]}
        elif request['method'] == 'notifications/initialized':
            raw = b''
            return json.dumps(request, separators=(',', ':')).encode(), raw, {
                'transport_complete': True, 'body_bytes_observed': 0, 'status': 202}
        else:
            result = {'content': [{'type': 'text', 'text': 'source example'}]}
        raw = json.dumps({'jsonrpc': '2.0', 'id': request.get('id'), 'result': result}).encode()
        return json.dumps(request, separators=(',', ':')).encode(), raw, {
            'transport_complete': True, 'body_bytes_observed': len(raw), 'status': 200}
    def decode(self, raw, receipt, request_id):
        value = json.loads(raw)
        assert value['id'] == request_id
        return value


class ControllerTests(unittest.TestCase):
    def test_container_has_no_host_mount_or_network(self):
        command = solver.container_command('sha256:' + 'a' * 64)
        self.assertEqual(command[command.index('--network') + 1], 'none')
        self.assertIn('--read-only', command)
        self.assertIn('no-new-privileges', command)
        self.assertNotIn('-v', command)
        self.assertNotIn('--mount', command)
        self.assertNotIn('--env', command)
        with self.assertRaises(ValueError):
            solver.container_command('mutable:latest')

    def test_fixed_provider_and_broker_paths(self):
        message = {'channel': 'provider', 'id': '1', 'method': 'POST', 'path': '/v1/responses',
                   'body_base64': base64.b64encode(b'{"model":"synthetic"}').decode()}
        self.assertEqual(solver.decode_message(message)[1]['model'], 'synthetic')
        for update in ({'path': 'https://other.example/responses'}, {'path': '/v1/files'},
                       {'method': 'GET'}, {'channel': 'shell'}, {'id': '../1'},
                       {'body_base64': '!!'}):
            with self.assertRaises((ValueError, KeyError)):
                solver.decode_message(dict(message, **update))

    def test_native_onboarding_counts_full_catalog_and_restricts_dispatch(self):
        native, record = Native(), Record()
        broker = solver.NativeBroker(native, record, ['allowed'])
        broker.onboard()
        self.assertEqual(len(record.attempts), 3)
        self.assertIn(b'forbidden', record.responses[-1][1])
        response = broker.handle({'id': 4, 'method': 'tools/list'})
        self.assertEqual([t['name'] for t in response['result']['tools']], ['allowed'])
        with self.assertRaises(ValueError):
            broker.handle({'id': 5, 'method': 'tools/call', 'params': {'name': 'forbidden'}})
        self.assertEqual(len(record.attempts), 3)
        broker.handle({'id': 6, 'method': 'tools/call', 'params': {'name': 'allowed', 'arguments': {}}})
        self.assertEqual(len(record.attempts), 4)
        self.assertEqual(len(record.displays), 1)
        with self.assertRaises(ValueError):
            broker.handle({'id': 7, 'method': 'resources/read', 'params': {'uri': 'file:///private/source'}})

    def test_capture_never_overwrites_requests_or_inventory(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'capture'
            capture = solver.Capture(root)
            stem = capture.request('provider', b'first')
            with self.assertRaises(FileExistsError):
                capture.retain(stem + '.request.json', b'revised')
            capture.event({'channel': 'event', 'event': {'type': 'synthetic'}})
            capture.finish(False)
            value = json.loads((root / 'inventory.json').read_text())
            self.assertFalse(value['complete'])
            self.assertEqual((root / (stem + '.request.json')).read_bytes(), b'first')
            with self.assertRaises(FileExistsError):
                solver.Capture(root)

    def test_provider_auth_only_in_memory_response_bytes_and_errors_retained(self):
        observed = {}
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                observed['path'] = self.path
                observed['auth'] = self.headers.get('Authorization')
                observed['raw'] = self.rfile.read(int(self.headers['Content-Length']))
                self.send_response(400)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', '20')
                self.end_headers()
                self.wfile.write(b'{"error":"example"}')
            def log_message(self, *args):
                pass
        server = HTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            raw, receipt = solver.provider_exchange('http://127.0.0.1:%d/v1' % server.server_port,
                                                    'credential-in-memory', b'{"model":"example"}', 1)
        finally:
            server.shutdown()
            server.server_close()
            thread.join(2)
        self.assertEqual(observed['path'], '/v1/responses')
        self.assertEqual(observed['auth'], 'Bearer credential-in-memory')
        self.assertEqual(observed['raw'], b'{"model":"example"}')
        self.assertEqual(receipt['status'], 400)
        self.assertFalse(receipt['transport_complete'])
        self.assertEqual(receipt['body_bytes_observed'], len(raw))
        self.assertNotIn('credential-in-memory', str(receipt))

    def test_probe_cannot_retrieve_or_call_product_tools(self):
        with self.assertRaises(ValueError):
            solver.mock_mcp({'id': 1, 'method': 'tools/call', 'params': {'name': 'corpus_query'}}, ['corpus_query'])
        response = solver.mock_mcp({'id': 2, 'method': 'tools/list'}, ['corpus_query'])
        self.assertEqual(response['result']['tools'][0]['name'], 'corpus_query')

    def test_bounded_full_envelope_keeps_truncation_and_native_error(self):
        value = {'jsonrpc': '2.0', 'id': 7, 'result': {'isError': True,
                 'content': [{'type': 'text', 'text': 'escaped"\\snow\u2603' * 3000}]}}
        original = solver.canonical(value)
        bounded = solver.bounded_response(value, 8192)
        self.assertLessEqual(len(solver.canonical(bounded)), 8192)
        self.assertTrue(bounded['result']['isError'])
        payload = json.loads(bounded['result']['content'][0]['text'])
        self.assertTrue(payload['truncated_display'])
        self.assertEqual(payload['native_envelope_bytes'], len(original))
        self.assertTrue(original.decode().startswith(payload['prefix']))
        self.assertEqual(solver.bounded_response({'id': 7, 'result': {}}, 8192), {'id': 7, 'result': {}})
        error = solver.bounded_response({'id': 8, 'error': {'code': -1, 'message': 'x' * 9000}}, 8192)
        self.assertTrue(error['result']['isError'])

    def test_execution_binds_exact_contract_task_and_all_budgets(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'prompt.txt').write_bytes(b'fixture prompt\n')
            from run_record import reference
            budgets = {'calls': 24, 'response_bytes': 262144, 'display_bytes': 8192, 'assignment_seconds': 600}
            prompt_ref = reference(root, 'prompt.txt')
            contract = {'schema': 'native-pair-v1', 'tasks': [{'id': 'example', 'prompt': prompt_ref}], 'budgets': budgets}
            (root / 'contract.json').write_bytes(solver.canonical(contract))
            settings = {'model': 'synthetic', 'reasoning': {'effort': 'high'}, 'tool_choice': 'auto',
                        'parallel_tool_calls': False, 'text': {'verbosity': 'low'},
                        'store': False, 'stream': True, 'include': ['reasoning.encrypted_content']}
            frozen = {'contract': reference(root, 'contract.json'), 'arm': 'a',
                      'isolation': {'image_sha256': 'sha256:' + 'a' * 64},
                      'model': {'requested_alias': 'synthetic', 'settings': {'reasoning_effort': 'high', 'reasoning_summary': 'none', 'provider_fields': settings}},
                      'native_allowed_tools': ['allowed'],
                      'runners': {n: solver.digest(Path(solver.__file__).with_name(n).read_bytes())
                                  for n in ('isolated_solver.py', 'run_record.py', 'native_http.py', 'journey_clock.py')}}
            (root / 'freeze.json').write_bytes(solver.canonical(frozen))
            config = {'freeze': reference(root, 'freeze.json'), 'identity': {
                'task': 'example', 'arm': 'a', 'contract_sha256': solver.digest(solver.canonical(contract)),
                'prompt_sha256': prompt_ref['sha256']}, 'model': 'synthetic', 'reasoning_effort': 'high',
                'image': frozen['isolation']['image_sha256'], 'prompt': 'fixture prompt\n',
                'budgets': budgets, 'enabled_tools': ['allowed'], 'timeout_seconds': 600}
            solver.validate_execution_config(root, config)
            for key in ('calls', 'response_bytes', 'display_bytes', 'assignment_seconds'):
                changed = deepcopy(config)
                changed['budgets'][key] += 1
                with self.assertRaises(ValueError):
                    solver.validate_execution_config(root, changed)

    def test_provider_extra_settings_and_endpoint_substitution_rejected(self):
        value = {'model': 'synthetic', 'reasoning': {'effort': 'high'}, 'store': False,
                 'input': [], 'prompt_cache_key': 'session'}
        config = {'model': 'synthetic', 'reasoning_effort': 'high',
                  'provider_fields': {'model': 'synthetic', 'reasoning': {'effort': 'high'}, 'store': False}}
        solver.validate_provider_request(config, value)
        for key in ('temperature', 'top_p', 'max_output_tokens', 'previous_response_id', 'instructions'):
            with self.assertRaises(ValueError):
                solver.validate_provider_request(config, dict(value, **{key: 1}))
        config.update(native_url_env='NATIVE_FIXTURE', provider_url_env='PROVIDER_FIXTURE')
        env = {'NATIVE_FIXTURE': 'https://native.example/mcp', 'PROVIDER_FIXTURE': 'https://provider.example/v1'}
        frozen = {'product': {'endpoint_sha256': solver.digest(env['NATIVE_FIXTURE'].encode())},
                  'model': {'provider_base_url_sha256': solver.digest(env['PROVIDER_FIXTURE'].encode())}}
        solver.validate_environment_bindings(frozen, config, env)
        with self.assertRaises(ValueError):
            solver.validate_environment_bindings(frozen, config, dict(env, NATIVE_FIXTURE='https://changed.example/mcp'))
            for key in ('contract_sha256', 'prompt_sha256', 'task', 'arm'):
                changed = deepcopy(config)
                changed['identity'][key] = 'changed'
                with self.assertRaises(ValueError):
                    solver.validate_execution_config(root, changed)

    def test_synthetic_provider_probe_has_only_metadata_code(self):
        raw, receipt = solver.probe_provider_response('synthetic', 1)
        self.assertEqual(receipt['status'], 200)
        self.assertIn(b'ALL_TOOLS.map', raw)
        self.assertNotIn(b'tools.call', raw)
        _, receipt = solver.probe_provider_response('synthetic', 2)
        self.assertEqual(receipt['status'], 400)


if __name__ == '__main__':
    unittest.main()
