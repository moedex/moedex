import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import browse
import client


class BrowseTests(unittest.TestCase):
    def reconstruct(self, raw, value, pointer='', maximum=2048):
        parts, offset = [], 0
        while True:
            wire = browse.page(raw, value, pointer, offset, maximum)
            self.assertLessEqual(len(wire), maximum)
            view = json.loads(wire)
            self.assertEqual(view['raw_sha256'], hashlib.sha256(raw).hexdigest())
            self.assertEqual(view['raw_bytes'], len(raw))
            parts.append(view['data'])
            if view['next_offset'] is None:
                break
            self.assertTrue(view['partial'])
            self.assertGreater(view['next_offset'], offset)
            offset = view['next_offset']
        return json.loads(''.join(parts))

    def test_lossless_unicode_escaping_and_nested_values(self):
        value = {'result': {'source': ('é😀\n\\"\x00'*700), 'empty': [], 'nil': None}}
        raw = json.dumps(value, ensure_ascii=False).encode()
        self.assertEqual(self.reconstruct(raw, value), value)
        self.assertEqual(self.reconstruct(raw, value, '/result/source'), value['result']['source'])

    def test_pointer_escaping_and_invalid_paths(self):
        value = {'a/b': {'~key': [None, 4]}}
        self.assertEqual(browse.select(value, '/a~1b/~0key/1'), 4)
        for pointer in ('a', '/a~2b', '/a~1b/~0key/01', '/a~1b/~0key/-1', '/a~1b/~0key/2'):
            with self.assertRaises((ValueError, KeyError, IndexError)):
                browse.select(value, pointer)

    def test_catalog_preserves_complete_descriptor(self):
        descriptor = {'name': 'trace', 'description': 'long '*1000,
                      'inputSchema': {'type': 'object'}, 'outputSchema': {'type': 'object'}}
        for value in ({'tools': [descriptor]}, {'result': {'tools': [descriptor]}}):
            raw = json.dumps(value).encode()
            self.assertEqual(browse.catalog_value(raw)['tools'], ['trace'])
            selected = browse.catalog_value(raw, 'trace')
            self.assertEqual(self.reconstruct(raw, selected), descriptor)
            with self.assertRaises(ValueError):
                browse.catalog_value(raw, 'missing')

    def test_invalid_limits_and_offsets(self):
        for maximum, offset in ((2047, 0), (16385, 0), (2048, -1), (2048, 5)):
            with self.assertRaises(ValueError):
                browse.page(b'{}', {}, offset=offset, maximum=maximum)

    def test_crossing_response_charged_in_full_and_views_do_not_mutate(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root/'prompt').write_text('task')
            (root/'catalog').write_text('{"tools": []}')
            run = root/'run'
            client.initialize(run, 'http://127.0.0.1:1/mcp', root/'prompt', root/'catalog')
            value = {'jsonrpc': '2.0', 'result': {'large': 'x'*client.MAX_RESPONSE_BYTES}}
            raw = json.dumps(value).encode()
            result, event = client.request(run, 'tools/call', {'name': 'tool', 'arguments': {}},
                                           exchange=lambda *a: (raw, 200, {}), now=lambda: 10)
            self.assertEqual(event['response_bytes_used'], len(raw))
            self.assertEqual(result, raw)
            before = {p.name: p.read_bytes() for p in run.iterdir()}
            self.assertEqual(self.reconstruct(result, value), value)
            self.assertEqual(before, {p.name: p.read_bytes() for p in run.iterdir()})
            with self.assertRaises(client.Stopped):
                client.request(run, 'tools/call', {'name': 'tool', 'arguments': {}}, now=lambda: 11)


if __name__ == '__main__':
    unittest.main()
