#!/usr/bin/env python3
"""Append-only native assignment records, conservative launch gates, offline audit.

This records evidence, not semantic scores or proof that a solver was isolated.
An independent reviewer must verify the referenced isolation/provider records.
"""
import argparse
import contextlib
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time

from journey_clock import monotonic
import provenance

SHA = re.compile(r'^[0-9a-f]{64}$')
NAME = re.compile(r'^[a-zA-Z0-9][a-zA-Z0-9_.-]*$')


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def read_ref(root, ref):
    """Verify an exact relative reference; reject traversal and every symlink."""
    if not isinstance(ref, dict) or set(ref) != {'path', 'sha256'}:
        raise ValueError('reference must contain exactly path and sha256')
    if not isinstance(ref['sha256'], str) or not SHA.fullmatch(ref['sha256']):
        raise ValueError('invalid reference digest')
    name = ref['path']
    if not isinstance(name, str) or not name or '\\' in name:
        raise ValueError('invalid reference path')
    path = Path(name)
    if path.is_absolute() or '..' in path.parts or name != path.as_posix():
        raise ValueError('reference path must be normalized and relative')
    root = Path(root).resolve()
    cursor = root
    for part in path.parts:
        cursor = cursor / part
        if cursor.is_symlink():
            raise ValueError('symlink reference rejected')
    if not cursor.is_file():
        raise ValueError('reference is not a file')
    raw = cursor.read_bytes()
    if digest(raw) != ref['sha256']:
        raise ValueError('reference digest mismatch')
    return raw


def reference(root, path):
    root, path = Path(root).absolute(), Path(path)
    if not path.is_absolute():
        path = root / path
    relative = path.relative_to(root).as_posix()
    result = {'path': relative, 'sha256': digest(path.read_bytes())}
    read_ref(root, result)
    return result


def _new_file(path, raw):
    """Create once and fsync, including the containing directory."""
    fd = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as output:
        output.write(raw)
        output.flush()
        os.fsync(output.fileno())
    directory = os.open(str(Path(path).parent), os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def boot_identity():
    """Stable during an OS boot, distinct after reboot; never silently guess."""
    if sys.platform.startswith('linux'):
        return 'linux:' + Path('/proc/sys/kernel/random/boot_id').read_text().strip()
    if sys.platform == 'darwin':
        value = subprocess.check_output(['/usr/sbin/sysctl', '-n', 'kern.boottime'], text=True).strip()
        if value:
            return 'darwin:' + value
    raise RuntimeError('no supported boot identity; supply a verified OS boot identity')


def freeze_gate(root, manifest):
    """Return launch blockers. Referenced evidence still needs independent review.

    Unknown or absent values fail closed; diagnostic assignments may retain the
    blockers, but cannot become benchmark eligible simply by answering a task.
    """
    errors = []
    if not isinstance(manifest, dict):
        return ['freeze must be an object']
    def require(condition, reason):
        if not condition:
            errors.append(reason)
    def evidence(value, label):
        try:
            read_ref(root, value)
        except (ValueError, OSError, TypeError):
            errors.append(label + ' reference missing or invalid')
    errors.extend(provenance.validate(root, manifest, read_ref))
    try:
        observed = provenance.mode(manifest) == provenance.OBSERVED
    except ValueError:
        observed = False
    model = manifest.get('model', {})
    require(isinstance(model, dict), 'model must be an object')
    if not isinstance(model, dict):
        model = {}
    if not observed:
        require(isinstance(model.get('revision'), str) and bool(model['revision'].strip()) and
                model['revision'].strip().lower() not in ('unknown', 'latest', 'unavailable'), 'exact model revision unknown')
    require(isinstance(model.get('settings'), dict) and bool(model['settings']), 'model settings missing')
    if not observed:
        evidence(model.get('verification'), 'model verification')
    records = manifest.get('solver_records', {})
    if not isinstance(records, dict):
        records = {}
    require(records.get('capture_available') is True, 'full solver record capture unavailable')
    evidence(records.get('capture_probe'), 'solver capture probe')
    isolation = manifest.get('isolation', {})
    if not isinstance(isolation, dict):
        isolation = {}
    require(isolation.get('enforced') is True, 'solver isolation not enforced')
    evidence(isolation.get('verification'), 'isolation verification')
    preflight = manifest.get('preflight', {})
    if not isinstance(preflight, dict):
        preflight = {}
    require(preflight.get('same_execution_environment') is True, 'preflight environment does not match solver')
    evidence(preflight.get('verification'), 'execution preflight')
    product = manifest.get('product', {})
    if not isinstance(product, dict):
        product = {}
    if not observed:
        require(isinstance(product.get('serving_sha256'), str) and
                bool(SHA.fullmatch(product['serving_sha256'])), 'currently serving immutable product digest unknown')
        evidence(product.get('verification'), 'serving product verification')
        evidence(product.get('dependencies'), 'runtime dependency closure')
        try:
            closure = json.loads(read_ref(root, product.get('dependencies')))
            require(isinstance(closure, dict) and closure.get('complete') is True,
                    'runtime dependency closure not explicitly complete')
            if isinstance(closure, dict):
                evidence(closure.get('verification'), 'dependency closure verification')
        except (ValueError, OSError, TypeError):
            errors.append('runtime dependency closure manifest invalid')
    evidence(manifest.get('auditor'), 'prehashed auditor')
    if manifest.get('source_scope_policy') is not None:
        evidence(manifest.get('source_scope_review'), 'source scope review')
    return errors


class RunRecord:
    """One assignment: persistent attempts, revisions and stop state, no resets.

    Call begin_call BEFORE issuing network I/O. If it returns, the attempt is
    durably recorded. A crashed caller leaves a pending attempt; no retry or
    answer mutation is permitted on that assignment. Use a fresh assignment.
    """
    def __init__(self, root, assignment, session_id, boot_id=None, clock=monotonic):
        self.root = Path(root).resolve()
        if not NAME.fullmatch(assignment):
            raise ValueError('invalid assignment ID')
        self.directory = self.root / assignment
        self.session_id = session_id
        self.boot_id = boot_id if boot_id is not None else boot_identity()
        self.clock = clock

    @classmethod
    def create(cls, root, assignment, identity, budgets, freeze, session_id,
               boot_id=None, clock=monotonic, diagnostic=False):
        result = cls(root, assignment, session_id, boot_id, clock)
        if not isinstance(session_id, str) or not session_id:
            raise ValueError('session identity required')
        if not isinstance(identity, dict) or any(not isinstance(identity.get(k), str) or not identity[k]
                                                for k in ('task', 'arm', 'solver_id', 'contract_sha256', 'prompt_sha256')):
            raise ValueError('complete assignment identity required')
        if any(not SHA.fullmatch(identity[k]) for k in ('contract_sha256', 'prompt_sha256')):
            raise ValueError('invalid assignment identity digest')
        expected = {'calls', 'response_bytes', 'assignment_seconds', 'display_bytes'}
        if (not isinstance(budgets, dict) or set(budgets) != expected or
                any(isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v) or v <= 0
                    for v in budgets.values()) or
                any(not isinstance(budgets[k], int) for k in ('calls', 'response_bytes', 'display_bytes'))):
            raise ValueError('positive finite budgets required')
        frozen = json.loads(read_ref(result.root, freeze))
        blockers = freeze_gate(result.root, frozen)
        if blockers and not diagnostic:
            raise ValueError('launch gates failed: ' + '; '.join(blockers))
        result.directory.mkdir(mode=0o700)
        (result.directory / 'events').mkdir(mode=0o700)
        (result.directory / 'blobs').mkdir(mode=0o700)
        start = clock()
        if not math.isfinite(start) or start < 0:
            raise ValueError('invalid monotonic clock')
        _new_file(result.directory / 'assignment.json', canonical({
            'schema': 'native-run-record-v1', 'assignment': assignment,
            'identity': identity, 'budgets': budgets, 'freeze': freeze,
            'session_id': session_id, 'boot_id': result.boot_id,
            'clock': 'journey_clock.monotonic-v1', 'assigned_monotonic': start,
            'assigned_unix': time.time(), 'launch_blockers': blockers,
            'diagnostic': bool(diagnostic)}) + b'\n')
        return result

    @contextlib.contextmanager
    def _locked(self):
        lock_path = self.directory / 'lock'
        if lock_path.is_symlink():
            raise ValueError('assignment lock symlink rejected')
        fd = os.open(str(lock_path), os.O_RDWR | os.O_CREAT | getattr(os, 'O_NOFOLLOW', 0), 0o600)
        with os.fdopen(fd, 'a+b') as lock:
            if not stat.S_ISREG(os.fstat(lock.fileno()).st_mode):
                raise ValueError('assignment lock is not a regular file')
            fcntl.flock(lock, fcntl.LOCK_EX)
            state = audit(self.root, self.directory.name)
            if state['validation_errors']:
                raise ValueError('record audit failed: ' + '; '.join(state['validation_errors']))
            assignment = json.loads((self.directory / 'assignment.json').read_bytes())
            if assignment['boot_id'] != self.boot_id or assignment['session_id'] != self.session_id:
                raise RuntimeError('assignment belongs to another boot or coordinator session')
            elapsed = self.clock() - assignment['assigned_monotonic']
            if not math.isfinite(elapsed) or elapsed < state['last_elapsed_seconds']:
                raise RuntimeError('assignment clock moved backwards')
            yield assignment, state, elapsed

    def _event(self, state, elapsed, kind, **values):
        number = state['event_count'] + 1
        assignment = json.loads((self.directory / 'assignment.json').read_bytes())
        completed = (elapsed if kind.endswith('_commit') else
                     self.clock() - assignment['assigned_monotonic'])
        if (not math.isfinite(completed) or completed < elapsed or
                completed < state['last_elapsed_seconds']):
            raise RuntimeError('assignment clock moved backwards during persistence')
        event = dict(values, event=kind, sequence=number, elapsed_seconds=completed,
                     previous_sha256=state['last_event_sha256'])
        _new_file(self.directory / 'events' / ('%06d.json' % number), canonical(event) + b'\n')
        return event

    def _blob(self, state, kind, raw):
        if not isinstance(raw, bytes):
            raise TypeError('evidence must be bytes')
        path = self.directory / 'blobs' / ('%06d.%s' % (state['event_count'] + 1, kind))
        _new_file(path, raw)
        return reference(self.root, path)

    @staticmethod
    def _active(assignment, state, elapsed):
        if (state['pending_call'] is not None or state['pending_answer'] is not None or
                state['pending_record'] is not None or state['stopped']):
            raise RuntimeError('assignment stopped or has unresolved attempt')
        if elapsed >= assignment['budgets']['assignment_seconds']:
            raise RuntimeError('assignment deadline exceeded')

    def remaining_seconds(self):
        """Return the live residual deadline, including during a native request.

        Hosts must apply this residual to provider and native transport timeouts.
        A pending call is expected while receiving its response; an interrupted
        persistence receipt or answer submission is never resumable.
        """
        with self._locked() as (assignment, state, elapsed):
            if (state['stopped'] or state['pending_record'] is not None or
                    state['pending_answer'] is not None):
                raise RuntimeError('assignment stopped or has unresolved persistence')
            remaining = assignment['budgets']['assignment_seconds'] - elapsed
            if remaining <= 0:
                raise RuntimeError('assignment deadline exceeded')
            return remaining

    def _complete_record(self, assignment, event, reason=None):
        # Sample after evidence and event fsync. Persisting this administrative
        # receipt does not shift that boundary. Missing receipts fail closed.
        completed = self.clock() - assignment['assigned_monotonic']
        current = audit(self.root, self.directory.name)
        if completed >= assignment['budgets']['assignment_seconds'] and reason is None:
            reason = 'assignment deadline exceeded while persisting ' + event['event']
        self._event(current, completed, event['event'] + '_commit',
                    record_sequence=event['sequence'], stop_reason=reason)
        return reason

    def begin_call(self, request):
        with self._locked() as (assignment, state, elapsed):
            self._active(assignment, state, elapsed)
            if state['calls'] >= assignment['budgets']['calls'] or state['response_bytes_observed'] >= assignment['budgets']['response_bytes']:
                raise RuntimeError('native budget exhausted')
            ordinal = state['calls'] + 1
            ref = self._blob(state, 'request', request)
            event = self._event(state, elapsed, 'attempt', ordinal=ordinal, request=ref)
            if self._complete_record(assignment, event):
                raise RuntimeError('assignment deadline exceeded while recording attempt')
            return ordinal

    def finish_call(self, ordinal, response, receipt):
        with self._locked() as (assignment, state, elapsed):
            if state['stopped'] or state['pending_record'] is not None or state['pending_call'] != ordinal:
                raise RuntimeError('response has no matching pending attempt')
            if (not isinstance(receipt, dict) or type(receipt.get('transport_complete')) is not bool or
                    type(receipt.get('body_bytes_observed')) is not int or receipt['body_bytes_observed'] != len(response)):
                raise ValueError('receipt byte count/completeness must match retained response')
            raw_ref = self._blob(state, 'response', response)
            receipt_ref = self._blob(state, 'receipt', canonical(receipt) + b'\n')
            elapsed = self.clock() - assignment['assigned_monotonic']
            reason = None
            if not receipt['transport_complete']:
                reason = 'incomplete transport'
            elif state['response_bytes_observed'] + len(response) > assignment['budgets']['response_bytes']:
                reason = 'response budget exceeded'
            elif elapsed >= assignment['budgets']['assignment_seconds']:
                reason = 'assignment deadline exceeded'
            event = self._event(state, elapsed, 'response', ordinal=ordinal, response=raw_ref,
                                receipt=receipt_ref, stop_reason=reason)
            self._complete_record(assignment, event, reason)
            return raw_ref

    def display(self, raw, scope_policy=None):
        with self._locked() as (assignment, state, elapsed):
            self._active(assignment, state, elapsed)
            if len(raw) > assignment['budgets']['display_bytes']:
                raise ValueError('complete display envelope exceeds budget')
            ref = self._blob(state, 'display', raw)
            values = {'display': ref}
            if scope_policy is not None:
                values['scope_policy'] = self._blob(state, 'scope-policy', canonical(scope_policy) + b'\n')
            event = self._event(state, elapsed, 'display', **values)
            if self._complete_record(assignment, event):
                raise RuntimeError('assignment deadline exceeded while recording display')
            return ref

    def submit_answer(self, raw):
        with self._locked() as (assignment, state, elapsed):
            self._active(assignment, state, elapsed)
            ref = self._blob(state, 'answer', raw)
            revision = len(state['answers']) + 1
            self._event(state, elapsed, 'answer', revision=revision, answer=ref)
            # The answer body AND its submission event are durable before this
            # completion boundary. The following event retains that boundary;
            # its own fsync is administrative receipt persistence.
            completed = self.clock() - assignment['assigned_monotonic']
            current = audit(self.root, self.directory.name)
            self._event(current, completed, 'answer_commit', revision=revision,
                        stop_reason=('assignment deadline exceeded while persisting answer'
                                     if completed >= assignment['budgets']['assignment_seconds'] else None))
            if completed >= assignment['budgets']['assignment_seconds']:
                raise RuntimeError('answer retained but assignment deadline exceeded')
            return ref

    def stop(self, reason):
        if not isinstance(reason, str) or not reason:
            raise ValueError('stop reason required')
        with self._locked() as (_, state, elapsed):
            if state['stopped']:
                raise RuntimeError('assignment already stopped')
            self._event(state, elapsed, 'stop', reason=reason)

    def seal(self, solver_records, verification):
        """Attach final provider records and independent verification, then stop.

        verification JSON must bind records, assignment and exact model revision,
        assert complete/access_checked, and name a reviewer distinct from solver.
        The audit validates bindings; the assertions need independent scrutiny.
        """
        with self._locked() as (assignment, state, elapsed):
            if (state['pending_call'] is not None or state['pending_answer'] is not None or
                    state['pending_record'] is not None or state['stopped']):
                raise RuntimeError('assignment stopped or has unresolved attempt')
            read_ref(self.root, solver_records)
            if not isinstance(json.loads(read_ref(self.root, verification)), dict):
                raise ValueError('solver verification must be an object')
            self._event(state, elapsed, 'seal', solver_records=solver_records, verification=verification)


def audit(root, assignment):
    """Offline audit. Missing attempts/responses never become known zero bytes."""
    root = Path(root).resolve()
    if not isinstance(assignment, str) or not NAME.fullmatch(assignment):
        raise ValueError('invalid assignment ID')
    directory = root / assignment
    result = {'assignment': assignment, 'event_count': 0, 'calls': 0,
              'response_bytes_observed': 0, 'max_display_bytes': 0, 'pending_call': None,
              'stopped': False, 'answers': [], 'pending_answer': None, 'pending_record': None,
              'last_event_sha256': None,
              'last_elapsed_seconds': 0, 'execution_elapsed_seconds': 0, 'transport_complete': True,
              'transcript_verified': False, 'validation_errors': [], 'eligibility_blockers': []}
    errors = result['validation_errors']
    try:
        if any(p.is_symlink() for p in (directory, directory / 'events', directory / 'blobs')):
            raise ValueError('assignment evidence directory symlink rejected')
        assignment_file = directory / 'assignment.json'
        assignment_ref = reference(root, assignment_file)
        value = json.loads(read_ref(root, assignment_ref))
        if value['schema'] != 'native-run-record-v1' or value['assignment'] != assignment:
            raise ValueError('assignment identity mismatch')
        frozen = json.loads(read_ref(root, value['freeze']))
        scope_policy_sha256 = None
        if frozen.get('source_scope_policy') is not None:
            read_ref(root, frozen['source_scope_policy'])
            scope_policy_sha256 = frozen['source_scope_policy']['sha256']
        result['eligibility_blockers'] = freeze_gate(root, frozen)
        if value['launch_blockers'] != result['eligibility_blockers']:
            raise ValueError('launch gate record mismatch')
        if value.get('diagnostic') is True:
            result['eligibility_blockers'].append('diagnostic assignment is excluded from benchmarks')
        last_hash = digest(assignment_file.read_bytes())
        # Assignment hash anchors the event chain; empty ledger retains it too.
        result['last_event_sha256'] = last_hash
        used, responses = set(), {}
        for number, path in enumerate(sorted((directory / 'events').iterdir()), 1):
            if path.name != '%06d.json' % number:
                raise ValueError('event sequence missing or unexpected file')
            raw = read_ref(root, reference(root, path))
            event = json.loads(raw)
            if (event['sequence'] != number or event['previous_sha256'] != last_hash or
                    not isinstance(event['elapsed_seconds'], (int, float)) or isinstance(event['elapsed_seconds'], bool) or
                    not math.isfinite(event['elapsed_seconds']) or event['elapsed_seconds'] < result['last_elapsed_seconds']):
                raise ValueError('event chain or monotonic timing mismatch')
            if result['stopped']:
                raise ValueError('event after assignment stop')
            kind = event['event']
            if result['pending_record'] is not None and kind not in (result['pending_record']['kind'] + '_commit', 'stop'):
                raise ValueError('event during incomplete evidence persistence')
            if result['pending_answer'] is not None and kind not in ('answer_commit', 'stop'):
                raise ValueError('event during incomplete answer submission')
            if kind != 'seal':
                result['execution_elapsed_seconds'] = event['elapsed_seconds']
            if kind in ('attempt', 'display') and event['elapsed_seconds'] >= value['budgets']['assignment_seconds']:
                errors.append('solver event after assignment deadline')
            def get(key):
                ref = event[key]
                raw = read_ref(root, ref)
                used.add(ref['path'])
                return raw
            if kind == 'attempt':
                if result['pending_call'] is not None or event['ordinal'] != result['calls'] + 1:
                    raise ValueError('overlapping/misnumbered attempt')
                get('request')
                result['calls'] += 1
                result['pending_call'] = event['ordinal']
                result['pending_record'] = {'kind': kind, 'sequence': number, 'stop_reason': None}
            elif kind == 'response':
                if result['pending_call'] != event['ordinal']:
                    raise ValueError('response without pending attempt')
                body, receipt = get('response'), json.loads(get('receipt'))
                responses[event['ordinal']] = digest(body)
                if type(receipt.get('body_bytes_observed')) is not int or receipt['body_bytes_observed'] != len(body) or type(receipt.get('transport_complete')) is not bool:
                    raise ValueError('response receipt mismatch')
                result['response_bytes_observed'] += len(body)
                result['transport_complete'] &= receipt['transport_complete']
                result['pending_call'] = None
                result['pending_record'] = {'kind': kind, 'sequence': number, 'stop_reason': event['stop_reason']}
                if not receipt['transport_complete'] and not event['stop_reason']:
                    raise ValueError('incomplete transport must stop assignment')
            elif kind == 'display':
                if result['pending_call'] is not None:
                    raise ValueError('display during pending call')
                display = get('display')
                result['max_display_bytes'] = max(result['max_display_bytes'], len(display))
                if 'scope_policy' in event:
                    decision = json.loads(get('scope_policy'))
                    if (decision.get('schema') != 'native-scope-decision-v1' or
                            scope_policy_sha256 is None or decision.get('policy_sha256') != scope_policy_sha256 or
                            type(decision.get('accepted')) is not bool or
                            type(decision.get('native_dispatched')) is not bool or
                            decision.get('native_response_sha256') != responses.get(decision.get('ordinal')) or
                            decision.get('display_sha256') != digest(display)):
                        raise ValueError('scope policy decision binding mismatch')
                    result.setdefault('scope_policy_decisions', []).append(decision)
                elif frozen.get('source_scope_policy') is not None:
                    raise ValueError('scoped native display missing policy decision')
                result['pending_record'] = {'kind': kind, 'sequence': number, 'stop_reason': None}
            elif kind in ('attempt_commit', 'response_commit', 'display_commit'):
                pending = result['pending_record']
                if (pending is None or kind != pending['kind'] + '_commit' or
                        event['record_sequence'] != pending['sequence']):
                    raise ValueError('evidence completion without matching pending record')
                if pending['stop_reason'] and event['stop_reason'] != pending['stop_reason']:
                    raise ValueError('evidence completion lost stop reason')
                if event['elapsed_seconds'] >= value['budgets']['assignment_seconds'] and not event['stop_reason']:
                    raise ValueError('late evidence completion must stop assignment')
                result['pending_record'] = None
                result['stopped'] = bool(event['stop_reason'])
            elif kind == 'answer':
                if result['pending_call'] is not None or result['pending_answer'] is not None or event['revision'] != len(result['answers']) + 1:
                    raise ValueError('invalid answer revision')
                get('answer')
                result['answers'].append({'revision': event['revision'], 'answer': event['answer'],
                                         'submitted_elapsed_seconds': event['elapsed_seconds'],
                                         'elapsed_seconds': None})
                result['pending_answer'] = event['revision']
            elif kind == 'answer_commit':
                if result['pending_answer'] != event['revision']:
                    raise ValueError('answer completion without pending submission')
                result['answers'][-1]['elapsed_seconds'] = event['elapsed_seconds']
                result['pending_answer'] = None
                result['stopped'] = bool(event['stop_reason'])
            elif kind == 'stop':
                if not isinstance(event['reason'], str) or not event['reason']:
                    raise ValueError('stop reason missing')
                result['stopped'] = True
            elif kind == 'seal':
                if (result['pending_call'] is not None or result['pending_answer'] is not None or
                        result['pending_record'] is not None):
                    raise ValueError('seal during pending operation')
                get('solver_records')
                verification = json.loads(get('verification'))
                if not isinstance(verification, dict) or not isinstance(frozen, dict):
                    raise ValueError('solver verification and freeze must be objects')
                model = frozen.get('model', {})
                result['transcript_verified'] = (
                    verification.get('assignment_sha256') == assignment_ref['sha256'] and
                    verification.get('solver_records') == event['solver_records'] and
                    (verification.get('model_revision') == model.get('revision')
                     if provenance.mode(frozen) == provenance.IMMUTABLE else
                     verification.get('freeze_sha256') == value['freeze']['sha256'] and
                     verification.get('provenance') == provenance.identity_binding(root, frozen, read_ref)) and
                    verification.get('complete') is True and verification.get('access_checked') is True and
                    isinstance(verification.get('reviewer_id'), str) and bool(verification['reviewer_id']) and
                    verification['reviewer_id'] != value['identity']['solver_id'])
                result['stopped'] = True
            else:
                raise ValueError('unknown event type')
            result['event_count'] = number
            result['last_elapsed_seconds'] = event['elapsed_seconds']
            last_hash = digest(raw)
            result['last_event_sha256'] = last_hash
        blobs = {p.relative_to(root).as_posix() for p in (directory / 'blobs').iterdir()}
        local_used = {p for p in used if p.startswith(assignment + '/blobs/')}
        if blobs != local_used:
            raise ValueError('orphaned/unrecorded evidence blob: interrupted mutation')
        budgets = value['budgets']
        if result['calls'] > budgets['calls']:
            errors.append('call budget exceeded')
        if result['response_bytes_observed'] > budgets['response_bytes']:
            errors.append('response budget exceeded')
        if result['max_display_bytes'] > budgets['display_bytes']:
            errors.append('display budget exceeded')
        if result['answers'] and result['answers'][-1]['elapsed_seconds'] is not None and result['answers'][-1]['elapsed_seconds'] >= budgets['assignment_seconds']:
            errors.append('final answer exceeds assignment deadline')
        if result['execution_elapsed_seconds'] >= budgets['assignment_seconds']:
            result['eligibility_blockers'].append('recording exceeded assignment deadline')
        result['identity'] = value['identity']
        result['freeze_sha256'] = value['freeze']['sha256']
        if not provenance.validate(root, frozen, read_ref):
            result['provenance'] = provenance.identity_binding(root, frozen, read_ref)
        result['assignment_sha256'] = assignment_ref['sha256']
    except (ValueError, OSError, KeyError, TypeError) as exc:
        errors.append(str(exc))
    if result['pending_call'] is not None:
        result['transport_complete'] = False
        result['eligibility_blockers'].append('unresolved attempted call; assignment must not resume')
    if result['pending_answer'] is not None:
        result['eligibility_blockers'].append('answer submission completion unknown; assignment must not resume')
    if result['pending_record'] is not None:
        result['eligibility_blockers'].append('evidence persistence completion unknown; assignment must not resume')
    result['response_bytes_is_lower_bound'] = not result['transport_complete'] or bool(errors)
    result['calls_is_lower_bound'] = bool(errors)
    result['assignment_seconds'] = (result['answers'][-1]['elapsed_seconds']
                                    if result['answers'] else (result['execution_elapsed_seconds']
                                                              if result['stopped'] else None))
    if not result['answers']:
        result['eligibility_blockers'].append('no answer submitted')
    if not result['transcript_verified']:
        result['eligibility_blockers'].append('complete independently verified solver records missing')
    if not result['transport_complete']:
        result['eligibility_blockers'].append('incomplete transport')
    result['benchmark_eligible'] = not errors and not result['eligibility_blockers']
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', required=True)
    parser.add_argument('--assignment', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    report = audit(args.root, args.assignment)
    _new_file(Path(args.output), canonical(report) + b'\n')
    return 2 if report['validation_errors'] else 0


if __name__ == '__main__':
    sys.exit(main())
