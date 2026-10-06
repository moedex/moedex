#!/usr/bin/env python3
"""Synthetic-only assembly tests. All fixtures live in disposable local directories."""
import contextlib
import base64
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import shutil
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
MODULE = HERE / 'assemble_pilot_results.py'
spec = importlib.util.spec_from_file_location('assembler', MODULE)
assembly = importlib.util.module_from_spec(spec)
spec.loader.exec_module(assembly)
NEUTRAL_SCHEMA = HERE / 'broader_claim.py'
CRITERIA = {'all_required_correct', 'physical_provenance', 'claim_citation_accuracy',
            'no_critical_unsupported_claims', 'source_scope_complete'}


class AssemblyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix='results-assembly-synthetic-', dir=HERE)
        cls.root = Path(cls.directory.name)
        cls.changed = {}
        schema_path = cls.root / assembly.SCHEMA_PATH
        schema_path.parent.mkdir(parents=True)
        shutil.copyfile(NEUTRAL_SCHEMA, schema_path)
        cls.schema_sha = assembly.sha(schema_path.read_bytes())

        def put(name, value):
            path = cls.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(assembly.canonical(value) + b'\n')
            return assembly.ref_for(cls.root, name)
        cls.put_initial = staticmethod(put)
        receipt = put('reviews/synthetic-policy.json', {'synthetic': True})
        corpus = [{'repository': 'synthetic-repository', 'commit': '1' * 40, 'manifest_sha256': '2' * 64}]
        tasks = []
        for index in range(60):
            kind = 'kind-' + str(index % 5)
            tasks.append({'id': f'task-{index:02d}', 'stratum': kind, 'source_family': f'family-{index:02d}',
                          'system': f'system-{index:02d}', 'domain': 'synthetic', 'language': 'synthetic',
                          'task_kind': kind, 'source_scope': corpus, 'prompt_sha256': assembly.sha(str(index).encode()),
                          'rubric_sha256': assembly.sha(('rubric-' + str(index)).encode()),
                          'witness_sha256s': [assembly.sha(('witness-' + str(index)).encode())]})
        plan = {'schema': 'broader-claim-plan-v1', 'plan_id': 'synthetic-only', 'phase': 'pilot',
                'arms': ['A', 'B'], 'repetitions': 3, 'sample_seed': 5, 'frame_sha256': '3' * 64,
                'corpus': corpus, 'strata': [{'id': f'kind-{i}', 'domain': 'synthetic', 'language': 'synthetic',
                                            'task_kind': f'kind-{i}', 'count': 12} for i in range(5)],
                'task_kind_weights': {f'kind-{i}': .2 for i in range(5)},
                'excluded': {'source_families': [], 'witness_sha256s': []},
                'governance': {'frozen_before_answers': True, 'sample_size_final': True, 'fixed_schedule': True, 'prior_pilot': None},
                'readiness': {'setup_ready': True, **{field: receipt for field in
                    ('source_only_frame_review', 'source_pin_alignment_review', 'raw_capture_policy',
                     'independent_scoring_policy', 'blinding_review', 'comparability_policy')}},
                'bootstrap': {'method': 'shared-exp1-cluster-weighted-percentile-v1', 'seed': 7, 'replicates': 1000,
                              'confidence': .95, 'practical_margin': .05},
                'coverage': {'families': 30, 'systems': 20, 'families_per_kind': 10, 'systems_per_kind': 8,
                             'family_effective_per_kind': 10, 'system_effective_per_kind': 8, 'max_cluster_share_per_kind': .2}}
        plan_ref = put('plan/plan.frozen.json', plan)
        sample_ref = put('plan/sample.frozen.json', {'schema': 'broader-claim-sample-v1',
                         'sampling_method': 'sha256-priority-within-source-stratum-v1', 'plan_sha256': assembly.sha(assembly.canonical(plan)),
                         'frame_sha256': plan['frame_sha256'], 'tasks': tasks})
        cls.machine_ref = put('reviews/synthetic-machine-report.json', {'schema': 'synthetic-report', 'errors': ['preserve this diagnostic']})
        freeze = put('freezes/synthetic.json', {'schema': 'synthetic-freeze'})
        slots = []
        for task in tasks:
            for arm in ('A', 'B'):
                for rep in range(1, 4):
                    name = f"{arm}-{task['id']}-r{rep:02d}"
                    solver = 'synthetic-solver-' + name
                    config = put('configs/' + name + '.json', {'assignment': name, 'opaque_prompt': 'synthetic opaque content'})
                    slots.append({'assignment': name, 'capture': 'captures/' + name, 'config': config, 'task_id': task['id'],
                                  'arm': arm, 'repetition': rep, 'task_sha256': assembly.sha(assembly.canonical(task)), 'solver_id': solver})
        packet = {'schema': 'broader-pilot-launch-packet-v1', 'phase': 'pilot', 'plan': plan_ref, 'sample': sample_ref, 'attempts': slots}
        cls.packet_ref = put('packet/launch-packet.json', packet)
        cls.slots = slots
        cls.slot = slots[0]
        cls.name = cls.slot['assignment']
        for slot in slots:
            name = slot['assignment']
            assignment = put(name + '/assignment.json', {'schema': 'native-run-record-v1', 'assignment': name,
                         'identity': {'task': slot['task_id'], 'arm': slot['arm'], 'solver_id': slot['solver_id']},
                         'freeze': freeze, 'launch_blockers': ['synthetic retained diagnostic']})
            intent = put('execution/' + name + '.intent.json', {'assignment': name, 'wave': 1, 'launched_unix': 1.0})
            result = put('execution/' + name + '.result.json', {'assignment': name, 'exit_code': 0, 'finished_unix': 2.0})
            inventory = put(slot['capture'] + '/inventory.json', {'complete': True, 'synthetic': True})
            final = put(name + '/blobs/000001.answer', {'synthetic': 'own final'})
            event = put(name + '/events/000001.json', {'event': 'answer', 'answer': {'path': 'blobs/000001.answer', 'sha256': final['sha256']}})
            bindings = [cls.packet_ref, plan_ref, sample_ref, slot['config']]
            raw = {'schema': 'broader-pilot-assignment-raw-review-v1', 'assignment': name, 'reviewer_id': 'synthetic-raw-reviewer',
                   'solver_id': slot['solver_id'], 'task_id': slot['task_id'], 'arm': slot['arm'], 'repetition': slot['repetition'],
                   'task_sha256': slot['task_sha256'], 'final': final, 'outcome': 'answered', 'failure_class': None,
                   'classification_basis': 'Synthetic immutable own final and terminal result.',
                   'evidence_refs': bindings + [assignment, freeze, intent, result, inventory, event, final],
                   'machine_review': {'artifact': cls.machine_ref, 'assignment': name},
                   'semantic_scoring': 'excluded_raw_review_only', 'access_loss_confirmed': False, 'scope_exposure_confirmed': False,
                   'shared_harness_defect_suspected': False, 'observed_utc': '2026-10-06T00:00:00Z',
                   'original_decision': {'outcome': 'answered', 'notes': 'Retain original synthetic decision.'},
                   'manual_review': {'all_retained_provider_native_bodies_read': True, 'all_terminal_failed_partial_late_bodies_read': True,
                        'identity_image_settings_isolation_verified': True, 'no_feedback_to_solver': True,
                        'raw_scope_display_and_selector_replay_verified': True, 'resource_and_relay_decisions_verified': True,
                        'notes': 'Synthetic only; no provider/product used.'},
                   'raw_review_fields': {'reviewer_id': 'synthetic-raw-reviewer', 'independent': True, 'complete': True,
                        'task_sha256': slot['task_sha256'], 'final_sha256': final['sha256'], 'capture_integrity': True,
                        'classification_confirmed': True}}
            source = {'schema': 'broader-pilot-assignment-source-review-v1', 'assignment': name,
                      'reviewer_id': 'synthetic-fresh-source-reviewer', 'solver_id': slot['solver_id'], 'task_sha256': slot['task_sha256'],
                      'final': final, 'review_history': {'no_task_authorship': True, 'no_prospective_source_or_gold_review': True,
                                                       'no_feedback_to_solver': True},
                      'applicability': 'final_reviewed', 'required_claims': [{'id': 'synthetic-claim', 'label': 'correct'}],
                      'material_claims_and_citations': [{'synthetic': True}], 'evidence_refs': bindings + [final],
                      'source_review_fields': {'reviewer_id': 'synthetic-fresh-source-reviewer', 'independent': True, 'complete': True,
                           'task_sha256': slot['task_sha256'], 'final_sha256': final['sha256'], 'criteria': dict.fromkeys(CRITERIA, True)},
                      'original_decision': {'synthetic': 'complete declared label only'}, 'limitations': 'Synthetic only.'}
            put('reviews/attempt-raw/' + name + '.json', raw)
            put('reviews/attempt-source/' + name + '.json', source)
        declarations = {'packet': cls.packet_ref, 'plan': plan_ref, 'sample': sample_ref, 'reviewer_id': 'synthetic-postrun-reviewer',
                        'independent': True, 'complete': True, 'evidence_refs': [cls.machine_ref], 'reasoning': 'Synthetic independent receipt.',
                        'original_decision': 'Retain explicit declaration.', 'observed_utc': '2026-10-06T00:00:00Z'}
        cls.access_ref = put('reviews/postrun-equal-access.json', dict(declarations,
                            schema='broader-pilot-postrun-equal-access-review-v1', established=True))
        cls.harness_ref = put('reviews/postrun-shared-harness.json', dict(declarations,
                             schema='broader-pilot-postrun-shared-harness-review-v1', confirmed=False, reviewed_causes=[]))

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def setUp(self):
        self.backups = {}
        self.packet_ref = copy.deepcopy(type(self).packet_ref)
        self.access_ref = copy.deepcopy(type(self).access_ref)
        self.harness_ref = copy.deepcopy(type(self).harness_ref)

    def tearDown(self):
        for path, previous in self.backups.items():
            if previous is None:
                path.unlink(missing_ok=True)
            else:
                path.write_bytes(previous)

    def data(self, name):
        return assembly.load((self.root / name).read_bytes())

    def replace(self, name, value):
        path = self.root / name
        if path not in self.backups:
            self.backups[path] = path.read_bytes() if path.exists() else None
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(assembly.canonical(value) + b'\n')
        return assembly.ref_for(self.root, name)

    def remove(self, name):
        path = self.root / name
        if path not in self.backups:
            self.backups[path] = path.read_bytes()
        path.unlink()

    def change_raw(self, **updates):
        name = 'reviews/attempt-raw/' + self.name + '.json'
        value = self.data(name)
        value.update(updates)
        return self.replace(name, value)

    def change_source(self, **updates):
        name = 'reviews/attempt-source/' + self.name + '.json'
        value = self.data(name)
        value.update(updates)
        return self.replace(name, value)

    def run_assembly(self, selection_ref=None, history_ref=None):
        return assembly.assemble(self.root, self.packet_ref, self.access_ref, self.harness_ref,
                                 self.schema_sha, selection_ref, history_ref)

    def history_manifest(self):
        originals = [assembly.ref_for(self.root, 'reviews/attempt-source/' + slot['assignment'] + '.json')
                     for slot in self.slots]
        receipt = {'schema': 'fresh-pilot-semantic-reviewer-history-attestation-v1',
                   'reviewer_id': 'synthetic-fresh-source-reviewer', 'packet': self.packet_ref,
                   'review_history': dict.fromkeys(assembly.HISTORY_FLAGS, True),
                   'original_review_refs': originals, 'disclosure': 'Synthetic explicit author declaration.',
                   'original_decision': 'Fresh history attested.', 'observed_utc': '2026-10-06T00:00:00+00:00'}
        ref = self.replace('reviews/history/synthetic.json', receipt)
        return self.replace('reviews/source-history-manifest.json', {
            'schema': 'broader-pilot-source-history-manifest-v1', 'packet': self.packet_ref, 'attestations': [ref]})

    def test_root_relative_answer_is_confined_and_retains_exact_original(self):
        path = self.name + '/events/000001.json'
        value = self.data(path)
        value['answer']['path'] = self.name + '/' + value['answer']['path']
        self.replace(path, value)
        final, _, _ = assembly.terminal_evidence(self.root, self.slot)
        self.assertEqual(final['path'], self.name + '/blobs/000001.answer')
        for path_value in ('../foreign.answer', 'blobs/../assignment.json', 'B-task-00-r01/blobs/000001.answer',
                           self.name + '/assignment.json', '/tmp/answer'):
            with self.subTest(path=path_value):
                value['answer']['path'] = path_value
                self.replace(path, value)
                with self.assertRaises(ValueError):
                    assembly.terminal_evidence(self.root, self.slot)

    def journal_selector(self, channel='answer', owner=None):
        final = self.data('reviews/attempt-source/' + self.name + '.json')['final']
        raw = assembly.read_ref(self.root, final)
        path = 'captures/' + (owner or self.name) + '/events.jsonl'
        self.replace(path, {'channel': channel, 'body_base64': base64.b64encode(raw).decode()})
        return {'path': path, 'sha256': final['sha256'], 'line': 1, 'field': 'body_base64'}

    def test_journal_final_selector_preserves_body_and_file_hash_domains(self):
        selector = self.journal_selector()
        source_ref = self.change_source(final=selector)
        source, fields, refs = assembly.review_artifact(self.root, self.name, 'source', self.slot,
            {s['solver_id'] for s in self.slots}, {}, self.data(source_ref['path'])['evidence_refs'][-1])
        self.assertEqual(source['final'], selector)
        self.assertIn(assembly.ref_for(self.root, selector['path']), refs)
        self.assertEqual(fields['artifact'], source_ref)
        for change in ({'line': 2}, {'line': True}, {'sha256': '0' * 64}, {'field': 'body'}, {'path': None}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                assembly.selected_body(self.root, dict(selector, **change))

    def test_journal_final_cannot_select_foreign_or_nonfinal_body(self):
        final = self.data('reviews/attempt-source/' + self.name + '.json')['final']
        for selector in (self.journal_selector(channel='final'), self.journal_selector(owner='B-task-00-r01')):
            self.change_source(final=selector)
            with self.assertRaises(ValueError):
                assembly.review_artifact(self.root, self.name, 'source', self.slot,
                    {s['solver_id'] for s in self.slots}, {}, final)

    def test_byte_descriptors_require_exact_integer_length_and_known_keys(self):
        ref = self.machine_ref
        size = len(assembly.read_ref(self.root, ref))
        self.assertEqual(assembly.references(self.root, dict(ref, bytes=size)), [ref])
        for extras in ({'bytes': True}, {'bytes': size + 1}, {'bytes': size, 'unknown': True}, {'unknown': 1}):
            with self.subTest(extras=extras), self.assertRaises(ValueError):
                assembly.references(self.root, dict(ref, **extras))

    def test_hash_bound_history_attestation_retains_narrative_and_grouped_findings(self):
        original = self.change_source(review_history='Original narrative must remain unchanged.',
                                      material_claims_and_citations={'groups': [{'label': 'unchanged'}]})
        manifest = self.history_manifest()
        before = assembly.read_ref(self.root, original)
        result, audit = self.run_assembly(history_ref=manifest)
        self.assertEqual(assembly.read_ref(self.root, original), before)
        self.assertEqual(audit['source_history_manifest'], manifest)
        self.assertIn('source_history_attestation', audit['attempts'][0])
        self.assertEqual(result['attempts'][0]['source_review']['criteria'], dict.fromkeys(CRITERIA, True))

    def test_history_manifest_rejects_missing_changed_foreign_or_duplicate_originals(self):
        manifest_ref = self.history_manifest()
        manifest = self.data(manifest_ref['path']);ref = manifest['attestations'][0];original = self.data(ref['path'])
        for mode in ('missing', 'changed', 'foreign', 'duplicate', 'solver', 'packet', 'false_flag'):
            value = copy.deepcopy(original)
            if mode == 'missing': value['original_review_refs'].pop()
            if mode == 'changed': value['original_review_refs'][0]['sha256'] = '0' * 64
            if mode == 'foreign': value['original_review_refs'][0] = self.machine_ref
            if mode == 'duplicate': value['original_review_refs'].append(value['original_review_refs'][0])
            if mode == 'solver': value['reviewer_id'] = self.slot['solver_id']
            if mode == 'packet': value['packet'] = self.machine_ref
            if mode == 'false_flag': value['review_history']['no_task_authorship'] = False
            manifest['attestations'] = [self.replace(ref['path'], value)]
            changed_ref = self.replace(manifest_ref['path'], manifest)
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                assembly.source_history_attestations(self.root, changed_ref, self.packet_ref,
                                                     self.slots, {s['solver_id'] for s in self.slots})

    def test_history_attestation_cannot_override_false_original_flags(self):
        self.change_source(review_history=dict.fromkeys(assembly.HISTORY_FLAGS, False))
        with self.assertRaisesRegex(ValueError, 'contradicts'):
            self.run_assembly(history_ref=self.history_manifest())

    def test_duplicate_history_author_rejected(self):
        ref = self.history_manifest();value = self.data(ref['path']);value['attestations'] *= 2
        changed_ref = self.replace(ref['path'], value)
        with self.assertRaisesRegex(ValueError, 'duplicate/solver'):
            assembly.source_history_attestations(self.root, changed_ref, self.packet_ref,
                                                 self.slots, {s['solver_id'] for s in self.slots})

    def test_source_omission_hash_is_never_interpreted_as_archive_file_hash(self):
        value = {'repository': 'synthetic-repository', 'path': 'src/example.cs', 'source_line': 3,
                 'body_field': 'source', 'native_ordinal': 2, 'utf8_bytes': 12, 'sha256': '1' * 64}
        self.assertEqual(assembly.references(self.root, value), [])
        self.assertEqual(assembly.references(self.root, dict(value, display_line_offset=0)), [])
        for updates in ({'sha256': 'unknown'}, {'source_line': True}, {'path': '../file'},
                        {'display_line_offset': -1}, {'unknown': True}):
            with self.subTest(updates=updates), self.assertRaises(ValueError):
                assembly.references(self.root, dict(value, **updates))

    def test_sensitive_safe_manual_diagnostics_preserve_original_flags_and_refs(self):
        path = 'reviews/attempt-raw/' + self.name + '.json'
        raw = self.data(path)
        safe = {'authorization': self.machine_ref, 'helper': self.machine_ref, 'omitted_line_displays': [],
                'original_bytes_mechanically_verified': True, 'sensitive_literal_values_directly_read': False,
                'dependent_raw_fact_limit': 'No original decisions depend on omitted literals.'}
        raw['manual_review']['sensitive_safe_representation'] = safe
        original = self.replace(path, raw)
        result, audit = self.run_assembly()
        self.assertEqual(result['attempts'][0]['raw_review']['artifact'], original)
        self.assertIn(self.machine_ref, audit['attempts'][0]['raw_evidence_refs'])
        for updates in ({'original_bytes_mechanically_verified': False},
                        {'sensitive_literal_values_directly_read': 'false'}, {'unknown': True}):
            raw['manual_review']['sensitive_safe_representation'] = dict(safe, **updates)
            self.replace(path, raw)
            with self.subTest(updates=updates), self.assertRaises(ValueError):
                self.run_assembly()

    def test_manual_diagnostics_cannot_replace_required_checks_or_add_unknown_fields(self):
        path = 'reviews/attempt-raw/' + self.name + '.json'
        original = self.data(path)
        for key, value in (('unknown', True), ('sensitive_literal_values_directly_read', 'false'),
                           ('omitted_context_lines', {}), ('raw_body_review_representation', '')):
            raw = copy.deepcopy(original);raw['manual_review'][key] = value
            self.replace(path, raw)
            with self.subTest(key=key), self.assertRaises(ValueError): self.run_assembly()

    def test_nonterminal_client_error_and_opaque_diagnostics_are_retained(self):
        path = 'reviews/attempt-raw/' + self.name + '.json';raw = self.data(path)
        error = {'captured_host_native_call_present': False, 'exact_cause': 'unestablished',
                 'preceding_provider_ordinal': 2, 'provider_following_request': self.machine_ref, 'terminal_stop': False}
        raw['manual_review'].update(nonterminal_client_error=error, diagnostics={'original_metadata': 'unchanged'})
        original = self.replace(path, raw);result, audit = self.run_assembly()
        self.assertEqual(result['attempts'][0]['outcome'], 'answered')
        self.assertEqual(result['attempts'][0]['raw_review']['artifact'], original)
        self.assertIn(self.machine_ref, audit['attempts'][0]['raw_evidence_refs'])
        for updates in ({'terminal_stop': True}, {'preceding_provider_ordinal': True}, {'unknown': True}):
            raw['manual_review']['nonterminal_client_error'] = dict(error, **updates);self.replace(path, raw)
            with self.subTest(updates=updates), self.assertRaises(ValueError): self.run_assembly()

    def make_selection(self):
        """Create a neutral legacy-format original and an explicitly approved v4."""
        packet = self.data(self.packet_ref['path'])
        original_path = 'reviews/attempt-raw/' + self.name + '.json'
        original = self.data(original_path)
        original['classification_basis'] = {'basis': original['classification_basis'],
                                            'details': 'Retained synthetic structured diagnostic.'}
        original['manual_review']['notes'] = [original['manual_review']['notes'], 'Retained second synthetic limitation.']
        original['evidence_refs'] = [item for item in original['evidence_refs'] if item not in (packet['plan'], packet['sample'])]
        original_ref = self.replace(original_path, original)
        bad_prior = copy.deepcopy(original)
        bad_prior['original_decision'] = 'A rejected prior correction changed this original decision.'
        prior_ref = self.replace('reviews/attempt-raw-corrections/' + self.name + '.v2.json', bad_prior)
        selected = copy.deepcopy(original)
        selected['classification_basis'] = original['classification_basis']['basis']
        selected['classification_details'] = copy.deepcopy(original['classification_basis'])
        selected['manual_review']['notes'] = ' '.join(original['manual_review']['notes'])
        selected['evidence_refs'].extend([packet['plan'], packet['sample'], original_ref])
        selected['formatting_correction'] = {'schema': 'raw-review-formatting-correction-v1', 'original': original_ref,
                    'observed_utc': '2026-10-06T00:01:00Z', 'reason': 'Mechanical representation and exact binding amendment.',
                    'preserved_prior_corrections': [prior_ref]}
        selected_ref = self.replace('reviews/attempt-raw-corrections/' + self.name + '.v4.json', selected)
        reason = 'Explicitly select approved mechanical correction; preserve original classification.'
        approval = {'schema': 'broader-pilot-raw-review-mechanical-amendment-review-v1', 'packet': self.packet_ref,
                    'plan': packet['plan'], 'sample': packet['sample'], 'assignment': self.name,
                    'original': original_ref, 'selected': selected_ref, 'reason': reason,
                    'reviewer_id': 'synthetic-independent-amendment-reviewer', 'independent': True, 'complete': True,
                    'approved': True, 'reasoning': 'Compared every original and selected JSON value; no decisions changed.',
                    'original_decision': 'Mechanical amendment approved only.', 'observed_utc': '2026-10-06T00:02:00Z'}
        approval_ref = self.replace('reviews/attempt-raw-corrections/' + self.name + '.mechanical-independent.json', approval)
        selection = {'schema': 'broader-pilot-raw-review-selection-v1', 'packet': self.packet_ref,
                     'plan': packet['plan'], 'sample': packet['sample'], 'selections': [
                         {'assignment': self.name, 'original': original_ref, 'selected': selected_ref,
                          'reason': reason, 'independent_review': approval_ref}]}
        return self.replace('reviews/raw-review-selection.closed.json', selection)

    def amend_selected_fixture(self, selection_ref, mutate):
        """Refresh fixture refs so value guards, not stale hashes, reject bad edits."""
        manifest = self.data(selection_ref['path'])
        entry = manifest['selections'][0]
        selected = self.data(entry['selected']['path'])
        mutate(selected)
        entry['selected'] = self.replace(entry['selected']['path'], selected)
        approval = self.data(entry['independent_review']['path'])
        approval['selected'] = entry['selected']
        entry['independent_review'] = self.replace(entry['independent_review']['path'], approval)
        return self.replace(selection_ref['path'], manifest)

    def no_capture(self, exit_code=None):
        self.remove(self.name + '/assignment.json')
        self.remove(self.name + '/events/000001.json')
        self.remove(self.slot['capture'] + '/inventory.json')
        raw_path = 'reviews/attempt-raw/' + self.name + '.json'
        raw = self.data(raw_path)
        raw.update(final=None, outcome='infrastructure_stop', failure_class='infrastructure')
        raw['raw_review_fields'].update(final_sha256=None, capture_integrity=False)
        raw['evidence_refs'] = [ref for ref in raw['evidence_refs'] if not ref['path'].startswith(self.name + '/') and
                                not ref['path'].startswith('captures/' + self.name) and not ref['path'].startswith('freezes/')]
        if exit_code is None:
            terminal = {'assignment': self.name, 'exit_code': None, 'launch_failed_errno': 5}
        else:
            terminal = {'assignment': self.name, 'exit_code': exit_code, 'finished_unix': 2.0}
        result_ref = self.replace('execution/' + self.name + '.result.json', terminal)
        raw['evidence_refs'] = [result_ref if ref['path'] == result_ref['path'] else ref for ref in raw['evidence_refs']]
        self.replace(raw_path, raw)
        source = self.data('reviews/attempt-source/' + self.name + '.json')
        source.update(final=None, applicability='no_final', required_claims=[], material_claims_and_citations=[])
        source['source_review_fields'].update(final_sha256=None, criteria=dict.fromkeys(CRITERIA, False))
        source['evidence_refs'] = [ref for ref in source['evidence_refs'] if not ref['path'].startswith(self.name + '/')]
        self.replace('reviews/attempt-source/' + self.name + '.json', source)

    def test_all360_exact_projection_retains_diagnostics_and_closed_hashes(self):
        result, manifest = self.run_assembly()
        self.assertEqual(len(result['attempts']), 360)
        self.assertEqual(len(manifest['attempts']), 360)
        self.assertEqual(result['attempts'][0]['raw_review']['artifact'], assembly.ref_for(self.root, 'reviews/attempt-raw/' + self.name + '.json'))
        self.assertEqual(manifest['shared_harness_declaration'], self.harness_ref)
        self.assertIsNone(result['shared_harness_defect']['review'])
        self.assertEqual(manifest['attempts'][0]['raw_original_decision'], self.data('reviews/attempt-raw/' + self.name + '.json')['original_decision'])
        self.assertIn(self.machine_ref, manifest['attempts'][0]['raw_evidence_refs'])
        self.assertEqual(set(result), {'schema', 'plan_sha256', 'sample_sha256', 'equal_access', 'shared_harness_defect', 'attempts'})

    def test_packet_hash_mismatch_fails(self):
        self.packet_ref['sha256'] = '0' * 64
        with self.assertRaisesRegex(ValueError, 'hash mismatch'):
            self.run_assembly()

    def test_missing_slot_fails_without_fabricated_failure(self):
        packet = self.data(self.packet_ref['path']); packet['attempts'].pop()
        self.packet_ref = self.replace(self.packet_ref['path'], packet)
        with self.assertRaisesRegex(ValueError, 'all360'):
            self.run_assembly()

    def test_missing_attempt_result_is_pending(self):
        self.remove('execution/' + self.name + '.result.json')
        with self.assertRaisesRegex(ValueError, 'missing artifact'):
            self.run_assembly()

    def test_missing_source_review_fails(self):
        self.remove('reviews/attempt-source/' + self.name + '.json')
        with self.assertRaisesRegex(ValueError, 'missing artifact'):
            self.run_assembly()

    def test_pending_review_fails(self):
        self.change_raw(status='pending')
        with self.assertRaisesRegex(ValueError, 'pending review'):
            self.run_assembly()

    def test_source_draft_status_fails_even_with_completed_primitive_fields(self):
        self.change_source(status='draft')
        with self.assertRaisesRegex(ValueError, 'nonterminal review status'):
            self.run_assembly()

    def test_raw_in_progress_review_status_fails_even_with_completed_primitive_fields(self):
        self.change_raw(review_status='in_progress')
        with self.assertRaisesRegex(ValueError, 'nonterminal review status'):
            self.run_assembly()

    def test_every_explicit_closure_status_requires_known_terminal_value(self):
        for field in ('status', 'review_status', 'semantic_source_review'):
            for status in ('active', 'draft', 'in_progress', 'pending-adjudication', 'prepared_only',
                           'unknown', 'completed_but_still_draft', '', False, None, {'status': 'draft'}):
                with self.subTest(field=field, status=status):
                    review = {'original_decision': 'A retained draft decision.', field: status}
                    with self.assertRaisesRegex(ValueError, 'nonterminal review status'):
                        assembly.closed(review, 'synthetic closure probe')

    def test_nested_manual_or_semantic_closure_status_cannot_hide_active_review(self):
        for field in ('manual_review', 'semantic_source_review'):
            for status in ('draft', 'in_progress'):
                with self.subTest(field=field, status=status):
                    review = {'original_decision': 'A retained original decision.', field: {'status': status}}
                    with self.assertRaisesRegex(ValueError, 'nonterminal review status'):
                        assembly.closed(review, 'synthetic nested closure probe')

    def test_terminal_status_with_incomplete_evidence_is_preserved(self):
        raw = self.data('reviews/attempt-raw/' + self.name + '.json')
        raw['review_status'] = 'closed'
        raw['raw_review_fields'].update(complete=False, capture_integrity=False)
        self.replace('reviews/attempt-raw/' + self.name + '.json', raw)
        source = self.data('reviews/attempt-source/' + self.name + '.json')
        source['status'] = 'completed'
        source['source_review_fields']['complete'] = False
        self.replace('reviews/attempt-source/' + self.name + '.json', source)
        result, manifest = self.run_assembly()
        self.assertFalse(result['attempts'][0]['raw_review']['complete'])
        self.assertFalse(result['attempts'][0]['source_review']['complete'])
        self.assertIn('missing_or_incomplete_raw_review', manifest['review_readiness_reasons'])
        self.assertIn('missing_or_incomplete_source_review', manifest['review_readiness_reasons'])

    def test_no_capture_oserror_is_retained_as_zero_not_success(self):
        self.no_capture()
        result, manifest = self.run_assembly()
        row = result['attempts'][0]
        self.assertEqual(len(result['attempts']), 360)
        self.assertEqual(row['outcome'], 'infrastructure_stop')
        self.assertFalse(row['raw_review']['capture_integrity'])
        self.assertIsNone(row['final'])
        self.assertIn('missing_or_incomplete_raw_review', manifest['review_readiness_reasons'])

    def test_no_capture_process_failure_is_retained(self):
        self.no_capture(exit_code=17)
        result, _ = self.run_assembly()
        self.assertEqual(result['attempts'][0]['failure_class'], 'infrastructure')

    def test_no_capture_unknown_classification_fails(self):
        self.no_capture()
        raw = self.data('reviews/attempt-raw/' + self.name + '.json')
        raw['raw_review_fields']['classification_confirmed'] = False
        self.replace('reviews/attempt-raw/' + self.name + '.json', raw)
        with self.assertRaisesRegex(ValueError, 'absent assignment'):
            self.run_assembly()

    def test_complete_review_with_incomplete_evidence_retained(self):
        raw = self.data('reviews/attempt-raw/' + self.name + '.json')
        raw['raw_review_fields'].update(complete=False, capture_integrity=False, classification_confirmed=False)
        raw['manual_review']['all_terminal_failed_partial_late_bodies_read'] = False
        self.replace('reviews/attempt-raw/' + self.name + '.json', raw)
        result, manifest = self.run_assembly()
        self.assertFalse(result['attempts'][0]['raw_review']['complete'])
        self.assertIn('missing_or_incomplete_raw_review', manifest['review_readiness_reasons'])

    def test_complete_raw_review_cannot_contradict_any_mandatory_manual_check(self):
        original = self.data('reviews/attempt-raw/' + self.name + '.json')
        flags = set(original['manual_review']) - {'notes', 'no_feedback_to_solver'}
        for flag in flags:
            with self.subTest(flag=flag):
                raw = copy.deepcopy(original)
                raw['manual_review'][flag] = False
                self.replace('reviews/attempt-raw/' + self.name + '.json', raw)
                with self.assertRaisesRegex(ValueError, 'complete raw review contradicts'):
                    self.run_assembly()

    def test_machine_review_selector_cannot_name_another_assignment(self):
        self.change_raw(machine_review={'artifact': self.machine_ref, 'assignment': self.slots[1]['assignment']})
        with self.assertRaisesRegex(ValueError, 'own-assignment machine-review selector'):
            self.run_assembly()

    def test_machine_review_selector_must_exist_and_have_exact_shape(self):
        for machine in ({'artifact': self.machine_ref}, {'artifact': self.machine_ref, 'assignment': None},
                        {'artifact': self.machine_ref, 'assignment': self.name, 'row': self.slots[1]['assignment']}):
            with self.subTest(machine=machine):
                self.change_raw(machine_review=machine)
                with self.assertRaisesRegex(ValueError, 'own-assignment machine-review selector'):
                    self.run_assembly()

    def test_classified_no_capture_failure_can_declare_incomplete_raw_evidence(self):
        self.no_capture()
        raw = self.data('reviews/attempt-raw/' + self.name + '.json')
        raw['raw_review_fields']['complete'] = False
        for flag in set(raw['manual_review']) - {'notes', 'no_feedback_to_solver'}:
            raw['manual_review'][flag] = False
        self.replace('reviews/attempt-raw/' + self.name + '.json', raw)
        result, manifest = self.run_assembly()
        self.assertEqual(len(result['attempts']), 360)
        self.assertEqual(result['attempts'][0]['failure_class'], 'infrastructure')
        self.assertFalse(result['attempts'][0]['raw_review']['complete'])
        self.assertFalse(result['attempts'][0]['raw_review']['capture_integrity'])
        self.assertIn('missing_or_incomplete_raw_review', manifest['review_readiness_reasons'])

    def test_any_cohort_solver_cannot_be_review_reviewer(self):
        reviewer = self.slots[-1]['solver_id']
        source = self.data('reviews/attempt-source/' + self.name + '.json')
        source['reviewer_id'] = reviewer; source['source_review_fields']['reviewer_id'] = reviewer
        self.replace('reviews/attempt-source/' + self.name + '.json', source)
        with self.assertRaisesRegex(ValueError, 'cohort solver'):
            self.run_assembly()

    def test_declaration_reviewer_cannot_be_solver(self):
        value = self.data(self.access_ref['path']); value['reviewer_id'] = self.slots[-1]['solver_id']
        self.access_ref = self.replace(self.access_ref['path'], value)
        with self.assertRaisesRegex(ValueError, 'independent review'):
            self.run_assembly()

    def test_fresh_source_history_required(self):
        self.change_source(review_history={'no_task_authorship': False, 'no_prospective_source_or_gold_review': True, 'no_feedback_to_solver': True})
        with self.assertRaisesRegex(ValueError, 'fresh semantic reviewer history'):
            self.run_assembly()

    def test_exact_task_join_required(self):
        self.change_source(task_sha256='0' * 64)
        with self.assertRaisesRegex(ValueError, 'task_sha256 join'):
            self.run_assembly()

    def test_final_cannot_be_another_slots_answer(self):
        other = self.data('reviews/attempt-source/' + self.slots[1]['assignment'] + '.json')['final']
        self.change_source(final=other)
        with self.assertRaisesRegex(ValueError, 'task/final review join'):
            self.run_assembly()

    def test_no_final_cannot_drop_existing_recorded_final(self):
        self.change_raw(final=None, outcome='no_final', failure_class='product')
        with self.assertRaisesRegex(ValueError, 'task/final review join'):
            self.run_assembly()

    def test_post_stop_existing_final_is_retained(self):
        self.change_raw(outcome='budget_stop', failure_class='budget')
        result, _ = self.run_assembly()
        self.assertIsNotNone(result['attempts'][0]['final'])
        self.assertEqual(result['attempts'][0]['failure_class'], 'budget')

    def test_review_fields_cannot_include_artifact_self_hash(self):
        raw = self.data('reviews/attempt-raw/' + self.name + '.json')
        raw['raw_review_fields']['artifact'] = assembly.ref_for(self.root, 'reviews/attempt-raw/' + self.name + '.json')
        self.replace('reviews/attempt-raw/' + self.name + '.json', raw)
        with self.assertRaisesRegex(ValueError, 'self hash'):
            self.run_assembly()

    def test_nested_review_self_reference_rejected(self):
        self.change_raw(disagreement=assembly.ref_for(self.root, 'reviews/attempt-raw/' + self.name + '.json'))
        with self.assertRaisesRegex(ValueError, 'itself'):
            self.run_assembly()

    def test_disagreements_and_original_labels_remain_immutable(self):
        dispute = self.replace('reviews/synthetic-policy.json', {'original': 'incorrect', 'superseding': 'uncertain', 'reasoning': 'preserve disagreement'})
        source = self.data('reviews/attempt-source/' + self.name + '.json')
        source['source_review_fields']['criteria']['all_required_correct'] = False
        source.update(disagreements=[dispute], original_decision={'label': 'incorrect', 'reasoning': 'Original reason.'})
        self.replace('reviews/attempt-source/' + self.name + '.json', source)
        original = (self.root / ('reviews/attempt-source/' + self.name + '.json')).read_bytes()
        result, manifest = self.run_assembly()
        self.assertEqual(original, (self.root / ('reviews/attempt-source/' + self.name + '.json')).read_bytes())
        self.assertIn(dispute, manifest['attempts'][0]['source_evidence_refs'])
        self.assertEqual(manifest['attempts'][0]['source_original_decision']['label'], 'incorrect')
        self.assertFalse(result['attempts'][0]['source_review']['criteria']['all_required_correct'])

    def test_shared_cause_without_confirmed_postrun_receipt_fails(self):
        self.change_raw(outcome='infrastructure_stop', failure_class='shared_harness')
        with self.assertRaisesRegex(ValueError, 'exact independent adjudication'):
            self.run_assembly()

    def test_independently_confirmed_exact_shared_cause_is_retained(self):
        raw_ref = self.change_raw(outcome='infrastructure_stop', failure_class='shared_harness')
        value = self.data(self.harness_ref['path'])
        value.update(confirmed=True, reviewed_causes=[{'assignment': self.name, 'cause': 'shared_harness',
                     'raw_review': raw_ref, 'reasoning': 'Synthetic independently confirmed defect.'}])
        self.harness_ref = self.replace(self.harness_ref['path'], value)
        result, manifest = self.run_assembly()
        self.assertTrue(result['shared_harness_defect']['confirmed'])
        self.assertIn('confirmed_shared_harness_defect', manifest['review_readiness_reasons'])

    def test_access_cause_contradicts_equal_access_true(self):
        self.change_raw(outcome='scope_stop', failure_class='access', access_loss_confirmed=True)
        with self.assertRaisesRegex(ValueError, 'access'):
            self.run_assembly()

    def test_access_loss_requires_independent_exact_receipt_reference(self):
        raw_ref = self.change_raw(outcome='scope_stop', failure_class='access', access_loss_confirmed=True)
        value = self.data(self.access_ref['path']); value['established'] = False
        self.access_ref = self.replace(self.access_ref['path'], value)
        with self.assertRaisesRegex(ValueError, 'access-loss raw review'):
            self.run_assembly()
        value['evidence_refs'].append(raw_ref)
        self.access_ref = self.replace(self.access_ref['path'], value)
        result, manifest = self.run_assembly()
        self.assertEqual(result['attempts'][0]['failure_class'], 'access')
        self.assertIn('equal_access_not_established', manifest['review_readiness_reasons'])

    def test_cli_requires_explicit_declarations(self):
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
            assembly.main(['--root', str(self.root)])
        self.assertEqual(error.exception.code, 2)

    def test_confined_exact_references_reject_traversal_and_symlink(self):
        with self.assertRaisesRegex(ValueError, 'normalized'):
            assembly.read_ref(self.root, {'path': '../escape.json', 'sha256': '0' * 64})
        link = self.root / 'synthetic-link.json'
        link.symlink_to('reviews/synthetic-policy.json')
        try:
            with self.assertRaisesRegex(ValueError, 'symlink'):
                assembly.ref_for(self.root, 'synthetic-link.json')
        finally:
            link.unlink()

    def test_duplicate_json_keys_rejected(self):
        with self.assertRaisesRegex(ValueError, 'duplicate JSON key'):
            assembly.load(b'{"outcome":"answered","outcome":"no_final"}')

    def test_missing_capture_inventory_retains_answer_and_review_defect(self):
        self.remove(self.slot['capture'] + '/inventory.json')
        raw = self.data('reviews/attempt-raw/' + self.name + '.json')
        raw['raw_review_fields'].update(capture_integrity=False, complete=False)
        raw['evidence_refs'] = [ref for ref in raw['evidence_refs'] if ref['path'] != self.slot['capture'] + '/inventory.json']
        self.replace('reviews/attempt-raw/' + self.name + '.json', raw)
        result, manifest = self.run_assembly()
        self.assertEqual(result['attempts'][0]['outcome'], 'answered')
        self.assertIsNotNone(result['attempts'][0]['final'])
        self.assertFalse(result['attempts'][0]['raw_review']['capture_integrity'])
        self.assertIn('missing_or_incomplete_raw_review', manifest['review_readiness_reasons'])

    def test_retained_orphan_answer_cannot_be_silently_dropped(self):
        self.no_capture()
        final = assembly.ref_for(self.root, self.name + '/blobs/000001.answer')
        self.replace(self.name + '/events/000001.json', {'event': 'answer',
                     'answer': {'path': 'blobs/000001.answer', 'sha256': final['sha256']}})
        with self.assertRaisesRegex(ValueError, 'task/final review join'):
            self.run_assembly()

    def test_raw_review_must_exist_even_if_source_review_exists(self):
        self.remove('reviews/attempt-raw/' + self.name + '.json')
        with self.assertRaisesRegex(ValueError, 'missing artifact'):
            self.run_assembly()

    def test_same_raw_and_source_reviewer_fails(self):
        source = self.data('reviews/attempt-source/' + self.name + '.json')
        source['reviewer_id'] = 'synthetic-raw-reviewer'
        source['source_review_fields']['reviewer_id'] = 'synthetic-raw-reviewer'
        self.replace('reviews/attempt-source/' + self.name + '.json', source)
        with self.assertRaisesRegex(ValueError, 'raw and semantic reviewers'):
            self.run_assembly()

    def test_false_harness_requires_explicit_independent_receipt(self):
        self.harness_ref['sha256'] = '0' * 64
        with self.assertRaisesRegex(ValueError, 'hash mismatch'):
            self.run_assembly()

    def test_schema_code_hash_must_match_exact_module(self):
        with self.assertRaisesRegex(ValueError, 'hash mismatch'):
            assembly.assemble(self.root, self.packet_ref, self.access_ref, self.harness_ref, '0' * 64)

    def test_explicit_approved_mechanical_selection_retains_both_reviews_and_all360(self):
        selection_ref = self.make_selection()
        entry = self.data(selection_ref['path'])['selections'][0]
        original_bytes = (self.root / entry['original']['path']).read_bytes()
        selected_bytes = (self.root / entry['selected']['path']).read_bytes()
        result, manifest = self.run_assembly(selection_ref)
        self.assertEqual(len(result['attempts']), 360)
        self.assertEqual(result['attempts'][0]['raw_review']['artifact'], entry['selected'])
        self.assertEqual(result['attempts'][1]['raw_review']['artifact'], assembly.ref_for(self.root, 'reviews/attempt-raw/' + self.slots[1]['assignment'] + '.json'))
        self.assertEqual(manifest['raw_review_selection'], selection_ref)
        retained = manifest['attempts'][0]['raw_review_selection']
        self.assertEqual(retained['original'], entry['original'])
        self.assertEqual(retained['selected'], entry['selected'])
        self.assertEqual(retained['independent_review'], entry['independent_review'])
        self.assertEqual(original_bytes, (self.root / entry['original']['path']).read_bytes())
        self.assertEqual(selected_bytes, (self.root / entry['selected']['path']).read_bytes())
        self.assertEqual(result['attempts'][0]['outcome'], self.data(entry['original']['path'])['outcome'])
        self.assertEqual(manifest['attempts'][0]['raw_original_decision'], self.data(entry['original']['path'])['original_decision'])
        self.assertIn(self.data(entry['selected']['path'])['formatting_correction']['preserved_prior_corrections'][0],
                      manifest['attempts'][0]['raw_evidence_refs'])

    def test_existing_correction_is_never_autoselected(self):
        self.make_selection()
        with self.assertRaisesRegex(ValueError, 'missing exact plan'):
            self.run_assembly()

    def test_explicit_selected_ref_does_not_follow_newer_version(self):
        selection_ref = self.make_selection()
        entry = self.data(selection_ref['path'])['selections'][0]
        self.replace('reviews/attempt-raw-corrections/' + self.name + '.v99.json', self.data(entry['selected']['path']))
        result, _ = self.run_assembly(selection_ref)
        self.assertEqual(result['attempts'][0]['raw_review']['artifact'], entry['selected'])

    def test_selection_cannot_change_any_core_or_unrelated_review_field(self):
        selection_ref = self.make_selection()
        entry = self.data(selection_ref['path'])['selections'][0]
        baseline = self.data(entry['selected']['path'])
        changes = {'task_id': 'another-task', 'arm': 'B', 'repetition': 2, 'solver_id': 'another-solver',
                   'task_sha256': '0' * 64, 'outcome': 'no_final', 'failure_class': 'product', 'final': None,
                   'reviewer_id': 'another-reviewer', 'original_decision': 'Changed original decision.',
                   'observed_utc': '2026-10-06T00:03:00Z', 'semantic_scoring': 'changed', 'new_unrelated_field': True}
        for field, value in changes.items():
            with self.subTest(field=field):
                def mutate(selected):
                    selected.clear(); selected.update(copy.deepcopy(baseline)); selected[field] = value
                selection_ref = self.amend_selected_fixture(selection_ref, mutate)
                with self.assertRaisesRegex(ValueError, 'changes a decision'):
                    self.run_assembly(selection_ref)

    def test_selection_cannot_change_raw_primitive_or_manual_boolean(self):
        selection_ref = self.make_selection()
        baseline = self.data(self.data(selection_ref['path'])['selections'][0]['selected']['path'])
        for section, field in (('raw_review_fields', 'complete'), ('raw_review_fields', 'capture_integrity'),
                               ('raw_review_fields', 'classification_confirmed'),
                               ('manual_review', 'all_retained_provider_native_bodies_read')):
            with self.subTest(section=section, field=field):
                def mutate(selected):
                    selected.clear(); selected.update(copy.deepcopy(baseline)); selected[section][field] = False
                selection_ref = self.amend_selected_fixture(selection_ref, mutate)
                with self.assertRaisesRegex(ValueError, 'changes a decision'):
                    self.run_assembly(selection_ref)

    def test_selection_json_type_changes_are_not_hidden_by_python_equality(self):
        raw = self.data('reviews/attempt-raw/' + self.name + '.json')
        raw['original_decision']['boolean_marker'] = True
        self.replace('reviews/attempt-raw/' + self.name + '.json', raw)
        selection_ref = self.make_selection()
        selection_ref = self.amend_selected_fixture(selection_ref,
                         lambda value: value['original_decision'].update(boolean_marker=1))
        with self.assertRaisesRegex(ValueError, 'changes a decision'):
            self.run_assembly(selection_ref)

    def test_selection_requires_exact_preserved_details_and_normalization(self):
        selection_ref = self.make_selection()
        baseline = self.data(self.data(selection_ref['path'])['selections'][0]['selected']['path'])
        for field, value in (('classification_basis', 'A different human-written summary.'),
                             ('classification_details', {'basis': baseline['classification_basis']}),
                             ('manual_review', dict(baseline['manual_review'], notes='Different or reordered limitations.'))):
            with self.subTest(field=field):
                def mutate(selected):
                    selected.clear(); selected.update(copy.deepcopy(baseline)); selected[field] = value
                selection_ref = self.amend_selected_fixture(selection_ref, mutate)
                with self.assertRaisesRegex(ValueError, 'changes a decision'):
                    self.run_assembly(selection_ref)

    def test_selection_evidence_must_preserve_original_prefix(self):
        selection_ref = self.make_selection()
        selection_ref = self.amend_selected_fixture(selection_ref, lambda value: value['evidence_refs'].pop(0))
        with self.assertRaisesRegex(ValueError, 'prefix must be retained'):
            self.run_assembly(selection_ref)

    def test_selection_evidence_can_append_only_missing_exact_bindings(self):
        selection_ref = self.make_selection()
        selection_ref = self.amend_selected_fixture(selection_ref, lambda value: value['evidence_refs'].append(self.machine_ref))
        with self.assertRaisesRegex(ValueError, 'append only missing exact'):
            self.run_assembly(selection_ref)

    def test_selection_refusal_or_author_approval_is_not_independent(self):
        selection_ref = self.make_selection()
        manifest = self.data(selection_ref['path']); entry = manifest['selections'][0]
        baseline = self.data(entry['independent_review']['path'])
        for field, value in (('approved', False), ('complete', False), ('independent', False),
                             ('reviewer_id', 'synthetic-raw-reviewer'), ('reviewer_id', self.slots[-1]['solver_id'])):
            with self.subTest(field=field, value=value):
                approval = copy.deepcopy(baseline); approval[field] = value
                entry['independent_review'] = self.replace(entry['independent_review']['path'], approval)
                selection_ref = self.replace(selection_ref['path'], manifest)
                with self.assertRaisesRegex(ValueError, 'independent approved'):
                    self.run_assembly(selection_ref)

    def test_selection_and_approval_require_exact_frozen_bindings_and_reason(self):
        selection_ref = self.make_selection()
        manifest = self.data(selection_ref['path']); entry = manifest['selections'][0]
        approval = self.data(entry['independent_review']['path'])
        approval['reason'] = 'Different selection scope.'
        entry['independent_review'] = self.replace(entry['independent_review']['path'], approval)
        selection_ref = self.replace(selection_ref['path'], manifest)
        with self.assertRaisesRegex(ValueError, 'approval binding differs'):
            self.run_assembly(selection_ref)
        manifest['plan'] = manifest['sample']
        selection_ref = self.replace(selection_ref['path'], manifest)
        with self.assertRaisesRegex(ValueError, 'selection packet/plan/sample binding'):
            self.run_assembly(selection_ref)

    def test_selection_requires_canonical_immutable_original(self):
        selection_ref = self.make_selection()
        manifest = self.data(selection_ref['path']); entry = manifest['selections'][0]
        entry['original'] = entry['selected']
        selection_ref = self.replace(selection_ref['path'], manifest)
        with self.assertRaisesRegex(ValueError, 'immutable canonical'):
            self.run_assembly(selection_ref)

    def test_duplicate_or_unknown_selection_slot_fails(self):
        selection_ref = self.make_selection()
        manifest = self.data(selection_ref['path']); manifest['selections'].append(copy.deepcopy(manifest['selections'][0]))
        selection_ref = self.replace(selection_ref['path'], manifest)
        with self.assertRaisesRegex(ValueError, 'duplicate raw-review selection'):
            self.run_assembly(selection_ref)

    def test_selection_hash_and_immutable_original_hash_must_match(self):
        selection_ref = self.make_selection()
        wrong = dict(selection_ref, sha256='0' * 64)
        with self.assertRaisesRegex(ValueError, 'hash mismatch'):
            self.run_assembly(wrong)
        entry = self.data(selection_ref['path'])['selections'][0]
        original = self.data(entry['original']['path']); original['observed_utc'] = '2026-10-06T00:04:00Z'
        self.replace(entry['original']['path'], original)
        with self.assertRaisesRegex(ValueError, 'hash mismatch'):
            self.run_assembly(selection_ref)

    def test_selection_provenance_is_bounded_and_prior_refs_cannot_be_selected(self):
        selection_ref = self.make_selection()
        selection_ref = self.amend_selected_fixture(selection_ref,
                         lambda value: value['formatting_correction'].update(outcome='answered'))
        with self.assertRaisesRegex(ValueError, 'bounded mechanical correction provenance'):
            self.run_assembly(selection_ref)

    def test_selection_join_preserves_empty_notes_members_without_trimming(self):
        raw = self.data('reviews/attempt-raw/' + self.name + '.json')
        raw['manual_review']['notes'] = ''
        self.replace('reviews/attempt-raw/' + self.name + '.json', raw)
        selection_ref = self.make_selection()
        result, _ = self.run_assembly(selection_ref)
        self.assertEqual(len(result['attempts']), 360)
        selected = self.data(result['attempts'][0]['raw_review']['artifact']['path'])
        self.assertEqual(selected['manual_review']['notes'], ' Retained second synthetic limitation.')

    def test_cli_optional_selection_requires_exact_path_hash_pair(self):
        args = ['--root', str(self.root), '--packet', self.packet_ref['path'], '--packet-sha256', self.packet_ref['sha256'],
                '--equal-access', self.access_ref['path'], '--equal-access-sha256', self.access_ref['sha256'],
                '--shared-harness', self.harness_ref['path'], '--shared-harness-sha256', self.harness_ref['sha256'],
                '--schema-sha256', self.schema_sha, '--output', 'unused-synthetic-results.json', '--manifest-output', 'unused-synthetic-manifest.json']
        with contextlib.redirect_stderr(io.StringIO()) as error_output, self.assertRaises(SystemExit):
            assembly.main(args + ['--raw-review-selection', 'reviews/raw-review-selection.closed.json'])
        self.assertIn('must be supplied together', error_output.getvalue())
        self.assertFalse((self.root / 'unused-synthetic-results.json').exists())

    def test_cli_writes_exact_hashes_and_never_overwrites_existing_diagnostics(self):
        args = ['--root', str(self.root), '--packet', self.packet_ref['path'], '--packet-sha256', self.packet_ref['sha256'],
                '--equal-access', self.access_ref['path'], '--equal-access-sha256', self.access_ref['sha256'],
                '--shared-harness', self.harness_ref['path'], '--shared-harness-sha256', self.harness_ref['sha256'],
                '--schema-sha256', self.schema_sha, '--output', 'synthetic-results.json', '--manifest-output', 'synthetic-manifest.json']
        with contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(assembly.main(args), 0)
        try:
            printed = json.loads(output.getvalue())
            self.assertEqual(printed['results'], assembly.ref_for(self.root, 'synthetic-results.json'))
            manifest = self.data('synthetic-manifest.json')
            self.assertEqual(manifest['results'], printed['results'])
            original = (self.root / 'synthetic-results.json').read_bytes()
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                assembly.main(args)
            self.assertEqual(error.exception.code, 2)
            self.assertEqual((self.root / 'synthetic-results.json').read_bytes(), original)
        finally:
            (self.root / 'synthetic-results.json').unlink()
            (self.root / 'synthetic-manifest.json').unlink()


if __name__ == '__main__':
    unittest.main()
