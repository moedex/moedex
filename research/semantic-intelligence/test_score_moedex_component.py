import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('moedex_scorer', Path(__file__).with_name('score-moedex-component.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class Scoring(unittest.TestCase):
    def setUp(self):
        self.key = lambda desc: {'language': 'csharp', 'namespace_kind': 'project',
                                 'namespace': 'Repo/src/P.csproj', 'descriptor': desc,
                                 'descriptor_kind': 'documentation_comment_id'}
        self.source = {'project': 'src/P.csproj', 'path': 'src/A.cs',
                       'byte_offset': 10, 'byte_length': 7, 'source_sha256': 'source-hash'}
        self.case = {'id': 'publish', 'family': 'domain_observation', 'source': self.source,
                     'owner': self.key('M:N.A.Send'), 'expected_kind': 'message_publish',
                     'expected_rule': 'csharp-framework-v1',
                     'expected_targets': [{'role': 'message', 'symbol': self.key('T:N.Message')}]}
        self.protocol = {'classification': 'test', 'positive_cases': [self.case], 'negative_cases': [],
                         'project_configuration': [{'project': 'src/P.csproj'}],
                         'reviewed_source_closure': {'paths': ['src/A.cs'], 'source_sha256': {'src/A.cs': 'source-hash'}}}
        self.artifact = {
            'sources': [{'id': 'source', 'path': 'src/A.cs', 'raw_sha256': 'source-hash'}],
            'contexts': [{'id': 'c1', 'project': 'src/P.csproj', 'snapshot_id': 'snapshot', 'source_ids': ['source']}],
            'symbols': [{'id': name, 'key': self.key(desc)} for name, desc in (
                ('owner', 'M:N.A.Send'), ('message', 'T:N.Message'), ('wrong', 'T:N.Wrong'),
                ('api', 'M:N.Bus.Publish(N.Message)'), ('interface', 'M:N.I.Send'), ('type', 'T:N.A'))],
            'occurrences': [{'id': 'o1', 'context_id': 'c1', 'source_id': 'source',
                             'role': 'reference', 'kind': 'invocation', 'offset': 10, 'length': 7}],
            'bindings': [{'id': 'b1', 'occurrence_id': 'o1', 'status': 'resolved',
                          'symbol_id': 'api', 'enclosing_symbol_id': 'owner',
                          'method': 'roslyn-semantic-model', 'extractor': 'msbuild-roslyn', 'extractor_version': '6',
                          'domain_facts': [{'kind': 'message_publish', 'rule': 'csharp-framework-v1',
                                            'evidence_scope': 'compile_time',
                                            'targets': [{'role': 'message', 'symbol_id': 'message'}]}]}]}

    def report(self):
        return m.score(self.protocol, self.artifact)

    def outcome(self):
        return self.report()['positive_cases'][0]['outcome']

    def test_exact_native_evidence(self):
        self.assertEqual(self.outcome(), 'supported_exact_occurrence')

    def test_wrong_identity_scope_and_span_do_not_match(self):
        original = copy.deepcopy(self.artifact)
        for mutate in (
            lambda: self.artifact['bindings'][0].update(enclosing_symbol_id='wrong'),
            lambda: self.artifact['symbols'][1]['key'].update(namespace='Other/P.csproj'),
            lambda: self.artifact['occurrences'][0].update(offset=11),
            lambda: self.artifact['bindings'][0].update(status='unresolved'),
        ):
            self.artifact = copy.deepcopy(original)
            mutate()
            self.assertNotEqual(self.outcome(), 'supported_exact_occurrence')

    def test_wrong_lifetime_rejected(self):
        self.case['expected_lifetime'] = 'scoped'
        self.artifact['bindings'][0]['domain_facts'][0]['lifetime'] = 'singleton'
        self.assertEqual(self.outcome(), 'wrong_assertion')

    def test_missing_variant_evidence_is_incomplete(self):
        self.artifact['contexts'].append({**self.artifact['contexts'][0], 'id': 'c2'})
        self.assertEqual(self.outcome(), 'capture_incomplete')

    def test_negative_requires_native_site_in_every_context(self):
        self.protocol['negative_cases'] = [{'id': 'negative', 'source': self.source}]
        self.artifact['bindings'][0]['domain_facts'] = []
        self.assertEqual(self.report()['negative_controls'][0]['outcome'], 'no_forbidden_assertion_observed')
        self.artifact['bindings'] = []
        self.assertEqual(self.report()['negative_controls'][0]['outcome'], 'capture_incomplete')

    def test_wrapper_must_not_be_promoted_to_direct_publish(self):
        self.protocol['negative_cases'] = [{'id': 'wrapper', 'source': self.source,
                                           'caller_owner': self.case['owner'], 'message': self.key('T:N.Message')}]
        self.assertEqual(self.report()['negative_controls'][0]['outcome'], 'wrong_assertion')
        self.artifact['bindings'][0]['domain_facts'][0]['targets'][0]['symbol_id'] = 'wrong'
        self.assertEqual(self.report()['negative_controls'][0]['outcome'], 'no_forbidden_assertion_observed')

    def test_additional_fact_on_matched_binding_stays_unscored(self):
        facts = self.artifact['bindings'][0]['domain_facts']
        extra = copy.deepcopy(facts[0])
        extra['targets'][0]['symbol_id'] = 'wrong'
        facts.append(extra)
        report = self.report()
        self.assertEqual(report['positive_cases'][0]['outcome'], 'supported_exact_occurrence')
        self.assertEqual(len(report['unscored_native_facts']), 1)
        self.assertEqual(report['unscored_native_facts'][0]['ordinal'], 1)

    def test_implementation_requires_exact_member_and_type(self):
        self.protocol['positive_cases'] = [{'id': 'impl', 'family': 'method_implementation', 'source': self.source,
            'implementation_method': self.key('M:N.A.Send'), 'interface_member': self.key('M:N.I.Send'),
            'implementing_type': self.key('T:N.A')}]
        self.artifact['occurrences'][0].update(role='declaration', kind='declaration')
        binding = self.artifact['bindings'][0]
        binding.update(symbol_id='owner', domain_facts=[], implementation_facts=[{
            'kind': 'interface_method_implementation', 'rule': 'csharp-interface-v1',
            'evidence_scope': 'compile_time', 'interface_symbol_id': 'interface', 'implementing_type_symbol_id': 'type'}])
        self.assertEqual(self.outcome(), 'supported_exact_occurrence')
        binding['implementation_facts'][0]['interface_symbol_id'] = 'type'
        self.assertEqual(self.outcome(), 'wrong_assertion')

    def test_source_coverage_must_exist_in_native_context(self):
        m.validate_source_coverage(self.protocol, self.artifact)
        self.artifact['contexts'][0]['source_ids'] = []
        with self.assertRaisesRegex(ValueError, 'native source closure mismatch'):
            m.validate_source_coverage(self.protocol, self.artifact)


if __name__ == '__main__':
    unittest.main()
