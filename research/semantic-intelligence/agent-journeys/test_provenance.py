"""Synthetic observed-service evidence; these are not product measurements."""
import copy
import json
from pathlib import Path
import tempfile
import unittest

import provenance as p
from run_record import read_ref, reference


def observed_fixture(ref, base=None):
    """Build linked raw receipts and metadata with a caller-owned ref writer."""
    limits = ['Serving image and runtime closure are unverified.',
              'Requested and returned model names do not establish an immutable revision.']
    settings = {'reasoning_effort': 'high', 'reasoning_summary': 'none',
                'provider_fields': {'model': 'synthetic-model', 'stream': True}}
    def exchange(request, response):
        body = response if isinstance(response, str) else p.canonical(response).decode()
        return {'request': ref(request), 'response': ref(body),
                'receipt': ref({'status': 200, 'body_bytes_observed': len(body.encode()),
                                'transport_complete': True})}
    server = {'name': 'synthetic-native', 'version': 'observed'}
    catalog = {'tools': [{'name': 'lookup', 'inputSchema': {'type': 'object'}}]}
    exchanges = [
        exchange({'jsonrpc': '2.0', 'id': 1, 'method': 'initialize'},
                 {'jsonrpc': '2.0', 'id': 1, 'result': {'serverInfo': server}}),
        exchange({'jsonrpc': '2.0', 'method': 'notifications/initialized'}, ''),
        exchange({'jsonrpc': '2.0', 'id': 3, 'method': 'tools/list'},
                 {'jsonrpc': '2.0', 'id': 3, 'result': catalog})]
    model_exchange = exchange(dict(settings['provider_fields'], input=[]),
                              {'object': 'response', 'model': 'synthetic-returned-model', 'status': 'completed'})
    model = {'schema': 'observed-model-v1', 'observed_utc': '2026-01-01T00:00:00+00:00',
             'requested_alias': 'synthetic-model', 'provider_base_url_sha256': 'a' * 64,
             'settings_sha256': p.digest(p.canonical(settings)), 'returned_models': ['synthetic-returned-model'],
             'immutable_revision_verified': False, **model_exchange}
    product = {'schema': 'observed-product-v1', 'observed_utc': '2026-01-01T00:00:00+00:00',
               'endpoint_sha256': 'b' * 64, 'server_info': server, 'catalog_sha256': p.digest(p.canonical(catalog)),
               'native_exchanges': exchanges, 'immutable_serving_identity_verified': False,
               'runtime_dependency_closure_verified': False}
    authorization = {'schema': 'observed-authorization-v1', 'mode': p.OBSERVED, 'authorized_by': 'user',
                     'decision': 'Permit observed identities with disclosed limits.',
                     'requirements': ['isolation', 'budgets', 'raw-capture', 'independent-scoring'],
                     'reproducibility_limits': limits}
    frozen = copy.deepcopy(base or {})
    frozen.update(provenance={'mode': p.OBSERVED, 'authorization': ref(authorization),
                              'reproducibility_limits': limits},
                  model={'requested_alias': 'synthetic-model', 'provider_base_url_sha256': 'a' * 64,
                         'revision': None, 'settings': settings, 'observation': ref(model)},
                  product={'endpoint_sha256': 'b' * 64, 'observation': ref(product)})
    return frozen


class ProvenanceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.serial = 0
        self.frozen = observed_fixture(self.ref)

    def ref(self, value):
        self.serial += 1
        raw = value.encode() if isinstance(value, str) else p.canonical(value)
        path = self.root / ('proof-%d' % self.serial)
        path.write_bytes(raw)
        return reference(self.root, path)

    def edit(self, section, update):
        ref = self.frozen[section]['observation']
        value = json.loads(read_ref(self.root, ref))
        update(value)
        self.frozen[section]['observation'] = self.ref(value)

    def test_explicit_mode_validates_and_identity_binding_is_hashed(self):
        self.assertEqual(p.validate(self.root, self.frozen, read_ref), [])
        binding = p.identity_binding(self.root, self.frozen, read_ref)
        self.assertEqual(binding['policy'], p.OBSERVED)
        self.assertEqual(binding['model_observation_sha256'], self.frozen['model']['observation']['sha256'])
        self.assertFalse(p.observations(self.root, self.frozen, read_ref)['model']['immutable_revision_verified'])

    def test_absent_policy_stays_strict_and_unknown_or_malformed_fail(self):
        self.assertEqual(p.mode({}), p.IMMUTABLE)
        self.assertEqual(p.mode({'provenance': {'mode': p.IMMUTABLE}}), p.IMMUTABLE)
        for policy in (None, [], {}, {'mode': 'latest'}, {'mode': None}):
            with self.subTest(policy=policy):
                self.assertTrue(p.validate(self.root, {'provenance': policy}, read_ref))

    def test_missing_mutated_or_forged_authorization_fails(self):
        original = copy.deepcopy(self.frozen)
        for update in ({'authorized_by': 'solver'}, {'decision': ''}, {'requirements': []},
                       {'mode': p.IMMUTABLE}, {'reproducibility_limits': []}, {'schema': 'wrong'}):
            self.frozen = copy.deepcopy(original)
            authorization = json.loads(read_ref(self.root, self.frozen['provenance']['authorization']))
            authorization.update(update)
            self.frozen['provenance']['authorization'] = self.ref(authorization)
            self.assertTrue(p.validate(self.root, self.frozen, read_ref), update)
        self.frozen = copy.deepcopy(original)
        del self.frozen['provenance']['authorization']
        self.assertTrue(p.validate(self.root, self.frozen, read_ref))
        self.frozen = original
        (self.root / self.frozen['provenance']['authorization']['path']).write_text('changed')
        self.assertTrue(p.validate(self.root, self.frozen, read_ref))

    def test_observation_bindings_and_limits_fail_closed(self):
        original = copy.deepcopy(self.frozen)
        cases = [('model', {'requested_alias': 'other'}), ('model', {'provider_base_url_sha256': 'c' * 64}),
                 ('model', {'settings_sha256': 'c' * 64}), ('model', {'returned_models': []}),
                 ('model', {'immutable_revision_verified': True}), ('model', {'observed_utc': '2026-01-01'}),
                 ('product', {'endpoint_sha256': 'c' * 64}), ('product', {'catalog_sha256': 'c' * 64}),
                 ('product', {'server_info': {'name': 'other'}}), ('product', {'native_exchanges': []}),
                 ('product', {'runtime_dependency_closure_verified': True})]
        for section, update in cases:
            self.frozen = copy.deepcopy(original)
            self.edit(section, lambda value: value.update(update))
            self.assertTrue(p.validate(self.root, self.frozen, read_ref), (section, update))
        self.frozen = original
        self.frozen['provenance']['reproducibility_limits'] = []
        self.assertTrue(p.validate(self.root, self.frozen, read_ref))

    def test_raw_exchange_response_and_receipt_are_verified(self):
        for field, update in (('response', {'model': 'drifted'}),
                              ('receipt', {'status': 500, 'body_bytes_observed': 1, 'transport_complete': True}),
                              ('request', {'model': 'drifted', 'stream': True})):
            original = copy.deepcopy(self.frozen)
            self.edit('model', lambda value: value.update({field: self.ref(update)}))
            self.assertTrue(p.validate(self.root, self.frozen, read_ref), field)
            self.frozen = original

    def test_observation_cannot_add_provider_settings_or_use_incomplete_success(self):
        original = copy.deepcopy(self.frozen)
        observation = json.loads(read_ref(self.root, self.frozen['model']['observation']))
        request = json.loads(read_ref(self.root, observation['request']))
        for key, value in (('temperature', 0), ('max_output_tokens', 512), ('previous_response_id', 'old')):
            self.frozen = copy.deepcopy(original)
            self.edit('model', lambda model: model.update(request=self.ref(dict(request, **{key: value}))))
            self.assertTrue(p.validate(self.root, self.frozen, read_ref), key)
        self.frozen = original
        body = p.canonical({'type': 'response.created', 'response': {
            'status': 'in_progress', 'model': 'synthetic-returned-model'}}).decode()
        self.edit('model', lambda model: model.update(
            response=self.ref(body), receipt=self.ref({'status': 200, 'body_bytes_observed': len(body.encode()),
                                                      'transport_complete': True})))
        self.assertTrue(p.validate(self.root, self.frozen, read_ref))

    def test_response_models_json_and_multiline_sse(self):
        self.assertEqual(p.response_models(b'{"model":"a"}'), ['a'])
        self.assertEqual(p.response_models(b'event: response.completed\r\ndata: {"response":\r\ndata: {"model":"b"}}\r\n\r\n'), ['b'])
        self.assertEqual(p.response_models(b'data: [DONE]\n\n'), [])

    def test_success_observation_rejects_malformed_or_invalid_identity_events(self):
        original = copy.deepcopy(self.frozen)
        completed = p.canonical({'type': 'response.completed', 'response': {
            'status': 'completed', 'model': 'synthetic-returned-model'}})
        for prefix in (b'data: broken-json', b'data: {"response":{"model":17}}',
                       b'data: {"response":{"model":""}}', b'data: {"model":null}'):
            body = (prefix + b'\n\ndata: ' + completed + b'\n\n').decode()
            self.frozen = copy.deepcopy(original)
            self.edit('model', lambda model: model.update(
                response=self.ref(body), receipt=self.ref({'status': 200, 'body_bytes_observed': len(body.encode()),
                                                          'transport_complete': True})))
            self.assertTrue(p.validate(self.root, self.frozen, read_ref), prefix)


if __name__ == '__main__':
    unittest.main()
