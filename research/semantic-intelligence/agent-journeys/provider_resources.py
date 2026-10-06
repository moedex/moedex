#!/usr/bin/env python3
"""Opt-in provider request/body ceilings and observed-usage stopping.

Usage is provider-reported, post-response resource accounting, not verified
billing or a hard cumulative token/dollar reservation. Cached input and
reasoning output remain in their reported totals. This module does no networking.
"""
import argparse
from copy import deepcopy
import hashlib
import json
import math
from pathlib import Path
import re


FIELDS = {'schema', 'max_requests', 'max_request_bytes', 'max_response_bytes',
          'max_total_request_bytes', 'max_total_response_bytes',
          'max_observed_input_tokens', 'max_observed_output_tokens', 'max_output_tokens'}
STEM = re.compile(r'^provider-[0-9]{4,}$')
LIMITATION = ('Provider usage is reported after dispatch, not authenticated billing. '
              'Observed token thresholds may be crossed by one in-flight request; '
              'unknown usage is never zero and stops further dispatch. Body ceilings '
              'bound retained decoded bytes with at most one overflow sentinel byte. '
              'No monetary ceiling or pre-dispatch cumulative token guarantee is established.')
INSTRUCTION_PREFIX = 'Provider resource ceilings:'


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def load_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate JSON field')
            result[key] = value
        return result
    def constant(value):
        raise ValueError('nonfinite JSON value')
    return json.loads(raw, object_pairs_hook=pairs, parse_constant=constant)


def validate_policy(policy):
    if type(policy) is not dict or set(policy) != FIELDS or policy['schema'] != 'provider-resource-policy-v1':
        raise ValueError('invalid provider resource policy')
    for key in FIELDS - {'schema'}:
        if type(policy[key]) is not int or policy[key] <= 0:
            raise ValueError('positive integer provider resource limits required')
    if policy['max_output_tokens'] < 16 or max(policy['max_request_bytes'], policy['max_response_bytes']) > 64 << 20:
        raise ValueError('provider limits outside supported transport range')
    return deepcopy(policy)


def prompt_instructions(policy):
    """Render the solver-facing limits from the same typed enforcement policy."""
    p = validate_policy(policy)
    return (f"{INSTRUCTION_PREFIX} {p['max_requests']} requests; "
            f"{p['max_request_bytes']} bytes per request; {p['max_response_bytes']} bytes per response; "
            f"{p['max_total_request_bytes']} cumulative request bytes; "
            f"{p['max_total_response_bytes']} cumulative response bytes; "
            f"observed {p['max_observed_input_tokens']} input tokens and "
            f"{p['max_observed_output_tokens']} output tokens stop further dispatch. "
            f"Fixed per-request max_output_tokens {p['max_output_tokens']}. "
            "Observed usage is reported after response; one in-flight request may cross a threshold. "
            "Unknown usage stops further dispatch. These are not hard cumulative billing quotas.")


def validate_prompt_instructions(prompt, policy):
    if type(prompt) is not str:
        raise ValueError('provider prompt must be text')
    lines = [line for line in prompt.splitlines() if INSTRUCTION_PREFIX in line]
    if lines != [prompt_instructions(policy)]:
        raise ValueError('provider prompt resource instructions differ from frozen policy')


def terminal_usage(body, receipt):
    """One JSON/SSE terminal response; duplicates never double-count a stream."""
    if (receipt.get('transport_complete') is not True or type(receipt.get('status')) is not int or
            not 200 <= receipt['status'] < 300):
        raise ValueError('provider_usage_unknown')
    candidates = []
    content_type = (receipt.get('content_type') or '').split(';', 1)[0].strip().lower()
    if content_type == 'text/event-stream' or body.lstrip().startswith((b'event:', b'data:')):
        data = []
        def append(lines):
            payload = '\n'.join(lines)
            if payload == '[DONE]':
                return
            event = load_json(payload)
            if type(event) is not dict:
                raise ValueError('provider_usage_unknown')
            if event.get('type') in ('response.completed', 'response.incomplete', 'response.failed'):
                response = event.get('response')
                if type(response) is not dict or response.get('status') != event['type'].split('.')[1]:
                    raise ValueError('provider_usage_unknown')
                candidates.append(response)
        for line in body.decode('utf-8').replace('\r\n', '\n').replace('\r', '\n').split('\n'):
            if not line:
                if data:
                    append(data)
                    data = []
            elif line.startswith('data:'):
                data.append(line[5:].lstrip(' '))
        if data:
            append(data)
    else:
        candidates.append(load_json(body))
    if not candidates or any(value != candidates[0] for value in candidates[1:]):
        raise ValueError('provider_usage_unknown')
    response = candidates[0]
    if (type(response) is not dict or type(response.get('id')) is not str or not response['id'] or
            response.get('status') not in ('completed', 'incomplete', 'failed') or type(response.get('usage')) is not dict):
        raise ValueError('provider_usage_unknown')
    usage = response['usage']
    for name in ('input_tokens', 'output_tokens', 'total_tokens'):
        if type(usage.get(name)) is not int or usage[name] < 0:
            raise ValueError('provider_usage_unknown')
    if usage['total_tokens'] != usage['input_tokens'] + usage['output_tokens']:
        raise ValueError('provider_usage_unknown')
    for name, member, total in [('input_tokens_details', 'cached_tokens', 'input_tokens'),
                                ('output_tokens_details', 'reasoning_tokens', 'output_tokens')]:
        if name in usage:
            detail = usage[name]
            if (type(detail) is not dict or type(detail.get(member)) is not int or
                    not 0 <= detail[member] <= usage[total]):
                raise ValueError('provider_usage_unknown')
    incomplete = response.get('incomplete_details')
    return {'response_id': response['id'], 'status': response['status'],
            'input_tokens': usage['input_tokens'], 'output_tokens': usage['output_tokens'],
            'max_output_incomplete': type(incomplete) is dict and incomplete.get('reason') == 'max_output_tokens'}


class ProviderResourceStop(ValueError):
    """Safe terminal reason; callers must retain bytes before usage checks."""


class ProviderResources:
    def __init__(self, policy, emit=None):
        self.policy = validate_policy(policy)
        self.emit = emit
        self.events = []
        self.request_count = self.request_bytes = self.response_bytes = 0
        self.input_tokens = self.output_tokens = 0
        self.usage_complete = True
        self.pending = self.stop_reason = None
        self.stems = set()

    def event(self, value):
        self.events.append(deepcopy(value))
        if self.emit is not None:
            self.emit(deepcopy(value))

    def fail(self, reason, phase, stem=None):
        self.stop_reason = reason
        self.event({'event': 'stop', 'reason': reason, 'phase': phase, 'stem': stem})
        raise ProviderResourceStop(reason)

    def begin(self, stem, raw):
        if self.stop_reason is not None or self.pending is not None:
            raise ProviderResourceStop('provider_resource_state_closed')
        if type(stem) is not str or not STEM.fullmatch(stem) or stem in self.stems or type(raw) is not bytes:
            raise ValueError('invalid provider resource request identity')
        self.stems.add(stem)
        try:
            value = load_json(raw)
            if type(value) is not dict:
                raise ValueError()
            if 'max_output_tokens' in value and (type(value['max_output_tokens']) is not int or
                                               value['max_output_tokens'] != self.policy['max_output_tokens']):
                self.fail('provider_output_setting_mismatch', 'request', stem)
            if 'max_output_tokens' not in value:
                value['max_output_tokens'] = self.policy['max_output_tokens']
                forwarded = canonical(value)
            else:
                forwarded = raw
        except ProviderResourceStop:
            raise
        except (ValueError, UnicodeError, TypeError):
            self.fail('provider_request_invalid', 'request', stem)
        checks = [('provider_request_count_cap', self.request_count >= self.policy['max_requests']),
                  ('provider_request_bytes_cap', max(len(raw), len(forwarded)) > self.policy['max_request_bytes']),
                  ('provider_total_request_bytes_cap', self.request_bytes + len(forwarded) > self.policy['max_total_request_bytes']),
                  ('provider_total_response_bytes_cap', self.response_bytes >= self.policy['max_total_response_bytes']),
                  ('provider_observed_input_cap', self.input_tokens >= self.policy['max_observed_input_tokens']),
                  ('provider_observed_output_cap', self.output_tokens >= self.policy['max_observed_output_tokens'])]
        for reason, exceeded in checks:
            if exceeded:
                self.fail(reason, 'request', stem)
        self.request_count += 1
        self.request_bytes += len(forwarded)
        self.pending = stem
        self.event({'event': 'request_reserved', 'stem': stem, 'original_sha256': digest(raw),
                    'upstream_sha256': digest(forwarded), 'upstream_bytes': len(forwarded)})
        return forwarded

    def transport_cap(self):
        return min(self.policy['max_response_bytes'], self.policy['max_total_response_bytes'] - self.response_bytes)

    def finish(self, stem, body, receipt):
        if self.stop_reason is not None or self.pending != stem or type(body) is not bytes or type(receipt) is not dict:
            raise ValueError('invalid provider resource response state')
        self.pending = None
        self.response_bytes += len(body)
        usage, usage_error = None, None
        try:
            if type(receipt.get('body_bytes_observed')) is not int or receipt['body_bytes_observed'] != len(body):
                raise ValueError('provider_usage_unknown')
            usage = terminal_usage(body, receipt)
        except (ValueError, TypeError, UnicodeError, AttributeError):
            usage_error = 'provider_usage_unknown'
            self.usage_complete = False
        if usage is not None:
            self.input_tokens += usage['input_tokens']
            self.output_tokens += usage['output_tokens']
        self.event({'event': 'response_observed', 'stem': stem, 'response_sha256': digest(body),
                    'response_bytes': len(body), 'receipt_sha256': digest(canonical(receipt)),
                    'usage': usage, 'usage_error': usage_error})
        checks = [('provider_response_bytes_cap', len(body) > self.policy['max_response_bytes']),
                  ('provider_total_response_bytes_cap', self.response_bytes > self.policy['max_total_response_bytes']),
                  ('provider_usage_unknown', usage_error is not None),
                  ('provider_observed_input_cap', self.input_tokens > self.policy['max_observed_input_tokens']),
                  ('provider_observed_output_cap', self.output_tokens > self.policy['max_observed_output_tokens']),
                  ('provider_response_output_limit', usage is not None and
                   (usage['max_output_incomplete'] or usage['output_tokens'] > self.policy['max_output_tokens'])),
                  ('provider_response_not_completed', usage is not None and usage['status'] != 'completed')]
        for reason, exceeded in checks:
            if exceeded:
                self.fail(reason, 'response', stem)

    def abort_pending(self):
        if self.pending is not None and self.stop_reason is None:
            stem, self.pending = self.pending, None
            self.usage_complete = False
            self.stop_reason = 'provider_usage_unknown'
            self.event({'event': 'abort_pending', 'stem': stem, 'reason': self.stop_reason})

    def summary(self):
        return {'schema': 'provider-resource-ledger-v1', 'policy': self.policy,
                'policy_sha256': digest(canonical(self.policy)), 'events': self.events,
                'request_attempts_reserved': self.request_count, 'upstream_request_bytes_reserved': self.request_bytes,
                'response_body_bytes_observed': self.response_bytes,
                'known_input_tokens': self.input_tokens, 'known_output_tokens': self.output_tokens,
                'all_reserved_usage_known': self.usage_complete and self.pending is None,
                'pending': self.pending, 'stop_reason': self.stop_reason, 'limitations': LIMITATION}


def audit_capture(root, expected_policy):
    """Replay exact retained requests/replies, including deliberate failed stops."""
    root = Path(root).resolve()
    def read(name):
        path = root / name
        if path.is_symlink() or not path.is_file() or path.resolve().parent != root:
            raise ValueError('provider audit requires confined regular files')
        return path.read_bytes()
    recorded = load_json(read('provider-resource-ledger.json'))
    policy = validate_policy(expected_policy)
    if load_json(read('provider-resource-policy.json')) != policy:
        raise ValueError('retained provider policy differs from independently frozen policy')
    if type(recorded) is not dict or recorded.get('policy') != policy:
        raise ValueError('provider ledger differs from independently frozen policy')
    tracker = ProviderResources(policy)
    for event in recorded.get('events', []):
        if type(event) is not dict:
            raise ValueError('invalid provider resource event')
        stem = event.get('stem')
        if type(stem) is not str or not STEM.fullmatch(stem):
            raise ValueError('invalid provider ledger identity')
        try:
            if event.get('event') == 'request_reserved':
                forwarded = tracker.begin(stem, read(stem + '.request.json'))
                if forwarded != read(stem + '.upstream-request.json'):
                    raise ValueError('retained upstream request differs from frozen transform')
            elif event.get('event') == 'response_observed':
                tracker.finish(stem, read(stem + '.response.raw'), load_json(read(stem + '.receipt.json')))
            elif event.get('event') == 'stop' and event.get('phase') == 'request':
                tracker.begin(stem, read(stem + '.request.json'))
            elif event.get('event') == 'abort_pending':
                tracker.abort_pending()
            elif event.get('event') != 'stop':
                raise ValueError('unknown provider resource event')
        except ProviderResourceStop:
            pass
    if tracker.summary() != recorded:
        raise ValueError('provider resource ledger differs from independent raw replay')
    # No omitted dispatched request/reply may disappear from the replay ledger.
    stems = {path.name.removesuffix('.request.json') for path in root.glob('provider-*.request.json')
             if not path.name.endswith('.upstream-request.json')}
    if stems != tracker.stems:
        raise ValueError('provider requests missing from resource ledger')
    reserved = {e['stem'] for e in tracker.events if e['event'] == 'request_reserved'}
    upstream = {path.name.removesuffix('.upstream-request.json') for path in root.glob('provider-*.upstream-request.json')}
    responses = {path.name.removesuffix('.response.raw') for path in root.glob('provider-*.response.raw')}
    receipts = {path.name.removesuffix('.receipt.json') for path in root.glob('provider-*.receipt.json')}
    completed = {e['stem'] for e in tracker.events if e['event'] == 'response_observed'}
    if upstream != reserved or responses != completed or receipts != completed:
        raise ValueError('provider retained file roster differs from ledger')
    return {'schema': 'provider-resource-audit-v1', 'valid': True, 'raw_replay_complete': True,
            'ledger_sha256': digest(read('provider-resource-ledger.json')),
            'policy_sha256': tracker.summary()['policy_sha256'], 'summary': tracker.summary(),
            'limits': LIMITATION}


def audit_relay_journal(root):
    """Reconstruct attempted/committed exact host relay bytes without trusting totals.

    An uncommitted write is unknown delivery, never proof of nonrelay. The capture
    inventory and frozen controller must independently establish journal coverage.
    """
    path = Path(root) / 'events.jsonl'
    if path.is_symlink() or not path.is_file():
        raise ValueError('missing regular controller journal')
    replies, pending, last_time = [], None, None
    for raw in path.read_bytes().splitlines():
        event = load_json(raw)
        if type(event) is not dict:
            raise ValueError('invalid controller journal event')
        stamp = event.get('controller_monotonic')
        if type(stamp) not in (int, float) or not math.isfinite(stamp) or stamp < 0 or (last_time is not None and stamp < last_time):
            raise ValueError('controller journal timing mismatch')
        last_time = stamp
        channel = event.get('channel')
        if channel == 'controller_reply':
            if pending is not None or event.get('reply_ordinal') != len(replies) + 1 or type(event.get('reply_ordinal')) is not int:
                raise ValueError('overlapping or misnumbered controller reply')
            message = event.get('message')
            if type(message) is not dict:
                raise ValueError('invalid controller relay message')
            wire = canonical(message) + b'\n'
            if type(event.get('wire_bytes')) is not int or event['wire_bytes'] != len(wire) or event.get('wire_sha256') != digest(wire):
                raise ValueError('controller relay wire binding mismatch')
            pending = {'reply_ordinal': event['reply_ordinal'], 'wire_bytes': len(wire),
                       'wire_sha256': digest(wire), 'message': message, 'committed': False}
            replies.append(pending)
        elif channel == 'controller_reply_commit':
            if (pending is None or any(type(event.get(key)) is not int for key in ('reply_ordinal', 'wire_bytes')) or
                    any(event.get(key) != pending[key] for key in ('reply_ordinal', 'wire_bytes', 'wire_sha256'))):
                raise ValueError('controller relay completion without matching attempt')
            pending['committed'] = True
            pending = None
    return {'schema': 'controller-relay-audit-v1', 'journal_sha256': digest(path.read_bytes()),
            'replies': replies, 'delivery_complete': pending is None,
            'limitations': 'Commit proves host pipe write/flush, not model consumption. An uncommitted write has unknown delivery.'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--capture', required=True)
    parser.add_argument('--policy', required=True, help='Independently frozen exact policy JSON')
    parser.add_argument('--output')
    args = parser.parse_args()
    try:
        value = audit_capture(args.capture, load_json(Path(args.policy).read_bytes()))
        raw = json.dumps(value, sort_keys=True, indent=2, allow_nan=False) + '\n'
        if args.output:
            with Path(args.output).open('x') as stream:
                stream.write(raw)
        else:
            print(raw, end='')
    except (ValueError, OSError, TypeError, KeyError) as error:
        parser.exit(2, 'provider resource audit failed: ' + str(error) + '\n')


if __name__ == '__main__':
    main()
