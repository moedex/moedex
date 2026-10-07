"""Synthetic offline workflow checks; no provider or product traffic."""
from copy import deepcopy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import isolated_solver as solver
import solver_workflow as w
import test_native_scope as sf
import test_run_record as rf
import native_scope as ns
import run_record as rr


def source(count=3, width=20):
    row = {'repo': 'example/api', 'path': 'worker.go', 'blob_sha': 'b'*40,
           'lines': count, 'start_line': 1, 'end_line': count, 'truncated': False,
           'content': ''.join('line %d: '%n + 'x'*width + '\n' for n in range(1, count+1))}
    return {'jsonrpc': '2.0', 'id': 1, 'result': {'structuredContent': row,
            'content': [{'type': 'text', 'text': w.canonical(row).decode()}]}}


def plan():
    request = 'Explain worker and failure.'
    return w.prepare(request, [{'id': 'worker', 'start': 8, 'end': 14, 'quote': 'worker', 'kind': 'requirement'},
                               {'id': 'failure', 'start': 19, 'end': 26, 'quote': 'failure', 'kind': 'terminal_path'}])


def answer(raw):
    cite = {'display_sha256': w.sha(raw), 'repo': 'example/api', 'path': 'worker.go', 'blob_sha': 'b'*40,
            'start_line': 1, 'end_line': 2, 'source_role': 'implementation'}
    return {'claims': [{'id': 'c1', 'text': 'Behavior explained', 'source_role': 'implementation', 'citations': [cite]}],
            'requirements': [{'id': i['id'], 'status': 'addressed', 'claim_ids': ['c1'],
                              'terminal_paths': [{'outcome': 'failure', 'status': 'addressed', 'claim_ids': ['c1']}]}
                             for i in plan()['inventory']]}


class Workflow(unittest.TestCase):
    def test_inventory_anchors_and_coverage_dispositions(self):
        p = plan(); bad = deepcopy(p['inventory']); bad[0]['quote'] = 'invented obligation'
        with self.assertRaises(ValueError): w.prepare(p['request'], bad)
        raw = w.canonical(source()); a = answer(raw)
        report = w.check(p, a, [raw])
        self.assertTrue(report['declared_complete']); self.assertEqual(report['semantic_correctness'], 'not assessed')
        self.assertIn('review', report['inventory_completeness'])
        a['requirements'].pop(); self.assertFalse(w.check(p, a, [raw])['mechanically_valid'])
        a = answer(raw); a['requirements'][0].update(status='unresolved', reason='Need evidence', claim_ids=[], terminal_paths=[])
        report = w.check(p, a, [raw]); self.assertTrue(report['mechanically_valid']); self.assertFalse(report['declared_complete'])
        del a['requirements'][0]['reason']; self.assertFalse(w.check(p, a, [raw])['mechanically_valid'])

    def test_opt_in_partial_disposition_retains_gaps_without_source_credit(self):
        raw = w.canonical(source()); a = answer(raw)
        a['claims'].append({'id': 'gap', 'text': 'Execution unverified', 'source_role': 'other',
                            'citations': [], 'unresolved_reason': 'No execution record'})
        a['requirements'][0].update(status='partial', reason='Only static source available', claim_ids=['c1', 'gap'])
        p = w.prepare(plan()['request'], plan()['inventory'], ledger_mode=w.LEDGER_MODE)
        report = w.check(p, a, [raw])
        self.assertTrue(report['mechanically_valid']); self.assertFalse(report['declared_complete'])
        self.assertIn('gap', report['unresolved']); self.assertIn('worker', report['unresolved'])
        self.assertEqual([r['claim_id'] for r in report['resolved_citations']], ['c1'])
        self.assertFalse(w.check(plan(), a, [raw])['mechanically_valid'])
        a['requirements'][0]['status'] = 'addressed'
        self.assertFalse(w.check(p, a, [raw])['mechanically_valid'])
        a['requirements'][0]['status'] = 'partial'; del a['requirements'][0]['reason']
        self.assertFalse(w.check(p, a, [raw])['mechanically_valid'])

    def test_partial_does_not_hide_bad_references_or_citations(self):
        raw = w.canonical(source()); p = w.prepare(plan()['request'], plan()['inventory'], ledger_mode=w.LEDGER_MODE)
        for refs in [[], ['unknown']]:
            a = answer(raw); a['requirements'][0].update(status='partial', reason='Gap', claim_ids=refs)
            self.assertFalse(w.check(p, a, [raw])['mechanically_valid'])
        a = answer(raw); a['requirements'][0].update(status='partial', reason='Gap')
        a['claims'][0]['citations'][0]['end_line'] = 999
        self.assertFalse(w.check(p, a, [raw])['mechanically_valid'])
        a = answer(raw); a['requirements'][0].update(status='partial', reason='Gap')
        a['claims'][0].update(citations=[], unresolved_reason='No evidence')
        self.assertFalse(w.check(p, a, [raw])['mechanically_valid'])

    def test_reviewed_terminal_paths_are_explicit_and_only_opt_in(self):
        raw = w.canonical(source()); a = answer(raw)
        p = w.prepare(plan()['request'], plan()['inventory'], ledger_mode=w.LEDGER_MODE)
        a['requirements'][0]['terminal_path_review'] = {'status': 'reviewed', 'reason': 'Outcomes inventoried'}
        self.assertTrue(w.check(p, a, [raw])['mechanically_valid'])
        self.assertFalse(w.check(plan(), a, [raw])['mechanically_valid'])
        for review, paths in [({'status': 'reviewed', 'reason': ''}, a['requirements'][0]['terminal_paths']),
                              ({'status': 'reviewed', 'reason': 'Reviewed'}, []),
                              ({'status': 'not_applicable', 'reason': 'No flow'}, a['requirements'][0]['terminal_paths'])]:
            a = answer(raw); a['requirements'][0].update(terminal_path_review=review, terminal_paths=paths)
            self.assertFalse(w.check(p, a, [raw])['mechanically_valid'])

    def test_new_plan_mode_and_guidance_are_hash_bound(self):
        p = w.prepare(plan()['request'], plan()['inventory'], w.REFERENCE_MODE, w.LEDGER_MODE)
        self.assertIn('presentation_id', p['instructions']); self.assertIn('status=partial', p['instructions'])
        raw = w.canonical(source()); a = answer(raw)
        self.assertTrue(w.check(p, a, [raw])['mechanically_valid'])
        p['instructions'] += ' modified'
        with self.assertRaises(ValueError): w.check(p, a, [raw])
        with self.assertRaises(ValueError): w.prepare(plan()['request'], plan()['inventory'], ledger_mode='future')

    def test_terminal_paths_and_unresolved_claims(self):
        raw = w.canonical(source()); a = answer(raw)
        a['requirements'][0]['terminal_paths'] = []
        self.assertFalse(w.check(plan(), a, [raw])['mechanically_valid'])
        a['requirements'][0]['terminal_paths'] = [{'outcome': 'cancellation', 'status': 'unresolved', 'claim_ids': [], 'reason': 'Not retrieved'}]
        report = w.check(plan(), a, [raw]); self.assertTrue(report['mechanically_valid']); self.assertFalse(report['declared_complete'])
        a['claims'][0].update(citations=[], unresolved_reason='Body unavailable')
        self.assertFalse(w.check(plan(), a, [raw])['mechanically_valid'])

    def test_ordinary_requirements_can_declare_no_applicable_terminal_path(self):
        raw = w.canonical(source()); a = answer(raw)
        a['requirements'][0].update(terminal_paths=[], terminal_path_review={'status': 'not_applicable', 'reason': 'Literal-only fact'})
        self.assertTrue(w.check(plan(), a, [raw])['mechanically_valid'])
        a['requirements'][1].update(terminal_paths=[], terminal_path_review={'status': 'not_applicable', 'reason': 'No flow'})
        self.assertFalse(w.check(plan(), a, [raw])['mechanically_valid'])

    def test_claim_specific_identity_range_and_role_consistency(self):
        raw = w.canonical(source())
        for field, value in [('display_sha256', '0'*64), ('repo', 'other'), ('path', 'interface.go'),
                             ('blob_sha', 'c'*40), ('start_line', 0), ('end_line', 4),
                             ('end_line', True), ('end_line', 10**50), ('source_role', 'interface')]:
            with self.subTest(field=field):
                a = answer(raw); a['claims'][0]['citations'][0][field] = value
                self.assertFalse(w.check(plan(), a, [raw])['mechanically_valid'])
        # Roles are labels for consistency, never a semantic declaration classifier.
        a = answer(raw); a['claims'][0]['source_role'] = a['claims'][0]['citations'][0]['source_role'] = 'interface'
        self.assertEqual(w.check(plan(), a, [raw])['semantic_correctness'], 'not assessed')

    def test_lf_lines_clipped_tails_and_missing_raw_capture_are_not_evidence(self):
        response = source(); row = response['result']['structuredContent']
        row.update(content='one\r\v\f\x85\u2028\u2029\npartial', end_line=50, clipped=True)
        raw = w.canonical(response); a = answer(raw); cite = a['claims'][0]['citations'][0]
        cite['end_line'] = 1; self.assertTrue(w.check(plan(), a, [raw])['mechanically_valid'])
        cite['end_line'] = 2; self.assertFalse(w.check(plan(), a, [raw])['mechanically_valid'])
        original = w.canonical(source(40, 300)); display = w.canonical(w.single_source(json.loads(original), 8192))
        a = answer(display); a['claims'][0]['citations'][0].update(start_line=40, end_line=40)
        self.assertFalse(w.check(plan(), a, [display])['mechanically_valid'])
        a['claims'][0]['citations'][0]['display_sha256'] = w.sha(original)
        self.assertFalse(w.check(plan(), a, [display])['mechanically_valid'])
        self.assertEqual(w.shown_sources(solver.bounded_response(json.loads(original), 8192)), [])
        self.assertEqual(w.text_lines('a\r\nb\r'), ['a\r\n', 'b\r'])

    def test_source_fact_survival_and_exact_display_cap_boundaries(self):
        response = source(24, 180); self.assertGreater(len(w.canonical(response)), 8192)
        shown = w.single_source(response, 8192); info = shown['result']['_meta']['dev.moedex/display']
        self.assertFalse(info['truncated_display']); self.assertEqual(shown['result']['structuredContent'], response['result']['structuredContent'])
        self.assertEqual(w.canonical(shown).count(b'line 24:'), 1)
        size = len(w.canonical(shown)); self.assertEqual(w.single_source(response, size), shown)
        less = w.single_source(response, size-1); self.assertLessEqual(len(w.canonical(less)), size-1)
        self.assertTrue(less['result']['_meta']['dev.moedex/display']['truncated_display'])
        self.assertIn('line 1:', less['result']['structuredContent']['content'])
        self.assertEqual(info['validated_envelope_sha256'], w.sha(w.canonical(response)))
        response['result']['isError'] = True; self.assertTrue(w.single_source(response, 8192)['result']['isError'])
        self.assertIsNone(w.single_source(response, 50))

    def test_graph_source_uses_actual_numbers_not_declaration_metadata(self):
        raw = w.canonical(sf.source()); a = answer(raw)
        a['claims'][0]['citations'][0].update(repo='Example.Api', path='src/api.cs', start_line=1, end_line=1)
        self.assertTrue(w.check(plan(), a, [raw])['mechanically_valid'])
        response = sf.source(); response['result']['structuredContent']['result']['lines'][0]['number'] = 4
        self.assertEqual(w.shown_sources(response), [])
        self.assertEqual(w.shown_sources(sf.envelope()), [])

    def test_many_short_lines_use_logarithmic_envelope_sizing(self):
        response = source(100000, 1)
        with patch.object(w, 'canonical', wraps=w.canonical) as sizing:
            shown = w.single_source(response, 8192)
        self.assertLess(sizing.call_count, 30)
        self.assertLessEqual(len(w.canonical(shown)), 8192)
        self.assertIn('line 1:', shown['result']['structuredContent']['content'])

    def test_malformed_inputs_and_duplicate_keys_fail_closed(self):
        raw = w.canonical(source())
        for field, value in [('claims', [None]), ('requirements', [None])]:
            a = answer(raw); a[field] = value; self.assertFalse(w.check(plan(), a, [raw])['mechanically_valid'])
        for change in [lambda a: a['claims'][0].update(citations=[None]),
                       lambda a: a['requirements'][0].update(claim_ids=[[]]),
                       lambda a: a['requirements'][0].update(terminal_paths=[None])]:
            a = answer(raw); change(a); self.assertFalse(w.check(plan(), a, [raw])['mechanically_valid'])
        self.assertFalse(w.check(plan(), answer(raw), [b'{"result":{},"result":{}}'])['mechanically_valid'])
        with self.assertRaises(ValueError): w.strict_json(b'{"x":1,"x":2}')
        with self.assertRaises(ValueError): w.strict_json(b'{"x":NaN}')

    def test_cli_explicit_gap_prompt_and_checker_agree(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); raw = w.canonical(source()); a = answer(raw)
            a['claims'].append({'id': 'gap', 'text': 'No observed runtime execution', 'source_role': 'other',
                                'citations': [], 'unresolved_reason': 'Only static source supplied'})
            a['requirements'][0].update(status='partial', reason='Runtime not observed', claim_ids=['c1', 'gap'])
            a['requirements'][0]['terminal_path_review'] = {'status': 'reviewed', 'reason': 'Paths listed below'}
            for name, body in [('request', plan()['request'].encode()), ('inventory', w.canonical(plan()['inventory'])),
                               ('answer', w.canonical(a)), ('display', raw)]: (root/name).write_bytes(body)
            prepared = subprocess.run([sys.executable, '-B', w.__file__, 'prepare', '--request', str(root/'request'),
                                       '--inventory', str(root/'inventory'), '--ledger-mode', w.LEDGER_MODE,
                                       '--citation-reference-mode', w.REFERENCE_MODE], capture_output=True, check=True)
            (root/'plan').write_bytes(prepared.stdout)
            self.assertIn('status=partial', json.loads(prepared.stdout)['instructions'])
            checked = subprocess.run([sys.executable, '-B', w.__file__, 'check', '--plan', str(root/'plan'),
                                      '--answer', str(root/'answer'), '--display', str(root/'display')],
                                     capture_output=True, check=True)
            report = json.loads(checked.stdout)
            self.assertTrue(report['mechanically_valid']); self.assertFalse(report['declared_complete'])
            self.assertEqual([r['claim_id'] for r in report['resolved_citations']], ['c1'])

    def test_cli_prepare_check(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); raw = w.canonical(source())
            for name, body in [('request', plan()['request'].encode()), ('inventory', w.canonical(plan()['inventory'])),
                               ('answer', w.canonical(answer(raw))), ('display', raw)]: (root/name).write_bytes(body)
            prepared = subprocess.run([sys.executable, '-B', w.__file__, 'prepare', '--request', str(root/'request'), '--inventory', str(root/'inventory')], capture_output=True, check=True)
            (root/'plan').write_bytes(prepared.stdout)
            checked = subprocess.run([sys.executable, '-B', w.__file__, 'check', '--plan', str(root/'plan'), '--answer', str(root/'answer'), '--display', str(root/'display')], capture_output=True, check=True)
            self.assertTrue(json.loads(checked.stdout)['declared_complete'])


class Broker(unittest.TestCase):
    handle = sf.BrokerScopeTests.handle
    execution = sf.BrokerScopeTests.execution
    def setUp(self):
        self.fixture = rf.Records(); self.fixture.setUp(); self.addCleanup(self.fixture.tmp.cleanup)
        self.fixture.budgets = {'calls': 8, 'response_bytes': 1<<20, 'assignment_seconds': 30, 'display_bytes': 8192}
        path = self.fixture.root/'scope.json'; path.write_bytes(rr.canonical(sf.policy()))
        self.fixture.manifest.update(source_scope_policy=rr.reference(self.fixture.root, path),
                                     source_scope_review=self.fixture.proof, presentation_mode=w.PRESENTATION)
        self.record = self.fixture.create(); self.scope = ns.NativeScope(sf.policy())
    def test_compact_broker_retains_raw_delivered_bytes_and_cursor(self):
        value = sf.source(cursor='next-page'); value['result']['structuredContent']['result']['lines'][0]['text'] = 'terminal fact ' + 'x'*4600; sf.mirror(value)
        client = sf.FakeNative(value)
        broker = solver.NativeBroker(client, self.record, sf.policy()['allowed_tools'], source_scope=self.scope, presentation_mode=w.PRESENTATION)
        response = self.handle(broker, sf.request('graph_source', filePath='src/api.cs'))
        self.assertIn('terminal fact', response['result']['structuredContent']['result']['lines'][0]['text'])
        self.assertEqual(self.scope.cursors['next-page'], ('example/api', 'src/api.cs'))
        report = self.fixture.report(); self.assertFalse(report['validation_errors'])
        events = [json.loads(path.read_bytes()) for path in sorted((self.record.directory/'events').glob('*.json'))]
        displays = [e for e in events if e.get('event') == 'display']; responses = [e for e in events if e.get('event') == 'response']
        delivered = rr.read_ref(self.fixture.root, displays[-1]['display'])
        self.assertEqual(delivered, rr.canonical(response)); self.assertEqual(rr.canonical(json.loads(delivered)), delivered)
        replay_scope = ns.NativeScope(sf.policy())
        accepted = replay_scope.accept(value, replay_scope.prepare(sf.request('graph_source', filePath='src/api.cs')))
        self.assertEqual(rr.canonical(solver.present_response(accepted, 8192, w.PRESENTATION)), delivered)
        self.assertEqual(w.recorded_displays(self.fixture.root, 'assignment-a'), [delivered])
        self.assertEqual(rr.read_ref(self.fixture.root, responses[-1]['response']), rr.canonical(value))
        self.assertTrue(report['scope_policy_decisions'][0]['accepted'])

    def test_future_mode_freeze_and_runner_binding(self):
        config, frozen = self.execution(); config['presentation_mode'] = w.PRESENTATION
        with self.assertRaises(ValueError): solver.validate_execution_config(self.fixture.root, config)
        frozen['presentation_mode'] = w.PRESENTATION; frozen['runners']['solver_workflow.py'] = w.sha(Path(w.__file__).read_bytes())
        path = self.fixture.root/'execution-freeze.json'; path.write_bytes(rr.canonical(frozen)); config['freeze'] = rr.reference(self.fixture.root, path)
        solver.validate_execution_config(self.fixture.root, config); self.assertEqual(config['_presentation_mode'], w.PRESENTATION)
        frozen['runners']['solver_workflow.py'] = '0'*64; path.write_bytes(rr.canonical(frozen)); config['freeze'] = rr.reference(self.fixture.root, path)
        with self.assertRaises(ValueError): solver.validate_execution_config(self.fixture.root, config)
        with self.assertRaises(ValueError): solver.NativeBroker(None, None, ['read_source'], presentation_mode=w.PRESENTATION)

    def test_compact_mode_keeps_scope_rejection_and_error_signaling(self):
        value = sf.envelope(data={'results': [sf.node(repository='outside')]})
        broker = solver.NativeBroker(sf.FakeNative(value), self.record, sf.policy()['allowed_tools'],
                                    source_scope=self.scope, presentation_mode=w.PRESENTATION)
        response = self.handle(broker)
        self.assertTrue(response['result']['isError']); self.assertNotIn('outside', rr.canonical(response).decode())
        report = self.fixture.report(); self.assertFalse(report['validation_errors'])
        self.assertFalse(report['scope_policy_decisions'][0]['accepted']); self.assertEqual(report['calls'], 1)
        self.assertFalse(self.scope.nodes)

    def test_record_rejects_delivery_mode_mismatch_before_display_persistence(self):
        broker = solver.NativeBroker(sf.FakeNative(sf.envelope()), self.record, sf.policy()['allowed_tools'], source_scope=self.scope)
        with self.assertRaisesRegex(ValueError, 'display presentation mode differs from freeze'):
            self.handle(broker)  # Legacy presentation under a single-source freeze.
        self.assertFalse(self.fixture.report()['validation_errors'])
        self.assertEqual(w.recorded_displays(self.fixture.root, 'assignment-a'), [])

    def test_mode_receipt_tampering_invalidates_recorded_delivery_inputs(self):
        broker = solver.NativeBroker(sf.FakeNative(sf.envelope()), self.record, sf.policy()['allowed_tools'],
                                    source_scope=self.scope, presentation_mode=w.PRESENTATION)
        self.handle(broker)
        path = next((self.record.directory/'blobs').glob('*.scope-policy'))
        value = json.loads(path.read_bytes()); value['presentation_mode'] = 'legacy'
        path.write_bytes(rr.canonical(value))
        self.assertTrue(self.fixture.report()['validation_errors'])
        with self.assertRaises(ValueError): w.recorded_displays(self.fixture.root, 'assignment-a')


if __name__ == '__main__': unittest.main()
