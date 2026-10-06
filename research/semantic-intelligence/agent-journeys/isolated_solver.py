#!/usr/bin/env python3
"""Credential-free container solvers with host-side native/provider recording.

No corpus or host directories are mounted. The container has no external
network. A fixed host controller serves its provider and MCP requests over
Docker stdin/stdout. This controller never supplies a shell or arbitrary URL
tool. Product credentials stay in host memory. Evidence belongs in an authorized
private directory; raw provider bodies contain prompts and retrieved source.
"""
import argparse
import base64
import http.client
import json
import math
import os
from pathlib import Path
import re
import selectors
import signal
import subprocess
import time
import urllib.parse

import contract as native_contract
from native_http import NativeHTTP
from journey_clock import monotonic
from run_record import RunRecord, _new_file, canonical, digest, read_ref, reference
import provenance
from native_scope import NativeScope, ScopeViolation
from provider_resources import (ProviderResources, validate_policy as validate_provider_resources,
                                validate_prompt_instructions)


IMAGE = re.compile(r'^sha256:[0-9a-f]{64}$')


def bounded_response(response, cap):
    """Bound the complete JSON-RPC envelope; raw native bytes remain retained."""
    raw = canonical(response)
    if len(raw) <= cap:
        return response
    text = raw.decode()
    def envelope(count):
        payload = {'truncated_display': True, 'native_envelope_bytes': len(raw), 'prefix': text[:count]}
        result = {'content': [{'type': 'text', 'text': canonical(payload).decode()}]}
        if 'error' in response or (isinstance(response.get('result'), dict) and response['result'].get('isError')):
            result['isError'] = True
        return {'jsonrpc': '2.0', 'id': response.get('id'), 'result': result}
    lo, hi = 0, len(text)
    while lo < hi:
        middle = (lo + hi + 1) // 2
        if len(canonical(envelope(middle))) <= cap:
            lo = middle
        else:
            hi = middle - 1
    result = envelope(lo)
    if len(canonical(result)) > cap:
        raise ValueError('display cap cannot fit truncation envelope')
    return result


def container_command(image):
    if not isinstance(image, str) or not IMAGE.fullmatch(image):
        raise ValueError('solver image must be an immutable local image ID')
    return ['docker', 'run', '--rm', '-i', '--network', 'none', '--read-only',
            '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
            '--pids-limit', '128', '--memory', '1g',
            '--tmpfs', '/tmp:rw,nosuid,size=128m',
            '--tmpfs', '/work:rw,nosuid,size=32m',
            '--tmpfs', '/root/.codex:rw,nosuid,size=128m', image]


def provider_exchange(base_url, token, raw, timeout, cap=64 << 20):
    """POST only to the configured Responses endpoint; retain interrupted bytes.

    Counts decoded HTTP response-body bytes. Headers/TLS framing are excluded.
    No redirect, cookie, session ID, credential or exception text is retained.
    """
    url = urllib.parse.urlsplit(base_url)
    if (url.scheme not in ('https', 'http') or not url.hostname or url.username or
            url.password or url.query or url.fragment or
            (url.scheme == 'http' and url.hostname not in ('127.0.0.1', 'localhost', '::1'))):
        raise ValueError('provider URL must be HTTPS or plaintext loopback')
    if not math.isfinite(timeout) or timeout <= 0 or type(cap) is not int or cap <= 0:
        raise ValueError('positive finite transport budget required')
    if '\r' in token or '\n' in token:
        raise ValueError('invalid provider credential')
    cls = http.client.HTTPSConnection if url.scheme == 'https' else http.client.HTTPConnection
    connection = cls(url.hostname, url.port, timeout=timeout)
    started = time.monotonic()
    parts, size, status, content_type, error, complete = [], 0, None, None, None, False
    def remaining():
        value = timeout - (time.monotonic() - started)
        if value <= 0:
            raise TimeoutError()
        return value
    try:
        connection.connect()
        connection.sock.settimeout(remaining())
        connection.request('POST', url.path.rstrip('/') + '/responses', raw,
                           {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json',
                            'Accept': 'application/json, text/event-stream', 'Accept-Encoding': 'identity'})
        sock = connection.sock
        sock.settimeout(remaining())
        response = connection.getresponse()
        status, content_type = response.status, response.getheader('Content-Type')
        while size <= cap:
            sock.settimeout(remaining())
            block = response.read1(min(65536, cap + 1 - size))
            if not block:
                if getattr(response, 'length', 0) not in (None, 0):
                    raise http.client.IncompleteRead(b'', response.length)
                remaining()
                complete = True
                break
            parts.append(block)
            size += len(block)
        if size > cap:
            error = 'response_cap_exceeded'
    except Exception as exc:
        error = type(exc).__name__
    finally:
        connection.close()
    body = b''.join(parts)
    return body, {'status': status, 'content_type': content_type,
                  'body_bytes_observed': len(body), 'transport_complete': complete,
                  'error': error, 'elapsed_seconds': time.monotonic() - started}


def validate_provider_identity(body, receipt, expected_models):
    """Check every returned identity, requiring a completed response on HTTP success.

    Call only after retaining the exact response and receipt. Error bodies may
    omit an identity, but any identity they do return must still match the freeze.
    """
    expected = set(expected_models)
    if not expected or any(not isinstance(model, str) or not model for model in expected):
        raise ValueError('frozen observed provider identities required')
    status = receipt.get('status')
    successful = type(status) is int and 200 <= status < 300
    values, completed = [], False
    content_type = (receipt.get('content_type') or '').split(';', 1)[0].strip().lower()
    try:
        if content_type == 'text/event-stream' or body.lstrip().startswith((b'event:', b'data:')):
            events = []
            data = []
            def append_event(payload):
                if payload == '[DONE]':
                    return
                try:
                    events.append(json.loads(payload))
                except json.JSONDecodeError:
                    if successful:
                        raise
            for line in body.decode('utf-8', errors='strict' if successful else 'replace').replace('\r\n', '\n').replace('\r', '\n').split('\n'):
                if not line:
                    if data:
                        append_event('\n'.join(data))
                        data = []
                elif line.startswith('data:'):
                    data.append(line[5:].lstrip(' '))
            if data:
                append_event('\n'.join(data))
            for event in events:
                if not isinstance(event, dict):
                    raise ValueError('provider event must be an object')
                if 'model' in event:
                    values.append(event['model'])
                response = event.get('response')
                if isinstance(response, dict):
                    if 'model' in response:
                        values.append(response['model'])
                    if event.get('type') == 'response.completed':
                        if not isinstance(response.get('model'), str) or not response['model'] or response.get('status') != 'completed':
                            raise ValueError('completed provider response lacks model identity')
                        completed = True
        else:
            response = json.loads(body)
            if not isinstance(response, dict):
                raise ValueError('provider response must be an object')
            if 'model' in response:
                values.append(response['model'])
            completed = response.get('status') == 'completed' and isinstance(response.get('model'), str) and bool(response['model'])
    except (UnicodeError, json.JSONDecodeError):
        if successful:
            raise ValueError('successful provider response cannot establish model identity') from None
        return
    if any(not isinstance(model, str) or model not in expected for model in values):
        raise ValueError('provider returned model identity differs from frozen observation')
    if successful and not completed:
        raise ValueError('successful provider response lacks completed model identity')


class NativeBroker:
    """Serve only the frozen native tool roster, retaining full native onboarding."""
    def __init__(self, client, record, allowed_tools, observed_product=None, source_scope=None):
        if not allowed_tools or len(set(allowed_tools)) != len(allowed_tools):
            raise ValueError('unique nonempty native allowlist required')
        self.client, self.record, self.allowed = client, record, set(allowed_tools)
        self.catalog = None
        self.display_cap = 8192
        self.observed_product = observed_product
        self.source_scope = source_scope
        if source_scope is not None and source_scope.allowed != self.allowed:
            raise ValueError('scope policy and native allowlist differ')
        self.last_response_sha256 = None
        self.last_ordinal = None

    def exchange(self, request):
        request_bytes = json.dumps(request, separators=(',', ':')).encode()
        ordinal = self.record.begin_call(request_bytes)
        self.last_ordinal = ordinal
        self.client.timeout = min(self.client.timeout, self.record.remaining_seconds())
        returned, body, receipt = self.client.exchange(request)
        if returned != request_bytes:
            raise ValueError('native transport changed request bytes')
        self.record.finish_call(ordinal, body, receipt)
        self.last_response_sha256 = digest(body)
        if request.get('method') == 'notifications/initialized':
            if not receipt['transport_complete'] or receipt['status'] not in (200, 202, 204):
                raise ValueError('native notification failed')
            return None
        return self.client.decode(body, receipt, request['id'])

    def onboard(self):
        response = self.exchange({'jsonrpc': '2.0', 'id': 1, 'method': 'initialize',
                                  'params': {'protocolVersion': '2025-03-26', 'capabilities': {},
                                             'clientInfo': {'name': 'recorded-native-solver', 'version': '1'}}})
        if 'error' in response:
            raise ValueError('native initialization failed')
        self.exchange({'jsonrpc': '2.0', 'method': 'notifications/initialized'})
        catalog = self.exchange({'jsonrpc': '2.0', 'id': 3, 'method': 'tools/list', 'params': {}})
        tools = catalog['result']['tools']
        if len({t['name'] for t in tools}) != len(tools) or not self.allowed.issubset({t['name'] for t in tools}):
            raise ValueError('native catalog lacks frozen tools or has duplicate names')
        if catalog['result'].get('nextCursor'):
            raise ValueError('paginated native catalogs require a frozen onboarding adapter')
        if self.observed_product is not None:
            if (response['result'].get('serverInfo') != self.observed_product['server_info'] or
                    digest(canonical(catalog['result'])) != self.observed_product['catalog_sha256']):
                raise ValueError('native identity differs from frozen observation')
        self.catalog = [t for t in tools if t['name'] in self.allowed]
        if self.source_scope is not None:
            self.catalog = self.source_scope.catalog(self.catalog)
        return response['result']

    def handle(self, request):
        method, request_id = request.get('method'), request.get('id')
        result = None
        if method == 'initialize':
            result = {'protocolVersion': '2025-03-26', 'capabilities': {'tools': {}},
                      'serverInfo': {'name': 'recorded-native-broker', 'version': '1'}}
        elif method == 'notifications/initialized':
            return None
        elif method == 'tools/list':
            if self.catalog is None:
                raise ValueError('native onboarding has not completed')
            result = {'tools': self.catalog}
        elif method == 'tools/call':
            params = request.get('params', {})
            if self.source_scope is None and params.get('name') not in self.allowed:
                raise ValueError('native tool outside frozen allowlist')
            native_request = {'jsonrpc': '2.0', 'id': request_id, 'method': 'tools/call', 'params': params}
            policy_receipt = None
            if self.source_scope is None:
                response = self.exchange(native_request)
            else:
                context, reason = None, None
                try:
                    context = self.source_scope.prepare(params)
                except (ScopeViolation, ValueError, TypeError, KeyError):
                    reason = 'request_provenance_not_admitted'
                if reason is not None:
                    # Charge the denied intent as one attempt, with zero native
                    # response bytes. No product request is dispatched.
                    self.last_ordinal = self.record.begin_call(json.dumps(native_request, separators=(',', ':')).encode())
                    self.record.finish_call(self.last_ordinal, b'', {
                        'transport_complete': True, 'body_bytes_observed': 0,
                        'native_dispatched': False, 'scope_policy_blocked': True})
                    self.last_response_sha256 = digest(b'')
                    response = self.source_scope.error(request_id)
                else:
                    try:
                        response = self.exchange(native_request)
                    except (ValueError, TypeError, KeyError):
                        reason = 'response_provenance_not_admitted'
                        response = self.source_scope.error(request_id)
                    # A late/crossing response is already retained and charged;
                    # do not validate/register selectors or display it late.
                    self.record.remaining_seconds()
                    if reason is None:
                        try:
                            response = self.source_scope.accept(response, context)
                        except (ScopeViolation, ValueError, TypeError, KeyError):
                            reason = 'response_provenance_not_admitted'
                            response = self.source_scope.error(request_id)
                policy_receipt = {'schema': 'native-scope-decision-v1',
                    'policy_sha256': self.source_scope.policy_sha256,
                    'accepted': reason is None, 'reason': reason, 'native_dispatched': context is not None,
                    'ordinal': self.last_ordinal, 'native_response_sha256': self.last_response_sha256,
                    'graph_revision': self.source_scope.revision,
                    'registered_nodes': len(self.source_scope.nodes), 'registered_cursors': len(self.source_scope.cursors)}
            # Full native response remains in raw evidence. The entire model-visible
            # envelope (including JSON-RPC/MCP wrappers) is subject to the display cap.
            response = bounded_response(response, self.display_cap)
            raw = canonical(response)
            if policy_receipt is None:
                self.record.display(raw)
            else:
                policy_receipt['display_sha256'] = digest(raw)
                self.record.display(raw, scope_policy=policy_receipt)
            return response
        elif method in ('resources/list', 'resources/templates/list', 'prompts/list'):
            result = {('resources' if method == 'resources/list' else
                       'resourceTemplates' if method == 'resources/templates/list' else 'prompts'): []}
        else:
            raise ValueError('broker method outside frozen capability set')
        return {'jsonrpc': '2.0', 'id': request_id, 'result': result}


class Capture:
    """New evidence directory per process; requests persisted BEFORE dispatch."""
    def __init__(self, directory):
        self.root = Path(directory).resolve()
        self.root.mkdir(mode=0o700)
        self.files, self.ordinals = [], {}
        self.journal = self.root / 'events.jsonl'

    def retain(self, name, raw):
        path = self.root / name
        _new_file(path, raw)
        self.files.append(reference(self.root, path))

    def event(self, event):
        event = dict(event, controller_monotonic=monotonic())
        with self.journal.open('ab') as stream:
            stream.write(canonical(event) + b'\n')
            stream.flush()
            os.fsync(stream.fileno())

    def request(self, channel, raw):
        ordinal = self.ordinals.get(channel, 0) + 1
        self.ordinals[channel] = ordinal
        stem = '%s-%04d' % (channel, ordinal)
        self.retain(stem + '.request.json', raw)
        return stem

    def finish(self, complete):
        if self.journal.exists():
            self.files.append(reference(self.root, self.journal))
        self.retain('inventory.json', canonical({'complete': complete, 'files': self.files.copy(),
                                                'scope': 'exact provider/native broker bodies and serialized CLI events; no auth headers'}) + b'\n')


def decode_message(message):
    if message.get('channel') not in ('provider', 'mcp'):
        raise ValueError('unknown request channel')
    if not isinstance(message.get('id'), str) or not re.fullmatch(r'[1-9][0-9]*', message['id']):
        raise ValueError('invalid request identity')
    if message.get('method') != 'POST' or message.get('path') != ('/v1/responses' if message['channel'] == 'provider' else '/mcp'):
        raise ValueError('controller accepts only the frozen endpoint paths')
    raw = base64.b64decode(message['body_base64'], validate=True)
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise ValueError('request must be a JSON object')
    return raw, value


def validate_execution_config(root, config):
    """Bind ALL task budgets, identity and executing code to frozen bytes."""
    frozen = json.loads(read_ref(root, config['freeze']))
    contract = json.loads(read_ref(root, frozen['contract']))
    tasks = native_contract.validate(contract, lambda ref: read_ref(root, ref))
    if config['identity']['task'] not in tasks:
        raise ValueError('assigned task missing from frozen contract')
    task = tasks[config['identity']['task']]
    prompt = read_ref(root, task['prompt'])
    if (digest(canonical(contract)) != config['identity']['contract_sha256'] or
            task['prompt']['sha256'] != config['identity']['prompt_sha256'] or
            prompt != config['prompt'].encode() or
            config['budgets'] != contract['budgets'] or
            frozen.get('arm') != config['identity']['arm'] or
            frozen.get('isolation', {}).get('image_sha256') != config['image'] or
            frozen.get('model', {}).get('requested_alias') != config['model'] or
            frozen.get('model', {}).get('settings', {}).get('reasoning_effort') != config['reasoning_effort'] or
            frozen.get('model', {}).get('settings', {}).get('reasoning_summary') != 'none' or
            frozen.get('native_allowed_tools') != config['enabled_tools'] or
            config.get('timeout_seconds', 600) != contract['budgets']['assignment_seconds']):
        raise ValueError('execution configuration differs from frozen task, budgets, model, image or native roster')
    fields = frozen['model']['settings'].get('provider_fields', {})
    expected = {'model', 'reasoning', 'tool_choice', 'parallel_tool_calls', 'text', 'store', 'stream', 'include'}
    resources = frozen.get('provider_resources')
    config.pop('_provider_resources', None)
    if 'provider_resources' in config and config['provider_resources'] != resources:
        raise ValueError('provider resource configuration differs from frozen policy')
    if resources is not None:
        resources = validate_provider_resources(resources)
        validate_prompt_instructions(config['prompt'], resources)
        expected.add('max_output_tokens')
        if type(fields.get('max_output_tokens')) is not int or fields['max_output_tokens'] != resources['max_output_tokens']:
            raise ValueError('frozen provider output limit differs from resource policy')
        config['_provider_resources'] = resources
    if set(fields) != expected or fields['model'] != config['model'] or fields['reasoning'].get('effort') != config['reasoning_effort']:
        raise ValueError('complete frozen provider request settings required')
    config['provider_fields'] = fields
    runner_names = ['isolated_solver.py', 'run_record.py', 'native_http.py', 'journey_clock.py',
                    'contract.py', 'compare.py']
    config.pop('_source_scope', None)
    if 'source_scope_policy' in frozen or 'source_scope_policy' in config:
        if config.get('source_scope_policy') != frozen.get('source_scope_policy'):
            raise ValueError('execution scope policy differs from freeze')
        scope_policy = json.loads(read_ref(root, frozen['source_scope_policy']))
        compiler_inputs = ({name: read_ref(root, ref) for name, ref in scope_policy['compiler_admission'].items()
                            if name != 'schema'} if 'compiler_admission' in scope_policy else None)
        scope = NativeScope(scope_policy, policy_sha256=frozen['source_scope_policy']['sha256'],
                            compiler_inputs=compiler_inputs)
        sources = contract['corpus']['repositories'] if contract['schema'] == 'native-pair-v2' else [contract['corpus']]
        pins = {row['repository']: row['commit'] for row in sources}
        if (scope.allowed != set(config['enabled_tools']) or set(scope.projects) != set(pins) or
                any(row['commit'] != pins[name] for name, row in scope.projects.items())):
            raise ValueError('scope policy tools, background corpus roster or pins differ from contract')
        config['_source_scope'] = scope
        runner_names.append('native_scope.py')
    if resources is not None:
        runner_names.append('provider_resources.py')
    config.pop('_observed_identities', None)
    if provenance.mode(frozen) == 'observed-service-v1':
        blockers = provenance.validate(root, frozen, read_ref)
        if blockers:
            raise ValueError('observed provenance is invalid: ' + '; '.join(blockers))
        config['_observed_identities'] = provenance.observations(root, frozen, read_ref)
        runner_names.append('provenance.py')
    for name in runner_names:
        if frozen.get('runners', {}).get(name) != digest(Path(__file__).with_name(name).read_bytes()):
            raise ValueError('executing runner differs from frozen hash: ' + name)
    return frozen


def validate_environment_bindings(frozen, config, environment):
    if (digest(environment[config['native_url_env']].encode()) != frozen['product'].get('endpoint_sha256') or
            digest(environment[config['provider_url_env']].encode()) != frozen['model'].get('provider_base_url_sha256')):
        raise ValueError('native or provider endpoint differs from frozen identity')


def validate_provider_request(config, value):
    expected = config.get('provider_fields', {})
    if (value.get('model') != config['model'] or value.get('reasoning', {}).get('effort') != config['reasoning_effort'] or
            any(value.get(k) != v for k, v in expected.items()) or
            (expected and set(value) - set(expected) - {'input', 'prompt_cache_key', 'client_metadata'})):
        raise ValueError('solver changed frozen provider request settings')


def probe_provider_response(model, ordinal):
    """One metadata-only synthetic tool call, then HTTP 400; zero model calls."""
    if ordinal > 1:
        return canonical({'error': {'message': 'controlled capture probe', 'type': 'probe'}}), {
            'status': 400, 'content_type': 'application/json', 'transport_complete': True}
    item = {'type': 'custom_tool_call', 'id': 'synthetic_metadata_call', 'status': 'completed',
            'call_id': 'synthetic_metadata_call', 'name': 'exec',
            'input': 'text(ALL_TOOLS.map(({name}) => name));'}
    response = {'id': 'synthetic_metadata_response', 'object': 'response', 'status': 'completed',
                'model': model, 'output': [item],
                'usage': {'input_tokens': 0, 'output_tokens': 0, 'total_tokens': 0}}
    events = [
        {'type': 'response.created', 'response': dict(response, status='in_progress', output=[])},
        {'type': 'response.output_item.added', 'output_index': 0, 'item': dict(item, status='in_progress', input='')},
        {'type': 'response.output_item.done', 'output_index': 0, 'item': item},
        {'type': 'response.completed', 'response': response}]
    body = b''.join(b'event: ' + e['type'].encode() + b'\ndata: ' + canonical(e) + b'\n\n' for e in events)
    return body, {'status': 200, 'content_type': 'text/event-stream', 'transport_complete': True}


def run_container(config, directory, broker=None, record=None, probe=False):
    """Run one immutable, network-isolated image; probe never contacts a provider.

    Probe returns a synthetic metadata-only tool call, then answers HTTP 400. Its
    evidence demonstrates capture/isolation capabilities, not native readiness,
    model correctness, or a verified immutable provider model revision.
    """
    capture = Capture(directory)
    resources = None
    if config.get('_provider_resources') is not None:
        resources = ProviderResources(config['_provider_resources'],
                                      emit=lambda event: capture.event({'channel': 'provider_resource', 'event': event}))
        capture.retain('provider-resource-policy.json', canonical(resources.policy) + b'\n')
    command = container_command(config['image'])
    cidfile = capture.root / 'container.id'
    command[2:2] = ['--cidfile', str(cidfile)]
    capture.retain('container-command.json', canonical(command) + b'\n')
    timeout = config.get('timeout_seconds', 600)
    if (isinstance(timeout, bool) or not isinstance(timeout, (int, float)) or
            not math.isfinite(timeout) or timeout <= 0 or timeout > 3600):
        raise ValueError('positive finite assignment timeout of at most one hour required')
    started, seen, exit_code, complete = time.monotonic(), set(), None, False
    submitted_answer_sha256 = None
    def remaining_seconds():
        remaining = timeout - (time.monotonic() - started)
        if record is not None:
            remaining = min(remaining, record.remaining_seconds())
        if remaining <= 0:
            raise TimeoutError()
        return remaining
    def interrupted(signum, frame):
        raise InterruptedError('controller interrupted')
    previous_sigterm = signal.signal(signal.SIGTERM, interrupted)
    with (capture.root / 'container.stderr').open('wb') as stderr:
        process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=stderr)
        selector = selectors.DefaultSelector()
        selector.register(process.stdout, selectors.EVENT_READ)
        buffer = b''
        reply_ordinal = 0
        def reply(message):
            nonlocal reply_ordinal
            # A buffered provider response may finish after the bridge's
            # assignment deadline. Retain it, but never write a late reply.
            remaining_seconds()
            wire = canonical(message) + b'\n'
            reply_ordinal += 1
            binding = {'reply_ordinal': reply_ordinal, 'wire_sha256': digest(wire), 'wire_bytes': len(wire)}
            # Persist the exact attempted relay bytes before writing. Missing
            # commit means relay completion is unknown, never a proven nonrelay.
            capture.event(dict(binding, channel='controller_reply', message=message))
            if process.stdin.write(wire) != len(wire):
                raise RuntimeError('incomplete controller relay write')
            process.stdin.flush()
            capture.event(dict(binding, channel='controller_reply_commit'))
        try:
            start = {k: config[k] for k in ('model', 'reasoning_effort', 'prompt', 'enabled_tools')}
            start['timeout_ms'] = int(remaining_seconds() * 1000)
            if start['timeout_ms'] < 1:
                raise TimeoutError()
            # The relay buffers whole exchanges, including provider SSE. Its
            # timeout must allow the same remaining budget as the controller.
            start['exchange_timeout_ms'] = start['timeout_ms']
            reply({'channel': 'start', 'config': start})
            while exit_code is None:
                remaining = remaining_seconds()
                ready = selector.select(min(remaining, 1))
                if not ready:
                    if process.poll() is not None:
                        raise RuntimeError('container ended without completion event')
                    continue
                chunk = os.read(process.stdout.fileno(), 65536)
                if not chunk:
                    raise RuntimeError('container stream ended without completion event')
                buffer += chunk
                if len(buffer) > 96 << 20:
                    raise ValueError('container message cap exceeded')
                while b'\n' in buffer:
                    line, buffer = buffer.split(b'\n', 1)
                    message = json.loads(line)
                    capture.event(message)
                    channel = message.get('channel')
                    if channel in ('provider', 'mcp'):
                        # Streamable MCP clients may probe GET SSE or close a
                        # session via DELETE. These never forward native I/O.
                        if not isinstance(message.get('id'), str) or not re.fullmatch(r'[1-9][0-9]*', message['id']) or message['id'] in seen:
                            raise ValueError('invalid or reused request identity')
                        seen.add(message['id'])
                        if channel == 'mcp' and message.get('path') == '/mcp' and message.get('method') in ('GET', 'DELETE'):
                            reply({'channel': 'response', 'id': message['id'],
                                   'status': 405 if message['method'] == 'GET' else 202,
                                   'content_type': 'application/json', 'body_base64': ''})
                            continue
                        raw, value = decode_message(message)
                        stem = capture.request(channel, raw)
                        if channel == 'provider':
                            forwarded = raw
                            if resources is not None:
                                forwarded = resources.begin(stem, raw)
                                capture.retain(stem + '.upstream-request.json', forwarded)
                                value = json.loads(forwarded)
                            validate_provider_request(config, value)
                            if probe:
                                body, receipt = probe_provider_response(config['model'], capture.ordinals['provider'])
                                receipt['body_bytes_observed'] = len(body)
                            else:
                                body, receipt = provider_exchange(os.environ[config['provider_url_env']],
                                                                  os.environ[config['provider_token_env']], forwarded,
                                                                  remaining_seconds(),
                                                                  **({'cap': resources.transport_cap()} if resources is not None else {}))
                        else:
                            if probe:
                                result = mock_mcp(value, config['enabled_tools'])
                            else:
                                result = broker.handle(value)
                            body = canonical(result) if result is not None else b''
                            receipt = {'status': 200 if result is not None else 202,
                                       'content_type': 'application/json', 'body_bytes_observed': len(body),
                                       'transport_complete': True}
                        capture.retain(stem + '.response.raw', body)
                        capture.retain(stem + '.receipt.json', canonical(receipt) + b'\n')
                        if channel == 'provider' and resources is not None:
                            resources.finish(stem, body, receipt)
                        if channel == 'provider' and not probe and config.get('_observed_identities'):
                            validate_provider_identity(body, receipt,
                                                       config['_observed_identities']['model']['returned_models'])
                        if not receipt['transport_complete']:
                            raise RuntimeError('incomplete recorded exchange')
                        reply({'channel': 'response', 'id': message['id'], 'status': receipt['status'],
                               'content_type': receipt['content_type'], 'body_base64': base64.b64encode(body).decode()})
                    elif channel == 'answer':
                        if not probe:
                            answer = base64.b64decode(message['body_base64'], validate=True)
                            value = json.loads(answer)
                            if not isinstance(value, dict) or value.get('task') != config['identity']['task']:
                                raise ValueError('final answer must bind the assigned task')
                            if digest(answer) != submitted_answer_sha256:
                                record.submit_answer(answer)
                                submitted_answer_sha256 = digest(answer)
                    elif channel == 'event' and not probe:
                        event = message.get('event', {})
                        item = event.get('item', {})
                        if event.get('type') == 'item.completed' and item.get('type') == 'agent_message':
                            text = item.get('text')
                            if isinstance(text, str):
                                try:
                                    candidate = json.loads(text)
                                except ValueError:
                                    candidate = None
                                if isinstance(candidate, dict) and candidate.get('task') == config['identity']['task']:
                                    answer = text.encode()
                                    record.submit_answer(answer)
                                    submitted_answer_sha256 = digest(answer)
                    elif channel == 'exit':
                        exit_code = message.get('code')
                        if type(exit_code) is not int:
                            raise ValueError('invalid container completion')
                        if buffer.strip():
                            raise ValueError('unexpected output after container completion')
                        break
                    elif channel not in ('event', 'diagnostic', 'error'):
                        raise ValueError('unknown container output')
            complete = True
        except BaseException as exc:
            capture.event({'channel': 'controller_error', 'error_type': type(exc).__name__})
            if record is not None:
                try:
                    record.stop(type(exc).__name__)
                except RuntimeError:
                    pass
            raise
        finally:
            selector.close()
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            try:
                if cidfile.is_file():
                    container_id = cidfile.read_text().strip()
                    if not re.fullmatch(r'[0-9a-f]{64}', container_id):
                        raise ValueError('invalid container cleanup identity')
                    cleanup = subprocess.run(['docker', 'rm', '--force', container_id],
                                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
                    capture.retain('container-cleanup.json', canonical({'container_id': container_id,
                                   'returncode': cleanup.returncode, 'auto_remove_may_already_have_completed': True}) + b'\n')
                    capture.files.append(reference(capture.root, cidfile))
            except Exception as exc:
                complete = False
                capture.event({'channel': 'cleanup_error', 'error_type': type(exc).__name__})
            finally:
                signal.signal(signal.SIGTERM, previous_sigterm)
                if resources is not None:
                    resources.abort_pending()
                    capture.retain('provider-resource-ledger.json', canonical(resources.summary()) + b'\n')
                capture.finish(complete)
    return {'exit_code': exit_code, 'capture_complete': complete, 'probe': probe,
            'provider_requests': capture.ordinals.get('provider', 0),
            'inventory_sha256': digest((capture.root / 'inventory.json').read_bytes())}


def mock_mcp(request, enabled_tools):
    method = request.get('method')
    if method == 'notifications/initialized':
        return None
    if method == 'initialize':
        result = {'protocolVersion': '2025-03-26', 'capabilities': {'tools': {}},
                  'serverInfo': {'name': 'synthetic-capture-probe', 'version': '1'}}
    elif method == 'tools/list':
        result = {'tools': [{'name': name, 'description': 'Synthetic isolation probe; never queries a corpus.',
                             'inputSchema': {'type': 'object', 'properties': {'query': {'type': 'string'}},
                                             'required': ['query']}} for name in enabled_tools]}
    elif method in ('resources/list', 'resources/templates/list', 'prompts/list'):
        result = {('resources' if method == 'resources/list' else
                   'resourceTemplates' if method == 'resources/templates/list' else 'prompts'): []}
    else:
        raise ValueError('probe cannot execute native or model tools')
    return {'jsonrpc': '2.0', 'id': request['id'], 'result': result}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True)
    parser.add_argument('--capture', required=True)
    parser.add_argument('--probe', action='store_true')
    args = parser.parse_args()
    config = json.loads(Path(args.config).read_text())
    if args.probe:
        report = run_container(config, args.capture, probe=True)
    else:
        root = Path(config['evidence_root']).resolve()
        frozen = validate_execution_config(root, config)
        validate_environment_bindings(frozen, config, os.environ)
        record = RunRecord.create(root, config['assignment'], config['identity'], config['budgets'],
                                  config['freeze'], config['coordinator_session'])
        broker = NativeBroker(NativeHTTP(os.environ[config['native_url_env']],
                                        os.environ[config['native_token_env']] if config.get('native_token_env') else None),
                              record, config['enabled_tools'],
                              config.get('_observed_identities', {}).get('product'),
                              source_scope=config.get('_source_scope'))
        broker.display_cap = config['budgets']['display_bytes']
        try:
            broker.onboard()
            report = run_container(config, args.capture, broker, record)
        except BaseException as exc:
            try:
                record.stop(type(exc).__name__)
            except RuntimeError:
                pass
            raise
    print(json.dumps(report))


if __name__ == '__main__':
    main()
