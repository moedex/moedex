#!/usr/bin/env python3
"""Logged native MCP client; this is an accounting boundary, not an OS sandbox."""
import argparse
import contextlib
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import sys
import time
import urllib.parse

from journey_clock import monotonic

MAX_CALLS = 24
MAX_RESPONSE_BYTES = 131072
MAX_SECONDS = 600
TRANSPORT_CAP = 8 << 20


class Stopped(Exception):
    pass


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def atomic_json(path, value):
    temporary = path.with_suffix('.tmp')
    with temporary.open('w') as f:
        json.dump(value, f, indent=2)
        f.write('\n')
        f.flush()
        os.fsync(f.fileno())
    os.replace(temporary, path)


def ledger(directory, event):
    with (directory/'transcript.jsonl').open('a') as f:
        f.write(json.dumps(event, separators=(',', ':'))+'\n')
        f.flush()
        os.fsync(f.fileno())


def persist_bytes(path, raw):
    with path.open('xb') as f:
        f.write(raw)
        f.flush()
        os.fsync(f.fileno())


def initialize(directory, endpoint, prompt, catalog):
    url = urllib.parse.urlsplit(endpoint)
    if (url.scheme != 'http' or url.hostname not in ('127.0.0.1', 'localhost', '::1')
            or url.username or url.password or url.query or url.fragment or url.path != '/mcp'):
        raise ValueError('endpoint must be a loopback http URL ending in /mcp')
    prompt_raw, catalog_raw = Path(prompt).read_bytes(), Path(catalog).read_bytes()
    directory.mkdir(parents=True, exist_ok=False)
    os.chmod(directory, 0o700)
    persist_bytes(directory/'prompt.txt', prompt_raw)
    persist_bytes(directory/'catalog.json', catalog_raw)
    state = {'version': 1, 'endpoint': endpoint, 'started_monotonic': None,
             'started_utc': None, 'calls': 0, 'response_bytes': 0,
             'pending': None, 'stopped': None,
             'limits': {'calls': MAX_CALLS, 'response_bytes': MAX_RESPONSE_BYTES,
                        'wall_seconds': MAX_SECONDS, 'transport_response_cap': TRANSPORT_CAP},
             'prompt_sha256': digest(prompt_raw), 'catalog_sha256': digest(catalog_raw),
             'client_sha256': digest(Path(__file__).read_bytes()),
             'initial_catalog_charged': False, 'token_usage': 'unknown',
             'filesystem_isolation': 'not enforced; solver can access host workspace',
             'accounting': 'Each attempted MCP list/call is charged, including native errors. Local CLI syntax errors are separate events, not MCP calls. Complete response bytes include the JSON-RPC envelope. First crossing response is retained and returned, then calls stop. Timer starts at first interaction; coordinator separately records solver spawn and answer times and enforces the full solver deadline.'}
    atomic_json(directory/'state.json', state)
    return state


@contextlib.contextmanager
def locked(directory):
    with (directory/'lock').open('a') as f:
        fcntl.flock(f.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(f.fileno(), fcntl.LOCK_UN)


def http_exchange(endpoint, request, timeout):
    url = urllib.parse.urlsplit(endpoint)
    deadline = monotonic()+timeout
    connection = http.client.HTTPConnection(url.hostname, url.port, timeout=timeout)
    try:
        connection.request('POST', url.path, request, {'Content-Type': 'application/json',
                           'Accept': 'application/json, text/event-stream'})
        sock = connection.sock
        response = connection.getresponse()
        chunks, size = [], 0
        while size <= TRANSPORT_CAP:
            remaining = deadline-monotonic()
            if remaining <= 0:
                raise TimeoutError('absolute response deadline exceeded')
            sock.settimeout(remaining)
            block = response.read1(min(65536, TRANSPORT_CAP+1-size))
            if not block:
                break
            chunks.append(block)
            size += len(block)
        return b''.join(chunks), response.status, dict(response.headers)
    finally:
        connection.close()


def request(directory, method, params, exchange=http_exchange, now=monotonic):
    if method not in ('tools/list', 'tools/call'):
        raise ValueError('only tools/list and tools/call are permitted')
    if not isinstance(params, dict):
        raise ValueError('params must be an object')
    if method == 'tools/list' and params:
        raise ValueError('tools/list takes no parameters in this client')
    if method == 'tools/call' and (set(params) != {'name', 'arguments'} or
            not isinstance(params['name'], str) or not params['name'] or
            not isinstance(params['arguments'], dict)):
        raise ValueError('tools/call requires name and arguments object')
    with locked(directory):
        state = json.loads((directory/'state.json').read_text())
        current = now()
        if state['started_monotonic'] is None:
            state['started_monotonic'] = current
            state['started_utc'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
        elapsed = current-state['started_monotonic']
        reason = state['stopped']
        if state['pending'] is not None:
            reason = 'interrupted request; response accounting unknown'
        elif elapsed < 0:
            reason = 'monotonic clock reset; wall accounting unknown'
        elif elapsed >= MAX_SECONDS:
            reason = 'wall budget exhausted'
        elif state['calls'] >= MAX_CALLS:
            reason = 'call budget exhausted'
        elif state['response_bytes'] >= MAX_RESPONSE_BYTES:
            reason = 'response byte budget exhausted'
        if reason:
            state['stopped'] = reason
            atomic_json(directory/'state.json', state)
            ledger(directory, {'event': 'blocked', 'reason': reason, 'elapsed_seconds': elapsed})
            raise Stopped(reason)
        ordinal = state['calls']+1
        stem = f'{ordinal:03d}'
        wire = json.dumps({'jsonrpc': '2.0', 'id': ordinal, 'method': method,
                           'params': params}, separators=(',', ':')).encode()
        persist_bytes(directory/(stem+'.request.json'), wire)
        state['calls'] = ordinal
        state['pending'] = ordinal
        atomic_json(directory/'state.json', state)
        ledger(directory, {'event': 'request', 'ordinal': ordinal, 'method': method,
                           'file': stem+'.request.json', 'sha256': digest(wire),
                           'elapsed_seconds': elapsed})
        start = now()
        raw, status, headers, error = b'', None, {}, None
        try:
            raw, status, headers = exchange(state['endpoint'], wire, MAX_SECONDS-elapsed)
        except Exception as exc:
            error = f'{type(exc).__name__}: {exc}'
        elapsed = now()-state['started_monotonic']
        incomplete = len(raw) > TRANSPORT_CAP
        observed = len(raw)
        if incomplete:
            raw = raw[:TRANSPORT_CAP]
        persist_bytes(directory/(stem+'.response.raw'), raw)
        # Charge the full crossing response; no next request can be sent.
        state['response_bytes'] += observed
        state['pending'] = None
        reasons = []
        if error:
            reasons.append('transport failure; response accounting unknown')
        if incomplete:
            reasons.append('transport cap exceeded; response incomplete')
        if state['response_bytes'] >= MAX_RESPONSE_BYTES:
            reasons.append('response byte budget exhausted')
        if state['calls'] >= MAX_CALLS:
            reasons.append('call budget exhausted')
        if elapsed >= MAX_SECONDS:
            reasons.append('wall budget exhausted')
        if reasons:
            state['stopped'] = '; '.join(reasons)
        atomic_json(directory/'state.json', state)
        event = {'event': 'response', 'ordinal': ordinal, 'file': stem+'.response.raw',
                 'sha256': digest(raw), 'serialized_response_bytes': observed,
                 'response_complete': not incomplete and error is None,
                 'http_status': status, 'headers': headers, 'error': error,
                 'elapsed_seconds': elapsed, 'request_seconds': now()-start,
                 'calls_used': state['calls'], 'response_bytes_used': state['response_bytes'],
                 'stopped': state['stopped'], 'token_usage': 'unknown'}
        ledger(directory, event)
        return raw, event


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    init = sub.add_parser('init', help='coordinator setup; initial catalog is uncharged')
    init.add_argument('--run-dir', type=Path, required=True)
    init.add_argument('--endpoint', required=True)
    init.add_argument('--prompt', type=Path, required=True)
    init.add_argument('--catalog', type=Path, required=True)
    listing = sub.add_parser('list')
    listing.add_argument('--run-dir', type=Path, required=True)
    call = sub.add_parser('call')
    call.add_argument('--run-dir', type=Path, required=True)
    call.add_argument('--name', required=True)
    call.add_argument('--arguments', required=True, help='JSON object')
    args = parser.parse_args()
    try:
        if args.command == 'init':
            initialize(args.run_dir.resolve(), args.endpoint, args.prompt, args.catalog)
            print('Run initialized; timer starts at first list/call.')
            return
        params = {} if args.command == 'list' else {'name': args.name, 'arguments': json.loads(args.arguments)}
        raw, event = request(args.run_dir.resolve(), 'tools/'+args.command, params)
        # No reformatting or wrapping of the product response.
        sys.stdout.buffer.write(raw)
        sys.stdout.buffer.flush()
        print(json.dumps({'journey_accounting': event}), file=sys.stderr)
        if event['error'] or not event['response_complete']:
            raise SystemExit(2)
    except (Stopped, ValueError) as exc:
        if isinstance(exc, ValueError) and (args.run_dir/'state.json').exists():
            with locked(args.run_dir):
                ledger(args.run_dir, {'event': 'local_cli_error', 'error': str(exc), 'charged_as_mcp_call': False})
        print(json.dumps({'journey_stopped': str(exc)}), file=sys.stderr)
        raise SystemExit(2)


if __name__ == '__main__':
    main()
