import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import prepare


class PrepareTests(unittest.TestCase):
    def test_initialize_instructions_catalog_and_zero_task_accounting(self):
        methods = []
        instructions = 'Use native discovery. ' * 900

        def exchange(endpoint, raw, timeout):
            request = json.loads(raw)
            methods.append(request['method'])
            if request['method'] == 'notifications/initialized':
                self.assertNotIn('id', request)
                return b'', 202, {}
            result = ({'protocolVersion': prepare.PROTOCOL, 'instructions': instructions}
                      if request['method'] == 'initialize' else
                      {'tools': [{'name': 'read_source', 'inputSchema': {'type': 'object'}}]})
            return json.dumps({'jsonrpc': '2.0', 'id': request['id'], 'result': result}).encode(), 200, {}

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root/'prompt').write_text('task')
            run = root/'run'
            report = prepare.prepare(root/'setup', run, 'http://127.0.0.1:1/mcp', root/'prompt', exchange)
            self.assertTrue(report['ready'])
            self.assertEqual(methods, ['initialize', 'notifications/initialized', 'tools/list'])
            state = json.loads((run/'state.json').read_text())
            self.assertEqual((state['calls'], state['response_bytes'], state['started_monotonic']), (0, 0, None))
            self.assertFalse((run/'transcript.jsonl').exists())
            # Read every instruction page through the actual solver command.
            parts, offset = [], 0
            while True:
                output = subprocess.check_output([sys.executable, str(Path(prepare.__file__).with_name('browse.py')),
                            '--run-dir', str(run), '--pointer', '/instructions', '--offset', str(offset), 'instructions'])
                self.assertLessEqual(len(output), 8192)
                view = json.loads(output)
                parts.append(view['data'])
                if view['next_offset'] is None:
                    break
                offset = view['next_offset']
            self.assertEqual(json.loads(''.join(parts)), instructions)
            self.assertEqual(state, json.loads((run/'state.json').read_text()))
            with self.assertRaisesRegex(ValueError, 'recovery/reset'):
                prepare.prepare(root/'second', run, 'http://127.0.0.1:1/mcp', root/'prompt', exchange)
            self.assertEqual(len(methods), 3)

    def test_transport_failure_is_setup_failure_not_a_scored_attempt(self):
        def denied(*args):
            raise PermissionError('loopback blocked')

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root/'prompt').write_text('task')
            with self.assertRaises(PermissionError):
                prepare.prepare(root/'setup', root/'run', 'http://127.0.0.1:1/mcp', root/'prompt', denied)
            report = json.loads((root/'setup/result.json').read_text())
            self.assertFalse(report['ready'])
            self.assertIn('PermissionError', report['error'])
            self.assertTrue((root/'setup/initialize.request.json').exists())
            self.assertFalse((root/'run').exists())

    def test_invalid_native_setup_never_creates_task(self):
        variants = ['version', 'instructions', 'rpc_error', 'rpc_id', 'session', 'oversized',
                    'notification', 'pagination', 'duplicate_tool', 'missing_schema', 'invalid_tool', 'invalid_envelope']
        for variant in variants:
            with self.subTest(variant=variant), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                (root/'prompt').write_text('task')

                def exchange(endpoint, raw, timeout):
                    req = json.loads(raw)
                    if req['method'] == 'notifications/initialized':
                        return b'', 500 if variant == 'notification' else 202, {}
                    if req['method'] == 'initialize':
                        result = {'protocolVersion': 'other' if variant == 'version' else prepare.PROTOCOL,
                                  'instructions': '' if variant == 'instructions' else 'instructions'}
                    else:
                        result = {'tools': [{'name': 'x', 'inputSchema': {}}]}
                        if variant == 'pagination':
                            result['nextCursor'] = 'next'
                        if variant == 'duplicate_tool':
                            result['tools'] *= 2
                        if variant == 'missing_schema':
                            del result['tools'][0]['inputSchema']
                        if variant == 'invalid_tool':
                            result['tools'] = [None]
                    response = {'jsonrpc': '2.0', 'id': 99 if variant == 'rpc_id' else req['id'], 'result': result}
                    if variant == 'rpc_error':
                        response['error'] = {'code': -32603}
                    wire = json.dumps(response).encode()
                    if variant == 'invalid_envelope':
                        wire = b'[]'
                    if variant == 'oversized':
                        wire = b'x'*(prepare.client.TRANSPORT_CAP+1)
                    return wire, 200, {'Mcp-Session-Id': 'session'} if variant == 'session' else {}

                with self.assertRaises(ValueError):
                    prepare.prepare(root/'setup', root/'run', 'http://127.0.0.1:1/mcp', root/'prompt', exchange)
                self.assertFalse((root/'run').exists())
                self.assertFalse(json.loads((root/'setup/result.json').read_text())['ready'])


if __name__ == '__main__':
    unittest.main()
