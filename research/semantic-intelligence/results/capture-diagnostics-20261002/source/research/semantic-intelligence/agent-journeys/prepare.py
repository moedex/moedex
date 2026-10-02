#!/usr/bin/env python3
"""Pre-assignment native MCP bootstrap for Moedex's stateless HTTP transport.

Run in the solver's intended network permission context before assigning a task.
Failed setup retains evidence but creates no task accounting directory. Never use
this to reset/recover an already assigned or stopped task.
"""
import argparse
import json
from pathlib import Path
import sys
import time
import urllib.parse

import client

PROTOCOL = '2025-11-25'  # Same compatibility lane as the unchanged task client.


def prepare(setup, run, endpoint, prompt, exchange=client.http_exchange):
    url = urllib.parse.urlsplit(endpoint)
    if (url.scheme != 'http' or url.hostname not in ('127.0.0.1', 'localhost', '::1') or
            url.username or url.password or url.query or url.fragment or url.path != '/mcp'):
        raise ValueError('endpoint must be a loopback http URL ending in /mcp')
    if run.exists():
        raise ValueError('task directory already exists; recovery/reset is forbidden')
    prompt.read_bytes()
    setup.mkdir(parents=True, exist_ok=False)
    report = {'version': 1, 'ready': False, 'endpoint': endpoint, 'exchanges': [],
              'started_utc': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
              'scope': 'pre-assignment setup; no solver call/byte/time budget consumed',
              'permission_scope': 'this process only; solver must use the same network permissions',
              'prepare_sha256': client.digest(Path(__file__).read_bytes()),
              'client_sha256': client.digest(Path(client.__file__).read_bytes())}

    def request(name, method, params, request_id=None):
        value = {'jsonrpc': '2.0', 'method': method, 'params': params}
        if request_id is not None:
            value['id'] = request_id
        wire = json.dumps(value, separators=(',', ':')).encode()
        client.persist_bytes(setup/(name+'.request.json'), wire)
        raw, status, headers = exchange(endpoint, wire, 30)
        client.persist_bytes(setup/(name+'.response.raw'), raw)
        report['exchanges'].append({'name': name, 'status': status, 'headers': headers,
                                    'bytes': len(raw), 'sha256': client.digest(raw)})
        if len(raw) > client.TRANSPORT_CAP:
            raise ValueError('setup response exceeds transport cap')
        if any(k.lower() == 'mcp-session-id' for k in headers):
            raise ValueError('stateful server unsupported by the frozen stateless task client')
        if request_id is None:
            if status != 202 or raw:
                raise ValueError('invalid initialized notification acknowledgement')
            return None
        if status != 200:
            raise ValueError(f'{method}: HTTP {status}')
        response = json.loads(raw)
        if (not isinstance(response, dict) or response.get('jsonrpc') != '2.0' or response.get('id') != request_id or
                'error' in response or not isinstance(response.get('result'), dict)):
            raise ValueError(f'{method}: invalid JSON-RPC result')
        return response['result']

    try:
        init = request('initialize', 'initialize', {'protocolVersion': PROTOCOL, 'capabilities': {},
                       'clientInfo': {'name': 'moedex-journey-setup', 'version': '1'}}, 1)
        if init.get('protocolVersion') != PROTOCOL:
            raise ValueError('server did not negotiate the task client compatibility version')
        if not isinstance(init.get('instructions'), str) or not init['instructions'].strip():
            raise ValueError('Moedex initialization instructions missing')
        request('initialized', 'notifications/initialized', {})
        catalog = request('catalog', 'tools/list', {}, 2)
        entries = catalog.get('tools')
        if not isinstance(entries, list) or not entries or catalog.get('nextCursor'):
            raise ValueError('expected a complete nonempty native tool catalog')
        if any(not isinstance(entry, dict) for entry in entries):
            raise ValueError('invalid tool descriptor')
        names = [entry.get('name') for entry in entries]
        if any(not isinstance(n, str) or not n for n in names) or len(set(names)) != len(names):
            raise ValueError('invalid or duplicate tool names')
        if any(not isinstance(entry.get('inputSchema'), dict) for entry in entries):
            raise ValueError('tool input schema missing')
        client.initialize(run, endpoint, prompt, setup/'catalog.response.raw')
        client.persist_bytes(run/'initialize.json', (setup/'initialize.response.raw').read_bytes())
        report.update(ready=True, tool_count=len(entries), instructions_sha256=client.digest(init['instructions'].encode()))
        client.atomic_json(run/'setup.json', report)
    except Exception as exc:
        report['error'] = f'{type(exc).__name__}: {exc}'
        raise
    finally:
        client.atomic_json(setup/'result.json', report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--setup-dir', type=Path, required=True)
    parser.add_argument('--run-dir', type=Path, required=True)
    parser.add_argument('--endpoint', required=True)
    parser.add_argument('--prompt', type=Path, required=True)
    args = parser.parse_args()
    try:
        report = prepare(args.setup_dir, args.run_dir, args.endpoint, args.prompt)
        print(json.dumps({'ready': report['ready'], 'tool_count': report['tool_count']}))
        return 0
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print(f'setup failed before assignment: {exc}', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
