"""Offline reference usability and recorded-byte binding; no model traffic."""
from copy import deepcopy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import isolated_solver as solver
import native_scope as ns
import run_record as rr
import solver_workflow as w
import test_native_scope as sf
import test_run_record as rf
import test_solver_workflow as wf


def referenced_answer(raw, identity='display-1'):
    value = wf.answer(raw)
    citation = value['claims'][0]['citations'][0]
    citation.pop('display_sha256')
    citation['presentation_id'] = identity
    return value


class ReferenceChecker(unittest.TestCase):
    def test_both_presentation_modes_expose_id_and_resolve_exact_recorded_hash(self):
        original = wf.source()
        for mode in (None, w.PRESENTATION):
            with self.subTest(mode=mode):
                shown = solver.present_response(original, 8192, mode, w.REFERENCE_MODE, 7)
                raw = rr.canonical(shown)
                self.assertEqual(shown['result']['_meta'][w.REFERENCE_KEY], {'mode': w.REFERENCE_MODE, 'id': 'display-7'})
                texts = '\n'.join(row['text'] for row in shown['result']['content'])
                self.assertIn('Presentation reference: display-7.', texts)
                report = w.check(wf.plan(), referenced_answer(raw, 'display-7'), [raw])
                self.assertTrue(report['mechanically_valid'])
                self.assertEqual(report['presentation_hashes'], {'display-7': w.sha(raw)})
                self.assertEqual(report['resolved_citations'][0]['display_sha256'], w.sha(raw))
                self.assertEqual(original, wf.source())
                if mode:
                    self.assertNotEqual(shown['result']['_meta']['dev.moedex/display']['validated_envelope_sha256'], w.sha(raw))
                original_answer = referenced_answer(raw, 'display-7'); before = deepcopy(original_answer)
                w.check(wf.plan(), original_answer, [raw])
                self.assertEqual(original_answer, before)

    def test_legacy_no_reference_mode_preserves_exact_bytes_and_hash_locators(self):
        for mode in (None, w.PRESENTATION):
            response = wf.source()
            before = solver.bounded_response(response, 8192) if mode is None else w.single_source(response, 8192)
            after = solver.present_response(response, 8192, mode)
            self.assertEqual(rr.canonical(before), rr.canonical(after))
            raw = rr.canonical(after)
            self.assertTrue(w.check(wf.plan(), wf.answer(raw), [raw])['mechanically_valid'])
            self.assertEqual(w.check(wf.plan(), wf.answer(raw), [raw])['presentation_hashes'], {})

    def test_unknown_spoofed_colliding_and_dual_locators_fail_closed(self):
        first = rr.canonical(solver.present_response(wf.source(), 8192, None, w.REFERENCE_MODE, 1))
        second = rr.canonical(solver.present_response(wf.source(width=21), 8192, None, w.REFERENCE_MODE, 1))
        a = referenced_answer(first)
        report = w.check(wf.plan(), a, [first, first])
        self.assertTrue(report['mechanically_valid'])
        report = w.check(wf.plan(), a, [first, second])
        self.assertFalse(report['mechanically_valid']); self.assertIn('ambiguous presentation ID: display-1', report['errors'])
        self.assertEqual(report['presentation_hashes'], {})
        a['claims'][0]['citations'][0]['presentation_id'] = 'display-999'
        self.assertFalse(w.check(wf.plan(), a, [first])['mechanically_valid'])
        a = referenced_answer(first); a['claims'][0]['citations'][0]['display_sha256'] = w.sha(second)
        self.assertFalse(w.check(wf.plan(), a, [first, second])['mechanically_valid'])
        a['claims'][0]['citations'][0]['display_sha256'] = w.sha(first)
        self.assertTrue(w.check(wf.plan(), a, [first])['mechanically_valid'])
        a['claims'][0]['citations'][0]['end_line'] = 4
        self.assertFalse(w.check(wf.plan(), a, [first])['mechanically_valid'])
        for bad in ({'id': 'display-1'}, {'mode': w.REFERENCE_MODE, 'id': 'display-01'},
                    {'mode': 'invented', 'id': 'display-1'}, {'mode': w.REFERENCE_MODE, 'id': 'display-1', 'extra': True}):
            shown = json.loads(first); shown['result']['_meta'][w.REFERENCE_KEY] = bad
            self.assertFalse(w.check(wf.plan(), referenced_answer(first), [rr.canonical(shown)])['mechanically_valid'])

    def test_prefix_fallback_and_denied_replies_have_no_source_credit(self):
        response = wf.source(count=1000, width=100)
        shown = solver.present_response(response, 8192, None, w.REFERENCE_MODE, 3)
        self.assertLessEqual(len(rr.canonical(shown)), 8192)
        self.assertEqual(w.shown_sources(shown), [])
        self.assertIsNone(w.reference_from_response(shown))
        report = w.check(wf.plan(), referenced_answer(rr.canonical(shown), 'display-3'), [rr.canonical(shown)])
        self.assertFalse(report['mechanically_valid']); self.assertEqual(report['presentation_hashes'], {})
        denied = solver.present_response(wf.source(), 8192, None, w.REFERENCE_MODE, 3)
        denied['result']['isError'] = True
        raw = rr.canonical(denied)
        report = w.check(wf.plan(), referenced_answer(raw, 'display-3'), [raw])
        self.assertFalse(report['mechanically_valid']); self.assertEqual(report['presentation_hashes'], {})
        decision = {'citation_reference_mode': w.REFERENCE_MODE, 'accepted': False, 'ordinal': 3}
        with self.assertRaisesRegex(ValueError, 'denied scope presentation'):
            w.validate_recorded_reference(rr.canonical(wf.source()), decision, w.REFERENCE_MODE)

    def test_opt_in_plan_instructions_and_original_plan_compatibility(self):
        original = wf.plan()
        opted = w.prepare(original['request'], original['inventory'], w.REFERENCE_MODE)
        self.assertIn('Do not calculate hashes of unknown', opted['instructions'])
        self.assertIn('presentation_id', opted['instructions'])
        raw = rr.canonical(solver.present_response(wf.source(), 8192, None, w.REFERENCE_MODE, 1))
        self.assertTrue(w.check(opted, referenced_answer(raw), [raw])['mechanically_valid'])
        self.assertTrue(w.check(original, referenced_answer(raw), [raw])['mechanically_valid'])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root/'request').write_text(original['request'])
            (root/'inventory').write_bytes(w.canonical(original['inventory']))
            result = subprocess.run([sys.executable, '-B', w.__file__, 'prepare', '--request', str(root/'request'),
                                     '--inventory', str(root/'inventory'), '--citation-reference-mode', w.REFERENCE_MODE],
                                    capture_output=True, check=True)
            self.assertEqual(json.loads(result.stdout), opted)
            (root/'plan').write_bytes(result.stdout)
            (root/'answer').write_bytes(w.canonical(referenced_answer(raw)))
            (root/'display').write_bytes(raw)
            checked = subprocess.run([sys.executable, '-B', w.__file__, 'check', '--plan', str(root/'plan'),
                                      '--answer', str(root/'answer'), '--display', str(root/'display')],
                                     capture_output=True, check=True)
            self.assertEqual(json.loads(checked.stdout)['presentation_hashes'], {'display-1': w.sha(raw)})


class ReferenceBroker(unittest.TestCase):
    handle = sf.BrokerScopeTests.handle
    execution = sf.BrokerScopeTests.execution
    def setup_record(self, mode):
        self.fixture = rf.Records(); self.fixture.setUp(); self.addCleanup(self.fixture.tmp.cleanup)
        self.fixture.budgets = {'calls': 8, 'response_bytes': 1<<20, 'assignment_seconds': 30, 'display_bytes': 8192}
        path = self.fixture.root/'scope.json'; path.write_bytes(rr.canonical(sf.policy()))
        self.fixture.manifest.update(source_scope_policy=rr.reference(self.fixture.root, path), source_scope_review=self.fixture.proof,
                                     citation_reference_mode=w.REFERENCE_MODE)
        if mode: self.fixture.manifest['presentation_mode'] = mode
        self.record = self.fixture.create(); self.scope = ns.NativeScope(sf.policy())

    def test_frozen_reference_broker_replay_raw_retention_and_record_mapping(self):
        for mode in (None, w.PRESENTATION):
            with self.subTest(mode=mode):
                self.setup_record(mode)
                value = sf.source(cursor='next-page')
                client = sf.FakeNative(value)
                broker = solver.NativeBroker(client, self.record, sf.policy()['allowed_tools'], source_scope=self.scope,
                                            presentation_mode=mode, citation_reference_mode=w.REFERENCE_MODE)
                response = self.handle(broker, sf.request('graph_source', filePath='src/api.cs'))
                raw = rr.canonical(response)
                self.assertEqual(self.scope.cursors['next-page'], ('example/api', 'src/api.cs'))
                report = self.fixture.report(); self.assertFalse(report['validation_errors'])
                decision = report['scope_policy_decisions'][0]
                self.assertEqual(decision['presentation_id'], 'display-1'); self.assertEqual(decision['citation_reference_mode'], w.REFERENCE_MODE)
                self.assertEqual(w.recorded_displays(self.fixture.root, 'assignment-a'), [raw])
                replay = ns.NativeScope(sf.policy()); accepted = replay.accept(value, replay.prepare(sf.request('graph_source', filePath='src/api.cs')))
                self.assertEqual(rr.canonical(solver.present_response(accepted, 8192, mode, w.REFERENCE_MODE, 1)), raw)
                events = [json.loads(path.read_bytes()) for path in (self.record.directory/'events').glob('*.json')]
                retained = [e for e in events if e.get('event') == 'response'][0]
                self.assertEqual(rr.read_ref(self.fixture.root, retained['response']), rr.canonical(value))
                a = referenced_answer(raw); a['claims'][0]['citations'][0].update(repo='Example.Api', path='src/api.cs', start_line=1, end_line=1)
                self.assertTrue(w.check(wf.plan(), a, w.recorded_displays(self.fixture.root, 'assignment-a'))['mechanically_valid'])

    def test_denied_scope_requests_preserve_modes_errors_and_no_id(self):
        self.setup_record(w.PRESENTATION)
        broker = solver.NativeBroker(sf.FakeNative(sf.envelope()), self.record, sf.policy()['allowed_tools'], source_scope=self.scope,
                                    presentation_mode=w.PRESENTATION, citation_reference_mode=w.REFERENCE_MODE)
        response = self.handle(broker, sf.request('graph_source', filePath='outside.cs'))
        self.assertTrue(response['result']['isError']); self.assertIsNone(w.reference_from_response(response))
        report = self.fixture.report(); self.assertFalse(report['validation_errors'])
        self.assertNotIn('presentation_id', report['scope_policy_decisions'][0])

    def test_config_requires_exact_frozen_mode_scope_and_helper_hash_even_for_legacy(self):
        self.setup_record(None)
        config, frozen = self.execution(); config['citation_reference_mode'] = w.REFERENCE_MODE
        with self.assertRaisesRegex(ValueError, 'citation reference mode differs'): solver.validate_execution_config(self.fixture.root, config)
        frozen['citation_reference_mode'] = w.REFERENCE_MODE
        path = self.fixture.root/'execution-freeze.json'; path.write_bytes(rr.canonical(frozen)); config['freeze'] = rr.reference(self.fixture.root, path)
        with self.assertRaisesRegex(ValueError, 'solver_workflow.py'): solver.validate_execution_config(self.fixture.root, config)
        frozen['runners']['solver_workflow.py'] = w.sha(Path(w.__file__).read_bytes())
        path.write_bytes(rr.canonical(frozen)); config['freeze'] = rr.reference(self.fixture.root, path)
        solver.validate_execution_config(self.fixture.root, config)
        self.assertEqual(config['_citation_reference_mode'], w.REFERENCE_MODE); self.assertNotIn('_presentation_mode', config)
        with self.assertRaises(ValueError): solver.NativeBroker(None, None, ['read_source'], citation_reference_mode=w.REFERENCE_MODE)
        with self.assertRaises(ValueError): solver.present_response(wf.source(), 8192, None, w.REFERENCE_MODE)
        del frozen['source_scope_policy']; path.write_bytes(rr.canonical(frozen)); config['freeze'] = rr.reference(self.fixture.root, path)
        with self.assertRaisesRegex(ValueError, 'frozen source scope'): solver.validate_execution_config(self.fixture.root, config)

    def test_record_preflight_and_audit_reject_reference_spoof_or_missing_mode(self):
        self.setup_record(None)
        decision = {'citation_reference_mode': w.REFERENCE_MODE, 'accepted': True, 'ordinal': 1, 'presentation_id': 'display-2'}
        raw = rr.canonical(solver.present_response(wf.source(), 8192, None, w.REFERENCE_MODE, 1))
        with self.assertRaisesRegex(ValueError, 'accepted native ordinal'): self.record.display(raw, scope_policy=decision)
        decision['presentation_id'] = 'display-1'
        wrong = rr.canonical(solver.present_response(wf.source(), 8192, None, w.REFERENCE_MODE, 2))
        with self.assertRaisesRegex(ValueError, 'differs from scope decision'): self.record.display(wrong, scope_policy=decision)
        with self.assertRaisesRegex(ValueError, 'mode differs from freeze'): self.record.display(raw)
        self.assertFalse(self.fixture.report()['validation_errors'])
        # Audit validates markers independently of the caller's receipt hash.
        with self.assertRaisesRegex(ValueError, 'differs from scope decision'): w.validate_recorded_reference(wrong, decision, w.REFERENCE_MODE)
        broker = solver.NativeBroker(sf.FakeNative(sf.source()), self.record, sf.policy()['allowed_tools'], source_scope=self.scope, citation_reference_mode=w.REFERENCE_MODE)
        self.handle(broker, sf.request('graph_source', filePath='src/api.cs'))
        path = next((self.record.directory/'blobs').glob('*.scope-policy'))
        receipt = json.loads(path.read_bytes()); receipt['presentation_id'] = 'display-2'; path.write_bytes(rr.canonical(receipt))
        self.assertTrue(self.fixture.report()['validation_errors'])
        with self.assertRaises(ValueError): w.recorded_displays(self.fixture.root, 'assignment-a')


if __name__ == '__main__': unittest.main()
