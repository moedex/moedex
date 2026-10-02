import concurrent.futures
import importlib.util
import json
from pathlib import Path
import tempfile
import time
import unittest

spec = importlib.util.spec_from_file_location('journey_client', Path(__file__).with_name('client.py'))
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)


class ClientTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        (root/'prompt').write_text('Find the implementation using public tools.')
        (root/'catalog').write_text('{"result":{"tools":[]}}')
        self.run = root/'run'
        client.initialize(self.run, 'http://127.0.0.1:19381/mcp', root/'prompt', root/'catalog')

    def state(self):
        return json.loads((self.run/'state.json').read_text())

    def change(self, **values):
        state = self.state()
        state.update(values)
        client.atomic_json(self.run/'state.json', state)

    def events(self):
        return [json.loads(line) for line in (self.run/'transcript.jsonl').read_text().splitlines()]

    def test_verbatim_and_first_interaction_clock(self):
        self.assertIsNone(self.state()['started_monotonic'])
        body = b'{ "jsonrpc":"2.0", "result":{"unicode":"\xc3\xa9"} }\n'
        def exchange(endpoint, wire, timeout):
            self.assertEqual(json.loads(wire)['method'], 'tools/list')
            self.assertEqual(timeout, 600)
            return body, 200, {'Content-Type': 'application/json'}
        raw, event = client.request(self.run, 'tools/list', {}, exchange=exchange, now=lambda: 100)
        self.assertEqual(raw, body)
        self.assertEqual((self.run/'001.response.raw').read_bytes(), body)
        self.assertEqual(event['serialized_response_bytes'], len(body))
        self.assertEqual(self.state()['started_monotonic'], 100)
        self.assertEqual(self.state()['calls'], 1)
        self.assertEqual(event['token_usage'], 'unknown')

    def test_full_crossing_response_then_block(self):
        self.change(response_bytes=client.MAX_RESPONSE_BYTES-2)
        body = b'{"result":"crosses budget"}'
        raw, event = client.request(self.run, 'tools/call', {'name': 'unknown', 'arguments': {}},
                                    exchange=lambda *args: (body, 200, {}))
        self.assertEqual(raw, body)
        self.assertTrue(event['response_complete'])
        self.assertGreater(self.state()['response_bytes'], client.MAX_RESPONSE_BYTES)
        with self.assertRaises(client.Stopped):
            client.request(self.run, 'tools/list', {}, exchange=lambda *args: self.fail('sent after exhaustion'))
        self.assertEqual(self.state()['calls'], 1)

    def test_atomic_last_call(self):
        self.change(calls=23)
        sent = []
        def exchange(*args):
            sent.append(True)
            time.sleep(.03)
            return b'{}', 200, {}
        def invoke():
            try:
                client.request(self.run, 'tools/list', {}, exchange=exchange)
                return 'ok'
            except client.Stopped:
                return 'stopped'
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(lambda _: invoke(), range(2)))
        self.assertCountEqual(results, ['ok', 'stopped'])
        self.assertEqual(len(sent), 1)
        self.assertEqual(self.state()['calls'], 24)

    def test_atomic_byte_budget(self):
        self.change(response_bytes=client.MAX_RESPONSE_BYTES-1)
        def invoke():
            try:
                client.request(self.run, 'tools/list', {}, exchange=lambda *args: (b'{}', 200, {}))
                return 'ok'
            except client.Stopped:
                return 'stopped'
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(lambda _: invoke(), range(2)))
        self.assertCountEqual(results, ['ok', 'stopped'])
        self.assertEqual(self.state()['calls'], 1)

    def test_wall_budget_includes_between_interactions(self):
        client.request(self.run, 'tools/list', {}, exchange=lambda *args: (b'{}', 200, {}), now=lambda: 100)
        with self.assertRaises(client.Stopped):
            client.request(self.run, 'tools/list', {}, exchange=lambda *args: self.fail('late call'), now=lambda: 700)
        self.assertEqual(self.state()['calls'], 1)

    def test_native_errors_are_charged_verbatim(self):
        body = b'{"jsonrpc":"2.0","error":{"code":-32602,"message":"bad arguments"}}'
        raw, event = client.request(self.run, 'tools/call', {'name': 'missing', 'arguments': {'bad': True}},
                                    exchange=lambda *args: (body, 400, {}))
        self.assertEqual(raw, body)
        self.assertEqual(event['http_status'], 400)
        self.assertEqual(self.state()['response_bytes'], len(body))
        self.assertIsNone(self.state()['stopped'])

    def test_interrupted_reservation_fails_closed(self):
        def killed(*args):
            raise KeyboardInterrupt()
        with self.assertRaises(KeyboardInterrupt):
            client.request(self.run, 'tools/list', {}, exchange=killed)
        self.assertEqual(self.state()['pending'], 1)
        with self.assertRaises(client.Stopped):
            client.request(self.run, 'tools/list', {}, exchange=lambda *args: self.fail('interrupted run resumed'))
        self.assertEqual(self.state()['calls'], 1)

    def test_transport_failure_conservatively_stops(self):
        def timeout(*args):
            raise TimeoutError('deadline')
        raw, event = client.request(self.run, 'tools/list', {}, exchange=timeout)
        self.assertFalse(event['response_complete'])
        self.assertEqual(self.state()['calls'], 1)
        self.assertIn('accounting unknown', self.state()['stopped'])

    def test_transport_cap_marks_prefix_incomplete(self):
        body = b'x'*(client.TRANSPORT_CAP+1)
        raw, event = client.request(self.run, 'tools/list', {}, exchange=lambda *args: (body, 200, {}))
        self.assertEqual(len(raw), client.TRANSPORT_CAP)
        self.assertFalse(event['response_complete'])
        self.assertIn('transport cap', self.state()['stopped'])

    def test_no_other_rpc_or_reinitialization(self):
        with self.assertRaises(ValueError):
            client.request(self.run, 'initialize', {}, exchange=lambda *args: self.fail('sent'))
        with self.assertRaises(FileExistsError):
            client.initialize(self.run, 'http://127.0.0.1:19381/mcp', self.run/'prompt.txt', self.run/'catalog.json')
        self.assertEqual(self.state()['calls'], 0)


if __name__ == '__main__':
    unittest.main()
