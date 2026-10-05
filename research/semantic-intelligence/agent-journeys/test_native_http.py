import unittest
from unittest.mock import patch
from native_http import NativeHTTP, rpc_response


class ResponseTests(unittest.TestCase):
    def test_sse_multiline_and_notifications(self):
        raw = b':keepalive\r\ndata: {"method":"notify"}\r\n\r\ndata: {"id":7,\r\ndata: "result":{"ok":true}}\r\n\r\n'
        self.assertEqual(rpc_response(raw, 'text/event-stream', 7)['result'], {'ok': True})

    def test_missing_or_duplicate_id(self):
        for raw in [b'data: {"id":8}\n\n', b'data: {"id":7}\n\ndata: {"id":7}\n\n']:
            with self.assertRaises(ValueError):
                rpc_response(raw, 'text/event-stream', 7)
        with self.assertRaises(ValueError):
            rpc_response(b'{"id":8}', 'application/json', 7)

    def test_native_error_is_a_response(self):
        self.assertIn('error', rpc_response(b'{"id":7,"error":{"code":-1}}', 'application/json', 7))

    def test_endpoint_confinement(self):
        for endpoint in ['http://example.com/mcp', 'https://user:secret@example.com/mcp',
                         'https://example.com/mcp?token=secret', 'file:///tmp/mcp']:
            with self.assertRaises(ValueError):
                NativeHTTP(endpoint)
        NativeHTTP('http://127.0.0.1:8081/mcp')
        NativeHTTP('https://example.com/mcp')

    def test_partial_response_survives_without_sensitive_error(self):
        class Sock:
            def settimeout(self, _): pass
        class Response:
            status = 200
            def getheader(self, name):
                return 'application/json' if name == 'Content-Type' else 'private-session'
            def read1(self, _):
                if not getattr(self, 'read', False):
                    self.read = True
                    return b'partial'
                raise OSError('secret-token')
        class Connection:
            sock = Sock()
            def __init__(self, *args, **kwargs): pass
            def request(self, *args): pass
            def getresponse(self): return Response()
            def close(self): pass
        with patch('native_http.http.client.HTTPConnection', Connection):
            client = NativeHTTP('http://localhost/mcp', 'secret-token')
            _, raw, receipt = client.exchange({'id':7,'method':'tools/list'})
        self.assertEqual(raw, b'partial')
        self.assertFalse(receipt['transport_complete'])
        self.assertEqual(receipt['error'], 'OSError')
        self.assertNotIn('secret-token', str(receipt))
        self.assertNotIn('private-session', str(receipt))

    def test_cap_preserves_crossing_bytes(self):
        class Sock:
            def settimeout(self, _): pass
        class Response:
            status = 200
            def getheader(self, _): return None
            def read1(self, count): return b'x'*count
        class Connection:
            sock = Sock()
            def __init__(self, *args, **kwargs): pass
            def request(self, *args): pass
            def getresponse(self): return Response()
            def close(self): pass
        with patch('native_http.http.client.HTTPConnection', Connection):
            _, raw, receipt = NativeHTTP('http://localhost/mcp',cap=10).exchange({'id':1,'method':'tools/list'})
        self.assertEqual(len(raw),11)
        self.assertEqual(receipt['error'],'response_cap_exceeded')
        self.assertFalse(receipt['transport_complete'])


if __name__ == '__main__':
    unittest.main()
