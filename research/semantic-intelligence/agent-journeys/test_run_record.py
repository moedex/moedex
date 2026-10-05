#!/usr/bin/env python3
"""Synthetic recorder fixtures; no measured product results."""
import json
import subprocess
import sys
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import run_record as rr


class Clock:
    def __init__(self):
        self.value = 1000.0
    def __call__(self):
        return self.value


class Records(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.clock = Clock()
        proof = self.root / 'proof.json'
        proof.write_text('{"fixture":"synthetic"}\n')
        self.proof = rr.reference(self.root, proof)
        closure = self.root / 'closure.json'
        closure.write_bytes(rr.canonical({'complete': True, 'verification': self.proof}))
        self.closure = rr.reference(self.root, closure)
        self.manifest = {
            'model': {'revision': 'synthetic-revision-001', 'settings': {'temperature': 0}, 'verification': self.proof},
            'solver_records': {'capture_available': True, 'capture_probe': self.proof},
            'isolation': {'enforced': True, 'verification': self.proof},
            'preflight': {'same_execution_environment': True, 'verification': self.proof},
            'product': {'serving_sha256': 'b' * 64, 'verification': self.proof, 'dependencies': self.closure},
            'auditor': self.proof}
        self.identity = {'task': 'synthetic-task', 'arm': 'arm-a', 'solver_id': 'solver-a',
                         'contract_sha256': 'a' * 64, 'prompt_sha256': 'c' * 64}
        self.budgets = {'calls': 3, 'response_bytes': 20, 'assignment_seconds': 30, 'display_bytes': 10}

    def create(self, name='assignment-a', diagnostic=False):
        path = self.root / ('freeze-' + name + '.json')
        path.write_bytes(rr.canonical(self.manifest))
        return rr.RunRecord.create(self.root, name, self.identity, self.budgets,
                                   rr.reference(self.root, path), 'session-a', 'boot-a', self.clock, diagnostic)

    def report(self, name='assignment-a'):
        return rr.audit(self.root, name)

    def response(self, record, raw=b'ok', complete=True):
        n = record.begin_call(b'{"method":"synthetic"}')
        record.finish_call(n, raw, {'body_bytes_observed': len(raw), 'transport_complete': complete})

    def test_revisions_preserve_first_body_and_hash(self):
        record = self.create()
        first = record.submit_answer(b'first answer')
        self.clock.value += 1
        second = record.submit_answer(b'corrected answer')
        self.assertNotEqual(first, second)
        self.assertEqual(rr.read_ref(self.root, first), b'first answer')
        self.assertEqual(rr.read_ref(self.root, second), b'corrected answer')
        self.assertEqual([a['revision'] for a in self.report()['answers']], [1, 2])
        with self.assertRaises(FileExistsError):
            self.create()

    def test_write_ahead_attempt_and_no_retry_after_interruption(self):
        record = self.create()
        self.assertEqual(record.begin_call(b'request'), 1)
        report = self.report()
        self.assertEqual(report['calls'], 1)
        self.assertEqual(report['pending_call'], 1)
        self.assertFalse(report['transport_complete'])
        self.assertTrue(report['response_bytes_is_lower_bound'])
        self.assertFalse(report['benchmark_eligible'])
        with self.assertRaises(RuntimeError):
            record.begin_call(b'retry')
        with self.assertRaises(RuntimeError):
            record.submit_answer(b'answer')
        record.stop('interrupted caller')
        with self.assertRaises(RuntimeError):
            record.finish_call(1, b'ok', {'body_bytes_observed': 2, 'transport_complete': True})

    def test_reboot_and_coordinator_session_resume_rejected(self):
        self.create()
        for session, boot in [('session-a', 'boot-b'), ('session-b', 'boot-a')]:
            other = rr.RunRecord(self.root, 'assignment-a', session, boot, self.clock)
            with self.assertRaisesRegex(RuntimeError, 'another boot or coordinator session'):
                other.begin_call(b'bad resume')

    def test_assignment_clocks_start_individually(self):
        a = self.create('a')
        self.clock.value += 20
        b = self.create('b')
        self.clock.value += 15
        with self.assertRaisesRegex(RuntimeError, 'deadline'):
            a.submit_answer(b'too late')
        b.submit_answer(b'on time')
        self.assertEqual(self.report('b')['answers'][0]['elapsed_seconds'], 15)

    def test_clock_backwards_rejected(self):
        record = self.create()
        self.clock.value += 2
        record.submit_answer(b'answer')
        self.clock.value -= 1
        with self.assertRaisesRegex(RuntimeError, 'backwards'):
            record.submit_answer(b'other')

    def test_full_response_crossing_counted_and_stopped(self):
        record = self.create()
        self.response(record, b'x' * 21)
        report = self.report()
        self.assertEqual(report['response_bytes_observed'], 21)
        self.assertTrue(report['stopped'])
        self.assertIn('response budget exceeded', report['validation_errors'])

    def test_incomplete_response_preserves_partial_body(self):
        record = self.create()
        self.response(record, b'partial', False)
        report = self.report()
        self.assertTrue(report['stopped'])
        self.assertEqual(report['response_bytes_observed'], 7)
        self.assertTrue(report['response_bytes_is_lower_bound'])
        with self.assertRaises(RuntimeError):
            record.submit_answer(b'late answer')

    def test_answer_and_event_tamper_detected(self):
        record = self.create()
        first = record.submit_answer(b'first')
        self.clock.value += 1
        record.submit_answer(b'second')
        (self.root / first['path']).write_bytes(b'changed')
        self.assertTrue(self.report()['validation_errors'])
        with self.assertRaises(ValueError):
            record.submit_answer(b'third')
        (self.root / first['path']).write_bytes(b'first')
        path = record.directory / 'events' / '000001.json'
        event = json.loads(path.read_bytes())
        event['elapsed_seconds'] = 0.25
        path.write_bytes(rr.canonical(event) + b'\n')
        self.assertTrue(self.report()['validation_errors'])

    def test_assignment_identity_tamper_detected(self):
        record = self.create()
        record.submit_answer(b'first')
        path = record.directory / 'assignment.json'
        value = json.loads(path.read_bytes())
        value['identity']['arm'] = 'arm-b'
        path.write_bytes(rr.canonical(value) + b'\n')
        self.assertTrue(self.report()['validation_errors'])

    def test_symlink_evidence_directory_rejected_before_mutation(self):
        record = self.create()
        (record.directory / 'events').rmdir()
        outside = self.root / 'outside-events'
        outside.mkdir()
        (record.directory / 'events').symlink_to(outside, target_is_directory=True)
        self.assertTrue(self.report()['validation_errors'])
        with self.assertRaises(ValueError):
            record.begin_call(b'must not follow link')
        self.assertEqual(list(outside.iterdir()), [])

    def test_uncommitted_blob_stops_mutations(self):
        record = self.create()
        (record.directory / 'blobs' / '000001.request').write_bytes(b'crash before event commit')
        self.assertIn('orphaned/unrecorded evidence blob', self.report()['validation_errors'][0])
        with self.assertRaises(ValueError):
            record.begin_call(b'do not reset')

    def test_strict_references_traversal_symlink_and_changed_bytes(self):
        with self.assertRaises(ValueError):
            rr.read_ref(self.root, {'path': '../outside', 'sha256': 'a' * 64})
        link = self.root / 'link'
        link.symlink_to(self.root / 'proof.json')
        with self.assertRaises(ValueError):
            rr.reference(self.root, link)
        for path in ['./proof.json', 'proof.json/../proof.json']:
            with self.assertRaises(ValueError):
                rr.read_ref(self.root, {'path': path, 'sha256': self.proof['sha256']})
        changed = dict(self.proof, sha256='a' * 64)
        with self.assertRaises(ValueError):
            rr.read_ref(self.root, changed)

    def test_unknown_launch_gates_fail_closed_but_diagnostics_retained(self):
        self.manifest['model']['revision'] = 'unknown'
        self.manifest['solver_records']['capture_available'] = False
        self.manifest['isolation']['enforced'] = False
        self.manifest['product']['serving_sha256'] = None
        self.manifest['auditor'] = None
        with self.assertRaisesRegex(ValueError, 'launch gates failed'):
            self.create('rejected')
        record = self.create(diagnostic=True)
        record.submit_answer(b'diagnostic answer')
        report = self.report()
        self.assertGreaterEqual(len(report['eligibility_blockers']), 6)
        self.assertFalse(report['benchmark_eligible'])

    def test_invalid_freeze_reference_never_launches_diagnostic(self):
        self.manifest['model']['verification']['sha256'] = 'e' * 64
        record = self.create(diagnostic=True)
        self.assertIn('model verification reference missing or invalid', self.report()['eligibility_blockers'])
        self.assertFalse(self.report()['benchmark_eligible'])

    def test_positive_finite_budgets_required(self):
        for value in [float('nan'), float('inf'), 0, True]:
            self.budgets['assignment_seconds'] = value
            with self.assertRaises(ValueError):
                self.create('invalid-' + str(value))

    def seal(self, record, complete=True, reviewer='reviewer-a'):
        provider = self.root / 'provider.jsonl'
        provider.write_bytes(b'{"event":"synthetic provider record"}\n')
        provider_ref = rr.reference(self.root, provider)
        verification = self.root / 'verification.json'
        verification.write_bytes(rr.canonical({
            'assignment_sha256': rr.reference(self.root, record.directory / 'assignment.json')['sha256'],
            'solver_records': provider_ref, 'model_revision': self.manifest['model']['revision'],
            'complete': complete, 'access_checked': True, 'reviewer_id': reviewer}))
        record.seal(provider_ref, rr.reference(self.root, verification))

    def test_complete_verified_records_gate_eligibility(self):
        record = self.create()
        self.response(record)
        record.display(b'display\n')
        self.clock.value += 10
        record.submit_answer(b'answer')
        self.assertFalse(self.report()['benchmark_eligible'])
        self.seal(record)
        report = self.report()
        self.assertTrue(report['transcript_verified'])
        self.assertTrue(report['benchmark_eligible'], report)
        with self.assertRaises(RuntimeError):
            record.submit_answer(b'after seal')

    def test_independent_seal_after_deadline_does_not_change_solver_duration(self):
        record = self.create()
        self.clock.value += 10
        record.submit_answer(b'answer within deadline')
        self.clock.value += 50
        self.seal(record)
        report = self.report()
        self.assertTrue(report['benchmark_eligible'], report)
        self.assertEqual(report['assignment_seconds'], 10)
        self.assertEqual(report['last_elapsed_seconds'], 60)

    def test_answer_completion_after_blob_and_event_fsync_is_conservative(self):
        record = self.create()
        self.clock.value += 29
        original = rr._new_file
        def delayed(path, raw):
            original(path, raw)
            if path.name.endswith('.answer') or path.name == '000001.json':
                self.clock.value += 0.75
        with mock.patch.object(rr, '_new_file', side_effect=delayed):
            with self.assertRaisesRegex(RuntimeError, 'answer retained'):
                record.submit_answer(b'body that crosses deadline during persistence')
        report = self.report()
        answer = report['answers'][0]
        self.assertEqual(rr.read_ref(self.root, answer['answer']), b'body that crosses deadline during persistence')
        self.assertEqual(answer['submitted_elapsed_seconds'], 29.75)
        self.assertEqual(answer['elapsed_seconds'], 30.5)
        self.assertFalse(report['benchmark_eligible'])
        self.assertTrue(report['stopped'])

    def test_interrupt_after_answer_event_retains_body_but_blocks_mutation(self):
        record = self.create()
        original = rr._new_file
        def interrupt(path, raw):
            if path.name == '000002.json':
                raise OSError('synthetic interrupted completion receipt')
            original(path, raw)
        with mock.patch.object(rr, '_new_file', side_effect=interrupt):
            with self.assertRaises(OSError):
                record.submit_answer(b'first retained body')
        report = self.report()
        self.assertEqual(report['pending_answer'], 1)
        self.assertEqual(rr.read_ref(self.root, report['answers'][0]['answer']), b'first retained body')
        self.assertFalse(report['benchmark_eligible'])
        with self.assertRaises(RuntimeError):
            record.begin_call(b'must not continue')

    def test_lock_symlink_rejected(self):
        record = self.create()
        target = self.root / 'lock-target'
        target.write_bytes(b'unchanged')
        (record.directory / 'lock').symlink_to(target)
        with self.assertRaisesRegex(ValueError, 'lock symlink'):
            record.submit_answer(b'answer')
        self.assertEqual(target.read_bytes(), b'unchanged')

    def test_arbitrary_dependency_reference_does_not_prove_closure(self):
        self.manifest['product']['dependencies'] = self.proof
        with self.assertRaisesRegex(ValueError, 'dependency closure'):
            self.create('rejected-closure')
        self.create(diagnostic=True)
        self.assertFalse(self.report()['benchmark_eligible'])

    def test_missing_external_solver_records_invalidates_sealed_audit(self):
        record = self.create()
        record.submit_answer(b'answer')
        self.seal(record)
        (self.root / 'provider.jsonl').unlink()
        report = self.report()
        self.assertTrue(report['validation_errors'])
        self.assertFalse(report['transcript_verified'])
        self.assertFalse(report['benchmark_eligible'])

    def test_cli_never_overwrites_report_and_incomplete_is_not_pass(self):
        self.create()
        output = self.root / 'audit.json'
        command = [sys.executable, str(Path(rr.__file__).resolve()), '--root', str(self.root),
                   '--assignment', 'assignment-a', '--output', str(output)]
        first = subprocess.run(command, capture_output=True)
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertFalse(json.loads(output.read_bytes())['benchmark_eligible'])
        raw = output.read_bytes()
        second = subprocess.run(command, capture_output=True)
        self.assertNotEqual(second.returncode, 0)
        self.assertEqual(output.read_bytes(), raw)

    def test_freeze_and_refs_reverified_before_each_mutation(self):
        record = self.create()
        record.submit_answer(b'first')
        (self.root / self.proof['path']).write_bytes(b'changed verification')
        self.assertTrue(self.report()['validation_errors'])
        with self.assertRaises(ValueError):
            record.begin_call(b'must not trust stale gate')

    def test_solver_cannot_attest_own_complete_records(self):
        record = self.create()
        record.submit_answer(b'answer')
        self.seal(record, reviewer='solver-a')
        self.assertFalse(self.report()['transcript_verified'])

    def test_incomplete_provider_verification_never_eligible(self):
        record = self.create()
        record.submit_answer(b'answer')
        self.seal(record, complete=False)
        self.assertFalse(self.report()['benchmark_eligible'])

    def test_display_envelope_budget(self):
        record = self.create()
        with self.assertRaises(ValueError):
            record.display(b'x' * 11)
        record.display(b'x' * 10)
        self.assertEqual(self.report()['max_display_bytes'], 10)

    def test_ready_diagnostic_remains_excluded_after_independent_seal(self):
        record = self.create(diagnostic=True)
        record.submit_answer(b'diagnostic answer')
        self.seal(record)
        report = self.report()
        self.assertTrue(report['transcript_verified'])
        self.assertFalse(report['benchmark_eligible'])
        self.assertIn('diagnostic assignment is excluded from benchmarks', report['eligibility_blockers'])

    def test_non_answer_event_fsync_crossings_are_retained_and_stop(self):
        for kind in ['attempt', 'response', 'display']:
            with self.subTest(kind=kind):
                name = 'late-' + kind
                record = self.create(name)
                record.submit_answer(b'earlier valid answer')
                ordinal = record.begin_call(b'request') if kind == 'response' else None
                self.clock.value += 29
                original = rr._new_file
                def delayed(path, raw):
                    original(path, raw)
                    if path.parent.name == 'events' and json.loads(raw)['event'] == kind:
                        self.clock.value += 2
                with mock.patch.object(rr, '_new_file', side_effect=delayed):
                    if kind == 'response':
                        record.finish_call(ordinal, b'body', {'body_bytes_observed': 4, 'transport_complete': True})
                    else:
                        with self.assertRaisesRegex(RuntimeError, 'deadline'):
                            (record.begin_call if kind == 'attempt' else record.display)(b'body')
                report = self.report(name)
                self.assertTrue(report['stopped'], report)
                self.assertIsNone(report['pending_record'])
                self.assertFalse(report['benchmark_eligible'])
                self.assertEqual(report['execution_elapsed_seconds'], 31)
                self.assertIn('recording exceeded assignment deadline', report['eligibility_blockers'])

    def test_interrupted_event_completion_receipt_blocks_seal_and_resume(self):
        for kind in ['attempt', 'response', 'display']:
            with self.subTest(kind=kind):
                name = 'interrupted-' + kind
                record = self.create(name)
                record.submit_answer(b'earlier valid answer')
                ordinal = record.begin_call(b'request') if kind == 'response' else None
                self.clock.value += 29
                original = rr._new_file
                def interrupted(path, raw):
                    original(path, raw)
                    if path.parent.name == 'events' and json.loads(raw)['event'] == kind:
                        self.clock.value += 2
                        raise OSError('synthetic interruption after event fsync')
                with mock.patch.object(rr, '_new_file', side_effect=interrupted):
                    with self.assertRaises(OSError):
                        if kind == 'response':
                            record.finish_call(ordinal, b'body', {'body_bytes_observed': 4, 'transport_complete': True})
                        else:
                            (record.begin_call if kind == 'attempt' else record.display)(b'body')
                report = self.report(name)
                self.assertEqual(report['pending_record']['kind'], kind)
                self.assertFalse(report['benchmark_eligible'])
                self.assertEqual(rr.read_ref(self.root, report['answers'][0]['answer']), b'earlier valid answer')
                if kind == 'response':
                    self.assertEqual(report['response_bytes_observed'], 4)
                for mutation in [lambda: record.begin_call(b'other'),
                                 lambda: record.submit_answer(b'other'), record.remaining_seconds,
                                 lambda: self.seal(record)]:
                    with self.assertRaises(RuntimeError):
                        mutation()

    def test_remaining_seconds_tracks_residual_and_allows_pending_native_call(self):
        record = self.create()
        self.assertEqual(record.remaining_seconds(), 30)
        self.clock.value += 7
        self.assertEqual(record.remaining_seconds(), 23)
        record.begin_call(b'request')
        self.clock.value += 2
        self.assertEqual(record.remaining_seconds(), 21)
        for session, boot in [('session-a', 'boot-b'), ('session-b', 'boot-a')]:
            other = rr.RunRecord(self.root, 'assignment-a', session, boot, self.clock)
            with self.assertRaisesRegex(RuntimeError, 'another boot or coordinator session'):
                other.remaining_seconds()
        self.clock.value += 21
        with self.assertRaisesRegex(RuntimeError, 'deadline'):
            record.remaining_seconds()
        record.stop('deadline')
        with self.assertRaisesRegex(RuntimeError, 'stopped'):
            record.remaining_seconds()

    def test_remaining_seconds_rejects_tampered_evidence(self):
        record = self.create()
        answer = record.submit_answer(b'answer')
        (self.root / answer['path']).write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError, 'audit failed'):
            record.remaining_seconds()

    def test_non_object_solver_verification_is_rejected_and_audited(self):
        record = self.create()
        record.submit_answer(b'answer')
        provider = self.root / 'provider.jsonl'
        provider.write_bytes(b'provider')
        provider_ref = rr.reference(self.root, provider)
        for raw in [b'[]', b'null', b'"proof"']:
            verification = self.root / 'wrong-verification.json'
            verification.write_bytes(raw)
            with self.assertRaisesRegex(ValueError, 'must be an object'):
                record.seal(provider_ref, rr.reference(self.root, verification))
        # An externally authored malformed event still produces a fail-closed
        # audit report, rather than an uncaught AttributeError.
        state = self.report()
        record._event(state, state['last_elapsed_seconds'], 'seal', solver_records=provider_ref,
                      verification=rr.reference(self.root, verification))
        report = self.report()
        self.assertIn('solver verification and freeze must be objects', report['validation_errors'])
        self.assertFalse(report['benchmark_eligible'])


if __name__ == '__main__':
    unittest.main()
