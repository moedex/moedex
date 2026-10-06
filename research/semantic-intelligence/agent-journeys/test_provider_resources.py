"""Source-free resource accounting, dispatch and independent replay checks."""
import base64
from copy import deepcopy
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import provider_resources as resource
import isolated_solver as solver


def policy(**changes):
    value = dict(schema='provider-resource-policy-v1', max_requests=128,
                 max_request_bytes=8 << 20, max_response_bytes=16 << 20,
                 max_total_request_bytes=32 << 20, max_total_response_bytes=32 << 20,
                 max_observed_input_tokens=200000, max_observed_output_tokens=16000,
                 max_output_tokens=16000)
    return dict(value, **changes)


def response(input_tokens=10, output_tokens=5, **changes):
    value = dict(id='synthetic', status='completed', model='synthetic',
                usage=dict(input_tokens=input_tokens, output_tokens=output_tokens,
                           total_tokens=input_tokens + output_tokens,
                           input_tokens_details={'cached_tokens': input_tokens},
                           output_tokens_details={'reasoning_tokens': output_tokens}))
    return dict(value, **changes)


def receipt(body, **changes):
    return dict(dict(status=200, content_type='application/json', transport_complete=True,
                     body_bytes_observed=len(body)), **changes)


def retain(root, name, raw):
    (root / name).write_bytes(raw)


def archive(root, tracker, requests=1, body=None, meta=None):
    retain(root, 'provider-resource-policy.json', resource.canonical(tracker.policy))
    for ordinal in range(1, requests + 1):
        stem = 'provider-%04d' % ordinal
        raw = b'{"model":"synthetic"}'
        retain(root, stem + '.request.json', raw)
        try:
            upstream = tracker.begin(stem, raw)
        except resource.ProviderResourceStop:
            break
        retain(root, stem + '.upstream-request.json', upstream)
        payload = resource.canonical(response()) if body is None else body
        headers = receipt(payload) if meta is None else meta
        retain(root, stem + '.response.raw', payload)
        retain(root, stem + '.receipt.json', resource.canonical(headers))
        try:
            tracker.finish(stem, payload, headers)
        except resource.ProviderResourceStop:
            break
    retain(root, 'provider-resource-ledger.json', resource.canonical(tracker.summary()))


class ResourceTests(unittest.TestCase):
    def test_policy_and_strict_json_reject_ambiguous_limits(self):
        for changes in ({'max_requests': True}, {'max_requests': 0}, {'max_output_tokens': 15},
                        {'max_response_bytes': (64 << 20) + 1}, {'unknown': 1}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                resource.validate_policy(policy(**changes))
        for raw in (b'{"a":1,"a":2}', b'{"a":NaN}'):
            with self.assertRaises(ValueError):
                resource.load_json(raw)

    def test_usage_counts_cached_and_reasoning_once(self):
        tracker = resource.ProviderResources(policy())
        tracker.begin('provider-0001', b'{}')
        body = resource.canonical(response(100, 50))
        tracker.finish('provider-0001', body, receipt(body))
        self.assertEqual((tracker.input_tokens, tracker.output_tokens), (100, 50))
        self.assertTrue(tracker.summary()['all_reserved_usage_known'])

    def test_sse_terminal_duplicates_do_not_double_count_and_conflicts_fail(self):
        event = {'type': 'response.completed', 'response': response()}
        chunk = b'data: ' + resource.canonical(event) + b'\n\n'
        raw = chunk + chunk + b'data: [DONE]\n\n'
        self.assertEqual(resource.terminal_usage(raw, receipt(raw, content_type='text/event-stream'))['input_tokens'], 10)
        for bad in (chunk + b'data: broken\n\n',
                    chunk + b'data: ' + resource.canonical(dict(event, response=response(11))) + b'\n\n',
                    b'data: ' + resource.canonical(dict(event, response=response(status='failed'))) + b'\n\n'):
            with self.subTest(raw=bad), self.assertRaises(ValueError):
                resource.terminal_usage(bad, receipt(bad, content_type='text/event-stream'))

    def test_unknown_partial_and_error_usage_stops_without_zero_fill(self):
        values = [response(usage=None), response(usage={'input_tokens': True, 'output_tokens': 0, 'total_tokens': 1}),
                  response(usage={'input_tokens': 1, 'output_tokens': 1, 'total_tokens': 3}),
                  response(usage={'input_tokens': 1, 'output_tokens': 0, 'total_tokens': 1,
                                  'input_tokens_details': {'cached_tokens': 2}})]
        pairs = [(resource.canonical(value), {}) for value in values]
        pairs += [(resource.canonical(response()), {'transport_complete': False}),
                  (b'{"error":"synthetic"}', {'status': 429}), (b'broken', {})]
        for body, changes in pairs:
            tracker = resource.ProviderResources(policy())
            tracker.begin('provider-0001', b'{}')
            with self.subTest(body=body, changes=changes), self.assertRaisesRegex(resource.ProviderResourceStop, 'usage_unknown'):
                tracker.finish('provider-0001', body, receipt(body, **changes))
            self.assertFalse(tracker.summary()['all_reserved_usage_known'])
            with self.assertRaises(resource.ProviderResourceStop):
                tracker.begin('provider-0002', b'{}')

    def test_fixed_output_transform_retains_explicit_correct_bytes(self):
        tracker = resource.ProviderResources(policy())
        raw = b'{ "model": "synthetic" }'
        forwarded = tracker.begin('provider-0001', raw)
        self.assertEqual(resource.load_json(forwarded)['max_output_tokens'], 16000)
        self.assertNotEqual(raw, forwarded)
        explicit = b'{ "max_output_tokens": 16000 }'
        self.assertEqual(resource.ProviderResources(policy()).begin('provider-0001', explicit), explicit)
        for raw in (b'{"max_output_tokens":15999}', b'{"max_output_tokens":true}', b'{"max_output_tokens":16000.0}'):
            with self.assertRaisesRegex(resource.ProviderResourceStop, 'setting_mismatch'):
                resource.ProviderResources(policy()).begin('provider-0001', raw)

    def test_request_caps_reserve_nothing_and_exact_total_next_request_stops(self):
        for caps, raw, reason in [(policy(max_request_bytes=16), b'{}', 'request_bytes'),
                                  (policy(max_total_request_bytes=16), b'{}', 'total_request_bytes')]:
            tracker = resource.ProviderResources(caps)
            with self.assertRaisesRegex(resource.ProviderResourceStop, reason):
                tracker.begin('provider-0001', raw)
            self.assertEqual(tracker.request_count, 0)
        raw = b'{"max_output_tokens":16000}'
        tracker = resource.ProviderResources(policy(max_total_request_bytes=len(raw)))
        tracker.begin('provider-0001', raw)
        body = resource.canonical(response())
        tracker.finish('provider-0001', body, receipt(body))
        with self.assertRaisesRegex(resource.ProviderResourceStop, 'total_request_bytes'):
            tracker.begin('provider-0002', raw)
        self.assertEqual(tracker.request_count, 1)

    def test_exact_threshold_allows_response_and_refuses_next_request(self):
        body = resource.canonical(response())
        for changes, reason in [({'max_requests': 1}, 'count_cap'),
                                ({'max_observed_input_tokens': 10}, 'input_cap'),
                                ({'max_observed_output_tokens': 5}, 'output_cap'),
                                ({'max_total_response_bytes': len(body)}, 'total_response_bytes')]:
            tracker = resource.ProviderResources(policy(**changes))
            tracker.begin('provider-0001', b'{}')
            tracker.finish('provider-0001', body, receipt(body))
            with self.subTest(changes=changes), self.assertRaisesRegex(resource.ProviderResourceStop, reason):
                tracker.begin('provider-0002', b'{}')

    def test_crossing_and_terminal_failures_are_retained_and_stop(self):
        for changes, value, reason in [({'max_observed_input_tokens': 9}, response(), 'input_cap'),
                                      ({'max_observed_output_tokens': 4}, response(), 'output_cap'),
                                      ({'max_response_bytes': 16}, response(), 'response_bytes'),
                                      ({'max_total_response_bytes': 16}, response(), 'total_response_bytes'),
                                      ({'max_output_tokens': 16}, response(output_tokens=17), 'output_limit'),
                                      ({}, response(status='incomplete', incomplete_details={'reason': 'max_output_tokens'}), 'output_limit'),
                                      ({}, response(status='failed'), 'not_completed'),
                                      ({}, response(status='incomplete', incomplete_details=None), 'not_completed')]:
            tracker = resource.ProviderResources(policy(**changes))
            tracker.begin('provider-0001', b'{}')
            body = resource.canonical(value)
            with self.subTest(changes=changes, value=value), self.assertRaisesRegex(resource.ProviderResourceStop, reason):
                tracker.finish('provider-0001', body, receipt(body))
            self.assertEqual(tracker.response_bytes, len(body))
            self.assertEqual(tracker.events[-2]['response_sha256'], resource.digest(body))

    def test_success_request_rejection_response_failure_and_abort_replay(self):
        for mode in ('success', 'request', 'response', 'abort'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                tracker = resource.ProviderResources(policy(max_requests=1))
                if mode == 'abort':
                    retain(root, 'provider-resource-policy.json', resource.canonical(tracker.policy))
                    retain(root, 'provider-0001.request.json', b'{}')
                    retain(root, 'provider-0001.upstream-request.json', tracker.begin('provider-0001', b'{}'))
                    tracker.abort_pending()
                    retain(root, 'provider-resource-ledger.json', resource.canonical(tracker.summary()))
                else:
                    archive(root, tracker, requests=2 if mode == 'request' else 1,
                            body=b'broken' if mode == 'response' else None)
                result = resource.audit_capture(root, tracker.policy)
                self.assertTrue(result['raw_replay_complete'])
                self.assertEqual(result['summary'], tracker.summary())

    def test_replay_rejects_tampering_omission_reordering_and_unknown_files(self):
        for mutation in ('request', 'upstream', 'response', 'receipt', 'policy', 'events', 'omit', 'extra', 'symlink'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                tracker = resource.ProviderResources(policy())
                archive(root, tracker)
                if mutation in ('request', 'upstream', 'response', 'receipt', 'policy'):
                    name = {'request': 'provider-0001.request.json', 'upstream': 'provider-0001.upstream-request.json',
                            'response': 'provider-0001.response.raw', 'receipt': 'provider-0001.receipt.json',
                            'policy': 'provider-resource-policy.json'}[mutation]
                    retain(root, name, b'{}')
                elif mutation == 'events':
                    ledger = tracker.summary()
                    ledger['events'].reverse()
                    retain(root, 'provider-resource-ledger.json', resource.canonical(ledger))
                elif mutation == 'omit':
                    (root / 'provider-0001.response.raw').unlink()
                elif mutation == 'extra':
                    retain(root, 'provider-0002.request.json', b'{}')
                else:
                    (root / 'provider-0001.response.raw').unlink()
                    (root / 'provider-0001.response.raw').symlink_to(root / 'provider-0001.request.json')
                with self.assertRaises((ValueError, OSError)):
                    resource.audit_capture(root, tracker.policy)


class ResourceControllerTests(unittest.TestCase):
    def test_raw_retained_before_resource_stop_and_never_relayed(self):
        class Process:
            def __init__(self, message):
                read_fd, write_fd = os.pipe()
                os.write(write_fd, solver.canonical(message) + b'\n')
                os.close(write_fd)
                self.stdout = os.fdopen(read_fd, 'rb')
                self.stdin, self.code = io.BytesIO(), None
            def poll(self): return self.code
            def terminate(self): self.code = -15
            def wait(self, timeout): return self.code
        for mode in ('request', 'response', 'unknown', 'settings'):
            request = {'model': 'synthetic', 'reasoning': {'effort': 'high'}}
            if mode == 'settings': request['max_output_tokens'] = 42
            message = {'channel': 'provider', 'id': '1', 'method': 'POST', 'path': '/v1/responses',
                       'body_base64': base64.b64encode(solver.canonical(request)).decode()}
            process = Process(message)
            body = b'broken' if mode == 'unknown' else resource.canonical(response())
            limits = policy(**({'max_request_bytes': 16} if mode == 'request' else
                               {'max_observed_input_tokens': 9} if mode == 'response' else {}))
            config = dict(image='sha256:' + 'a' * 64, model='synthetic', reasoning_effort='high',
                          prompt='synthetic prompt', enabled_tools=['allowed'], timeout_seconds=30,
                          provider_url_env='FIXTURE_URL', provider_token_env='FIXTURE_TOKEN', _provider_resources=limits)
            try:
                with self.subTest(mode=mode), tempfile.TemporaryDirectory() as tmp, \
                        patch.object(solver.subprocess, 'Popen', return_value=process), \
                        patch.object(solver, 'provider_exchange', return_value=(body, receipt(body))) as exchange, \
                        patch.dict(os.environ, {'FIXTURE_URL': 'https://example.invalid', 'FIXTURE_TOKEN': 'synthetic'}):
                    root = Path(tmp) / 'capture'
                    with self.assertRaises(resource.ProviderResourceStop):
                        solver.run_container(config, root)
                    self.assertEqual(exchange.call_count, int(mode in ('response', 'unknown')))
                    self.assertEqual(len(process.stdin.getvalue().splitlines()), 1)
                    self.assertFalse(json.loads((root / 'inventory.json').read_text())['complete'])
                    self.assertTrue(resource.audit_capture(root, limits)['valid'])
                    relay = resource.audit_relay_journal(root)
                    self.assertTrue(relay['delivery_complete'])
                    self.assertEqual(len(relay['replies']), 1)
                    self.assertEqual(relay['replies'][0]['message']['channel'], 'start')
                    if exchange.called:
                        self.assertEqual((root / 'provider-0001.response.raw').read_bytes(), body)
                        self.assertEqual(json.loads(exchange.call_args.args[2])['max_output_tokens'], 16000)
                        self.assertEqual(exchange.call_args.kwargs['cap'], limits['max_response_bytes'])
            finally:
                process.stdout.close()

    def test_relay_journal_failure_before_write_blocks_and_after_write_is_unknown(self):
        class Process:
            def __init__(self):
                read_fd, write_fd = os.pipe()
                os.close(write_fd)
                self.stdout = os.fdopen(read_fd, 'rb')
                self.stdin, self.code = io.BytesIO(), None
            def poll(self): return self.code
            def terminate(self): self.code = -15
            def wait(self, timeout): return self.code
        original_event = solver.Capture.event
        for fail_channel in ('controller_reply', 'controller_reply_commit'):
            process = Process()
            def event(capture, value):
                if value['channel'] == fail_channel:
                    raise OSError('synthetic durable journal failure')
                return original_event(capture, value)
            config = dict(image='sha256:' + 'a' * 64, model='synthetic', reasoning_effort='high',
                          prompt='synthetic prompt', enabled_tools=['allowed'], timeout_seconds=30,
                          _provider_resources=policy())
            try:
                with self.subTest(channel=fail_channel), tempfile.TemporaryDirectory() as tmp, \
                        patch.object(solver.subprocess, 'Popen', return_value=process), \
                        patch.object(solver.Capture, 'event', event):
                    root = Path(tmp) / 'capture'
                    with self.assertRaisesRegex(OSError, 'durable journal failure'):
                        solver.run_container(config, root)
                    self.assertEqual(bool(process.stdin.getvalue()), fail_channel == 'controller_reply_commit')
                    relay = resource.audit_relay_journal(root)
                    self.assertEqual(relay['delivery_complete'], fail_channel == 'controller_reply')
                    self.assertEqual(len(relay['replies']), int(fail_channel == 'controller_reply_commit'))
                    self.assertFalse(json.loads((root / 'inventory.json').read_text())['complete'])
            finally:
                process.stdout.close()

    def test_relay_replay_rejects_wire_tampering_reorder_and_duplicate_commit(self):
        message = {'channel': 'response', 'id': '1', 'status': 200, 'body_base64': ''}
        wire = resource.canonical(message) + b'\n'
        attempt = dict(channel='controller_reply', controller_monotonic=1, reply_ordinal=1,
                       message=message, wire_sha256=resource.digest(wire), wire_bytes=len(wire))
        commit = {key: value for key, value in attempt.items() if key != 'message'}
        commit['channel'] = 'controller_reply_commit'
        for mutation in ('wire', 'order', 'duplicate', 'time', 'negative_time', 'bool_commit'):
            events = deepcopy([attempt, commit])
            if mutation == 'wire': events[0]['message']['body_base64'] = 'tampered'
            elif mutation == 'order': events.reverse()
            elif mutation == 'duplicate': events.append(events[-1])
            elif mutation == 'time': events[-1]['controller_monotonic'] = 0
            elif mutation == 'negative_time': events[0]['controller_monotonic'] = -1
            else: events[-1]['reply_ordinal'] = True
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as tmp:
                retain(Path(tmp), 'events.jsonl', b''.join(resource.canonical(value) + b'\n' for value in events))
                with self.assertRaises(ValueError): resource.audit_relay_journal(tmp)

    def test_freeze_binds_policy_fixed_setting_and_module_without_changing_legacy(self):
        from run_record import reference
        from test_contract import fixture
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            contract = fixture(root)
            retain(root, 'contract.json', solver.canonical(contract))
            settings = dict(model='synthetic', reasoning={'effort': 'high'}, tool_choice='auto',
                            parallel_tool_calls=False, text={'verbosity': 'low'}, store=False, stream=True,
                            include=['reasoning.encrypted_content'])
            frozen = dict(arm='a', isolation={'image_sha256': 'sha256:' + 'a' * 64},
                          model={'requested_alias': 'synthetic', 'settings': {'reasoning_effort': 'high',
                                 'reasoning_summary': 'none', 'provider_fields': settings}},
                          contract=reference(root, root / 'contract.json'), native_allowed_tools=['allowed'],
                          runners={name: solver.digest(Path(solver.__file__).with_name(name).read_bytes())
                                   for name in ('isolated_solver.py', 'run_record.py', 'native_http.py',
                                                'journey_clock.py', 'contract.py', 'compare.py')})
            config = dict(identity={'task': 'example', 'arm': 'a', 'contract_sha256': solver.digest(solver.canonical(contract)),
                                    'prompt_sha256': contract['tasks'][0]['prompt']['sha256']}, model='synthetic',
                          reasoning_effort='high', image=frozen['isolation']['image_sha256'], prompt='fixture prompt\n',
                          budgets=contract['budgets'], enabled_tools=['allowed'], timeout_seconds=600,
                          _provider_resources={'untrusted': True})
            def check():
                retain(root, 'freeze.json', solver.canonical(frozen))
                config['freeze'] = reference(root, root / 'freeze.json')
                return solver.validate_execution_config(root, config)
            check()
            self.assertNotIn('_provider_resources', config)
            frozen['provider_resources'] = policy()
            with self.assertRaisesRegex(ValueError, 'output limit'):
                check()
            settings['max_output_tokens'] = 16000
            with self.assertRaisesRegex(ValueError, 'executing runner differs'):
                check()
            frozen['runners']['provider_resources.py'] = solver.digest(Path(resource.__file__).read_bytes())
            check()
            self.assertEqual(config['_provider_resources'], policy())
            config['provider_resources'] = policy(max_requests=129)
            with self.assertRaisesRegex(ValueError, 'differs from frozen policy'):
                check()
            del config['provider_resources']
            settings['max_output_tokens'] = 42
            with self.assertRaisesRegex(ValueError, 'output limit'):
                check()


if __name__ == '__main__':
    unittest.main()
