#!/usr/bin/env python3
"""Retain native HTTP MCP bodies for diagnostics; credentials stay in memory.

This transport is independent of the scored journey client and its accounting
auditor. Body bytes exclude HTTP headers, TLS framing, and model token usage.
"""
import http.client
import json
import time
import urllib.parse


def rpc_response(raw, content_type, request_id):
    """Decode JSON or multiline SSE data, selecting the matching RPC response."""
    if 'text/event-stream' not in (content_type or '').lower():
        value = json.loads(raw)
        if not isinstance(value, dict) or value.get('id') != request_id:
            raise ValueError('JSON-RPC response ID mismatch')
        return value
    messages, lines = [], []
    for line in raw.decode('utf-8-sig').splitlines() + ['']:
        if not line:
            if lines:
                messages.append(json.loads('\n'.join(lines)))
                lines = []
        elif line.startswith('data:'):
            value = line[5:]
            lines.append(value[1:] if value.startswith(' ') else value)
    matches = [m for m in messages if isinstance(m, dict) and m.get('id') == request_id]
    if len(matches) != 1:
        raise ValueError('expected exactly one matching JSON-RPC SSE response')
    return matches[0]


class NativeHTTP:
    """One native MCP session, with finite response size and elapsed deadlines.

    The caller must authorize the endpoint. HTTPS uses the default certificate
    validation; plaintext is limited to loopback. Redirects are never followed.
    Partial bodies and safe error types survive failure in the returned receipt.
    Session IDs, auth headers and endpoint URLs are not returned in receipts.
    """
    def __init__(self, endpoint, bearer_token=None, timeout=30, cap=8 << 20):
        url = urllib.parse.urlsplit(endpoint)
        if (url.scheme not in ('http', 'https') or not url.hostname or
                url.username or url.password or url.query or url.fragment):
            raise ValueError('expected an HTTP(S) endpoint without URL credentials or query')
        if url.scheme == 'http' and url.hostname not in ('localhost', '127.0.0.1', '::1'):
            raise ValueError('plaintext MCP requires loopback')
        if timeout <= 0 or cap <= 0:
            raise ValueError('timeout and response cap must be positive')
        if bearer_token and ('\r' in bearer_token or '\n' in bearer_token):
            raise ValueError('invalid bearer token')
        self.url, self.token = url, bearer_token
        self.timeout, self.cap = timeout, cap
        self.session_id, self.protocol = None, None

    def exchange(self, request):
        raw_request = json.dumps(request, separators=(',', ':')).encode()
        headers = {'Content-Type': 'application/json',
                   'Accept': 'application/json, text/event-stream'}
        if self.token:
            headers['Authorization'] = 'Bearer ' + self.token
        if self.session_id:
            headers['Mcp-Session-Id'] = self.session_id
        if self.protocol and request['method'] != 'initialize':
            headers['MCP-Protocol-Version'] = self.protocol
        cls = http.client.HTTPSConnection if self.url.scheme == 'https' else http.client.HTTPConnection
        conn = cls(self.url.hostname, self.url.port, timeout=self.timeout)
        start = time.monotonic()
        status, content_type, parts, size, error, complete = None, None, [], 0, None, False
        try:
            conn.request('POST', self.url.path or '/', raw_request, headers)
            sock = conn.sock
            response = conn.getresponse()
            status, content_type = response.status, response.getheader('Content-Type')
            self.session_id = response.getheader('Mcp-Session-Id') or self.session_id
            while size <= self.cap:
                remaining = self.timeout - (time.monotonic() - start)
                if remaining <= 0:
                    raise TimeoutError()
                sock.settimeout(remaining)
                block = response.read1(min(65536, self.cap + 1 - size))
                if not block:
                    # read1() can return EOF without raising IncompleteRead even
                    # when Content-Length still promises additional bytes.
                    if getattr(response, 'length', 0) not in (None, 0):
                        raise http.client.IncompleteRead(b'', response.length)
                    complete = True
                    break
                parts.append(block)
                size += len(block)
            if size > self.cap:
                error = 'response_cap_exceeded'
        except Exception as exc:
            # Exception text may contain a URL, credential, or server content.
            error = type(exc).__name__
        finally:
            conn.close()
        raw = b''.join(parts)
        receipt = {'status': status, 'content_type': content_type, 'body_bytes_observed': len(raw),
                   'transport_complete': complete, 'error': error,
                   'elapsed_seconds': time.monotonic() - start,
                   'session_id_present': bool(self.session_id)}
        return raw_request, raw, receipt

    def decode(self, raw, receipt, request_id):
        if not receipt['transport_complete']:
            raise ValueError('incomplete native response')
        value = rpc_response(raw, receipt['content_type'], request_id)
        if 'result' in value and isinstance(value['result'], dict):
            self.protocol = value['result'].get('protocolVersion', self.protocol)
        return value
