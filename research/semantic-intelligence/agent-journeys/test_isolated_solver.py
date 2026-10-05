"""Synthetic controller checks; these are not scored product comparisons."""
import base64
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
import unittest
from copy import deepcopy
from unittest.mock import patch

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
    def stop(self, reason):
        self.stop_reason = reason


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
    @unittest.skipUnless(shutil.which('node'), 'Node is required to check container CLI configuration')
    def test_container_approves_only_frozen_tools_and_rejects_unsafe_names(self):
        script = """
const bridge = require(process.argv[1]);
const names = ['allowed', 'second_tool'];
const args = bridge.configArgs({reasoning_effort: 'high', enabled_tools: names}, 1234, 5678);
const pairs = Object.fromEntries(args.flatMap((v, i) => v === '-c' ? [args[i + 1].split(/=(.*)/s).slice(0, 2)] : []));
const bad = [[], ['repeat', 'repeat'], ['good', 'bad.name'], ['bad\"name'], ['bad name'], ['bad\\nname'], [17]];
const rejected = bad.map(enabled_tools => {
  try { bridge.configArgs({enabled_tools}, 1234, 5678); return false; } catch { return true; }
});
console.log(JSON.stringify({pairs, rejected, args}));
"""
        result = subprocess.run(['node', '-e', script, str(Path(solver.__file__).with_name('container_solver.cjs'))],
                                check=True, capture_output=True, text=True)
        value = json.loads(result.stdout)
        approvals = {key: json.loads(item) for key, item in value['pairs'].items() if key.endswith('.approval_mode')}
        self.assertEqual(approvals, {'mcp_servers.retrieval.tools.allowed.approval_mode': 'approve',
                                    'mcp_servers.retrieval.tools.second_tool.approval_mode': 'approve'})
        self.assertEqual(json.loads(value['pairs']['approval_policy']), 'never')
        self.assertEqual(json.loads(value['pairs']['mcp_servers.retrieval.enabled_tools']), ['allowed', 'second_tool'])
        self.assertTrue(all(value['rejected']))
        self.assertIn('shell_tool', value['args'])

    def test_onboarding_failure_stops_assignment(self):
        record = Record()
        with tempfile.TemporaryDirectory() as tmp:
            config = {'evidence_root': tmp, 'assignment': 'example', 'identity': {}, 'budgets': {'display_bytes': 8192},
                      'freeze': {}, 'coordinator_session': 'coordinator', 'enabled_tools': ['allowed'],
                      'native_url_env': 'FIXTURE_NATIVE_URL'}
            config_path = Path(tmp) / 'config.json'
            config_path.write_text(json.dumps(config))
            with patch('sys.argv', ['isolated_solver.py', '--config', str(config_path), '--capture', str(Path(tmp) / 'capture')]), \
                    patch.object(solver, 'validate_execution_config', return_value={}), \
                    patch.object(solver, 'validate_environment_bindings'), \
                    patch.object(solver.RunRecord, 'create', return_value=record), \
                    patch.object(solver, 'NativeHTTP'), \
                    patch.object(solver, 'NativeBroker') as broker_class, \
                    patch.dict(os.environ, {'FIXTURE_NATIVE_URL': 'http://127.0.0.1/mcp'}):
                broker_class.return_value.onboard.side_effect = ValueError('native identity drift')
                with self.assertRaisesRegex(ValueError, 'native identity drift'):
                    solver.main()
        self.assertEqual(record.stop_reason, 'ValueError')

    def test_observed_native_identity_checks_full_catalog_after_capture(self):
        catalog = {'tools': [{'name': 'allowed', 'inputSchema': {}},
                             {'name': 'forbidden', 'inputSchema': {}}]}
        identity = {'server_info': {'name': 'synthetic'},
                    'catalog_sha256': solver.digest(solver.canonical(catalog))}
        broker = solver.NativeBroker(Native(), Record(), ['allowed'], identity)
        broker.onboard()
        for changed in (dict(identity, server_info={'name': 'changed'}),
                        dict(identity, catalog_sha256=solver.digest(solver.canonical(
                            {'tools': catalog['tools'][:1]})))):
            record = Record()
            broker = solver.NativeBroker(Native(), record, ['allowed'], changed)
            with self.assertRaisesRegex(ValueError, 'native identity'):
                broker.onboard()
            self.assertEqual(len(record.responses), 3)
            self.assertIn(b'forbidden', record.responses[-1][1])
            self.assertIsNone(broker.catalog)

    def test_observed_provider_requires_completed_identity_and_checks_every_event(self):
        receipt = {'status': 200, 'content_type': 'text/event-stream', 'transport_complete': True}
        def body(events):
            return b''.join(b'data: ' + solver.canonical(event) + b'\n\n' for event in events)
        created = {'type': 'response.created', 'response': {'status': 'in_progress', 'model': 'frozen'}}
        completed = {'type': 'response.completed', 'response': {'status': 'completed', 'model': 'frozen'}}
        solver.validate_provider_identity(body([created, completed]), receipt, ['frozen'])
        solver.validate_provider_identity(solver.canonical(completed['response']),
                                          dict(receipt, content_type='application/json'), ['frozen'])
        for events in ([dict(created, response={'model': 'drifted'}), completed],
                       [created],
                       [created, dict(completed, response={'status': 'completed'})],
                       [created, dict(completed, response={'status': 'failed', 'model': 'frozen'})],
                       [dict(created, response={'model': 17}), completed]):
            with self.assertRaises(ValueError):
                solver.validate_provider_identity(body(events), receipt, ['frozen'])
        with self.assertRaises(ValueError):
            solver.validate_provider_identity(b'not JSON', dict(receipt, content_type='application/json'), ['frozen'])
        solver.validate_provider_identity(b'{"error":{"message":"retry"}}',
                                          dict(receipt, status=429, content_type='application/json'), ['frozen'])
        with self.assertRaisesRegex(ValueError, 'differs'):
            solver.validate_provider_identity(b'{"model":"drifted","status":"failed"}',
                                              dict(receipt, status=400, content_type='application/json'), ['frozen'])
        with self.assertRaisesRegex(ValueError, 'differs'):
            solver.validate_provider_identity(body([dict(created, response={'model': 'drifted'})]) + b'data: broken\n\n',
                                              dict(receipt, status=400), ['frozen'])

    def test_provider_drift_raw_capture_precedes_rejection_and_no_reply(self):
        class Process:
            def __init__(self, message):
                read_fd, write_fd = os.pipe()
                os.write(write_fd, solver.canonical(message) + b'\n')
                os.close(write_fd)
                self.stdout = os.fdopen(read_fd, 'rb')
                self.stdin = io.BytesIO()
                self.code = None
            def poll(self):
                return self.code
            def terminate(self):
                self.code = -15
            def wait(self, timeout):
                return self.code
        request = {'model': 'frozen', 'reasoning': {'effort': 'high'}}
        message = {'channel': 'provider', 'id': '1', 'method': 'POST', 'path': '/v1/responses',
                   'body_base64': base64.b64encode(solver.canonical(request)).decode()}
        process = Process(message)
        raw = b'{"model":"drifted","status":"completed"}'
        receipt = {'status': 200, 'content_type': 'application/json',
                   'transport_complete': True, 'body_bytes_observed': len(raw)}
        config = {'image': 'sha256:' + 'a' * 64, 'model': 'frozen', 'reasoning_effort': 'high',
                  'prompt': 'metadata probe', 'enabled_tools': ['allowed'], 'timeout_seconds': 30,
                  'provider_url_env': 'FIXTURE_URL', 'provider_token_env': 'FIXTURE_TOKEN',
                  '_observed_identities': {'model': {'returned_models': ['frozen']}}}
        record = Record()
        try:
            with tempfile.TemporaryDirectory() as tmp, \
                    patch.object(solver.subprocess, 'Popen', return_value=process), \
                    patch.object(solver, 'provider_exchange', return_value=(raw, receipt)), \
                    patch.dict(os.environ, {'FIXTURE_URL': 'https://example.invalid', 'FIXTURE_TOKEN': 'private'}):
                root = Path(tmp) / 'capture'
                with self.assertRaisesRegex(ValueError, 'differs'):
                    solver.run_container(config, root, record=record)
                self.assertEqual((root / 'provider-0001.response.raw').read_bytes(), raw)
                self.assertEqual(json.loads((root / 'provider-0001.receipt.json').read_text()), receipt)
                self.assertFalse(json.loads((root / 'inventory.json').read_text())['complete'])
                self.assertEqual(record.stop_reason, 'ValueError')
                self.assertEqual(len(process.stdin.getvalue().splitlines()), 1)
        finally:
            process.stdout.close()

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
            (root / 'manifest.json').write_bytes(b'{"files":[]}')
            contract = {'schema': 'native-pair-v1',
                        'corpus': {'repository': 'https://example.test/api', 'commit': 'a' * 40,
                                   'manifest': reference(root, 'manifest.json')},
                        'rubric': reference(root, 'manifest.json'), 'protocol': reference(root, 'manifest.json'),
                        'tasks': [{'id': 'example', 'prompt': prompt_ref, 'atoms': ['example.1']}],
                        'budgets': budgets}
            (root / 'contract.json').write_bytes(solver.canonical(contract))
            settings = {'model': 'synthetic', 'reasoning': {'effort': 'high'}, 'tool_choice': 'auto',
                        'parallel_tool_calls': False, 'text': {'verbosity': 'low'},
                        'store': False, 'stream': True, 'include': ['reasoning.encrypted_content']}
            frozen = {'contract': reference(root, 'contract.json'), 'arm': 'a',
                      'isolation': {'image_sha256': 'sha256:' + 'a' * 64},
                      'model': {'requested_alias': 'synthetic', 'settings': {'reasoning_effort': 'high', 'reasoning_summary': 'none', 'provider_fields': settings}},
                      'native_allowed_tools': ['allowed'],
                      'runners': {n: solver.digest(Path(solver.__file__).with_name(n).read_bytes())
                                  for n in ('isolated_solver.py', 'run_record.py', 'native_http.py', 'journey_clock.py',
                                            'contract.py', 'compare.py')}}
            (root / 'freeze.json').write_bytes(solver.canonical(frozen))
            config = {'freeze': reference(root, 'freeze.json'), 'identity': {
                'task': 'example', 'arm': 'a', 'contract_sha256': solver.digest(solver.canonical(contract)),
                'prompt_sha256': prompt_ref['sha256']}, 'model': 'synthetic', 'reasoning_effort': 'high',
                'image': frozen['isolation']['image_sha256'], 'prompt': 'fixture prompt\n',
                'budgets': budgets, 'enabled_tools': ['allowed'], 'timeout_seconds': 600}
            solver.validate_execution_config(root, config)
            config['_observed_identities'] = {'model': {'returned_models': ['forged']}}
            solver.validate_execution_config(root, config)
            self.assertNotIn('_observed_identities', config)
            for key in ('calls', 'response_bytes', 'display_bytes', 'assignment_seconds'):
                changed = deepcopy(config)
                changed['budgets'][key] += 1
                with self.assertRaises(ValueError):
                    solver.validate_execution_config(root, changed)

            def retain(name, value):
                (root / name).write_bytes(solver.canonical(value))
                return reference(root, name)
            limits = ['Service and model revisions cannot be reproduced exactly.']
            frozen['provenance'] = {'mode': 'observed-service-v1', 'reproducibility_limits': limits,
                'authorization': retain('authorization.json', {'schema': 'observed-authorization-v1',
                    'mode': 'observed-service-v1', 'authorized_by': 'user', 'decision': 'Use observed service identities.',
                    'requirements': ['isolation', 'budgets', 'raw-capture', 'independent-scoring'],
                    'reproducibility_limits': limits})}
            native_exchanges = []
            native = Native()
            for ordinal, request in enumerate(({'jsonrpc': '2.0', 'id': 1, 'method': 'initialize'},
                                               {'jsonrpc': '2.0', 'method': 'notifications/initialized'},
                                               {'jsonrpc': '2.0', 'id': 3, 'method': 'tools/list'}), 1):
                _, body, receipt = native.exchange(request)
                response_name = 'native-%d.response.raw' % ordinal
                (root / response_name).write_bytes(body)
                native_exchanges.append({'request': retain('native-%d.request.json' % ordinal, request),
                                         'response': reference(root, response_name),
                                         'receipt': retain('native-%d.receipt.json' % ordinal, receipt)})
            frozen['product'] = {'endpoint_sha256': 'a' * 64,
                'observation': retain('product-observation.json', {'schema': 'observed-product-v1',
                    'observed_utc': '2026-10-05T00:00:00Z', 'endpoint_sha256': 'a' * 64,
                    'server_info': {'name': 'synthetic'},
                    'catalog_sha256': solver.digest(solver.canonical(json.loads(body)['result'])),
                    'native_exchanges': native_exchanges, 'immutable_serving_identity_verified': False,
                    'runtime_dependency_closure_verified': False})}
            frozen['model']['provider_base_url_sha256'] = 'b' * 64
            model_response = {'model': 'synthetic', 'status': 'completed'}
            frozen['model']['observation'] = retain('model-observation.json', {
                'schema': 'observed-model-v1', 'observed_utc': '2026-10-05T00:00:00Z',
                'requested_alias': 'synthetic', 'provider_base_url_sha256': 'b' * 64,
                'settings_sha256': solver.digest(solver.canonical(frozen['model']['settings'])),
                'returned_models': ['synthetic'], 'immutable_revision_verified': False,
                'request': retain('model-request.json', dict(settings, input=[])),
                'response': retain('model-response.json', model_response),
                'receipt': retain('model-receipt.json', {'status': 200, 'transport_complete': True,
                    'body_bytes_observed': len(solver.canonical(model_response))})})
            frozen['runners']['provenance.py'] = solver.digest(Path(solver.__file__).with_name('provenance.py').read_bytes())
            (root / 'freeze.json').write_bytes(solver.canonical(frozen))
            config['freeze'] = reference(root, 'freeze.json')
            config['_observed_identities'] = {'model': {'returned_models': ['forged']}}
            solver.validate_execution_config(root, config)
            self.assertEqual(config['_observed_identities']['model']['returned_models'], ['synthetic'])
            frozen['runners']['provenance.py'] = '0' * 64
            (root / 'freeze.json').write_bytes(solver.canonical(frozen))
            config['freeze'] = reference(root, 'freeze.json')
            with self.assertRaisesRegex(ValueError, 'executing runner differs'):
                solver.validate_execution_config(root, config)
            (root / 'model-response.json').write_bytes(b'{"model":"forged","status":"completed"}')
            with self.assertRaisesRegex(ValueError, 'observed provenance is invalid'):
                solver.validate_execution_config(root, config)

    def test_v2_execution_checks_all_sources_task_scope_and_frozen_validator(self):
        from run_record import reference
        from test_contract import fixture
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            contract = fixture(root)
            settings = {'model': 'synthetic', 'reasoning': {'effort': 'high'}, 'tool_choice': 'auto',
                        'parallel_tool_calls': False, 'text': {'verbosity': 'low'},
                        'store': False, 'stream': True, 'include': ['reasoning.encrypted_content']}
            frozen = {'arm': 'a', 'isolation': {'image_sha256': 'sha256:' + 'a' * 64},
                      'model': {'requested_alias': 'synthetic', 'settings': {'reasoning_effort': 'high',
                                'reasoning_summary': 'none', 'provider_fields': settings}},
                      'native_allowed_tools': ['allowed'],
                      'runners': {name: solver.digest(Path(solver.__file__).with_name(name).read_bytes())
                                  for name in ('isolated_solver.py', 'run_record.py', 'native_http.py',
                                               'journey_clock.py', 'contract.py', 'compare.py')}}
            config = {'identity': {'task': 'example', 'arm': 'a',
                                  'contract_sha256': solver.digest(solver.canonical(contract)),
                                  'prompt_sha256': contract['tasks'][0]['prompt']['sha256']},
                      'model': 'synthetic', 'reasoning_effort': 'high',
                      'image': frozen['isolation']['image_sha256'], 'prompt': 'fixture prompt\n',
                      'budgets': contract['budgets'], 'enabled_tools': ['allowed'], 'timeout_seconds': 600}
            def freeze():
                (root / 'contract.json').write_bytes(solver.canonical(contract))
                frozen['contract'] = reference(root, 'contract.json')
                (root / 'freeze.json').write_bytes(solver.canonical(frozen))
                config['freeze'] = reference(root, 'freeze.json')
            freeze()
            solver.validate_execution_config(root, config)
            original = deepcopy(contract)
            for scope in ([], ['https://example.test/unknown']):
                contract['tasks'][0]['repositories'] = scope
                freeze()
                with self.assertRaisesRegex(ValueError, 'task repository scope'):
                    solver.validate_execution_config(root, config)
            contract = deepcopy(original)
            contract['tasks'][0]['repositories'].pop()
            freeze()
            with self.assertRaisesRegex(ValueError, 'execution configuration differs'):
                solver.validate_execution_config(root, config)
            contract = deepcopy(original)
            contract['corpus']['repositories'][1]['commit'] = 'c' * 40
            freeze()
            with self.assertRaisesRegex(ValueError, 'execution configuration differs'):
                solver.validate_execution_config(root, config)
            contract = deepcopy(original)
            freeze()
            manifest = root / contract['corpus']['repositories'][1]['manifest']['path']
            raw = manifest.read_bytes()
            manifest.write_bytes(raw + b' ')
            with self.assertRaisesRegex(ValueError, 'reference digest mismatch'):
                solver.validate_execution_config(root, config)
            manifest.write_bytes(raw)
            for name in ('contract.py', 'compare.py'):
                expected = frozen['runners'][name]
                for wrong in (None, '0' * 64):
                    frozen['runners'][name] = wrong
                    freeze()
                    with self.subTest(name=name, wrong=wrong), self.assertRaisesRegex(ValueError, 'executing runner differs'):
                        solver.validate_execution_config(root, config)
                frozen['runners'][name] = expected
            freeze()
            solver.validate_execution_config(root, config)

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
