#!/usr/bin/env python3
"""Opt-in source presentation and offline solver requirement/citation preflight.

Only user-request anchors and source presentation envelopes are inputs. No rubric, hidden
corpus, provider, or semantic-support oracle is consulted.
"""
import argparse
from copy import deepcopy
import hashlib
import json
from pathlib import Path
import sys


PRESENTATION = 'single-source-v1'
FALLBACK = 'Validated data is in structuredContent. Source appears once; use a client that exposes structuredContent.'


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False, allow_nan=False).encode()


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def text_lines(text):
    # A final newline terminates its preceding line; it does not add an EOF line.
    parts = text.split('\n')
    return [part + '\n' for part in parts[:-1]] + ([parts[-1]] if parts[-1] else [])


def strict_json(raw):
    def unique(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError('duplicate JSON key: ' + key)
            value[key] = item
        return value
    def invalid(value):
        raise ValueError('non-finite JSON value: ' + value)
    return json.loads(raw, object_pairs_hook=unique, parse_constant=invalid)


def source_records(data):
    """Recognize source-bearing shapes only, never declaration metadata alone."""
    if not isinstance(data, dict):
        return []
    if isinstance(data.get('blocks'), list):
        return [(row, 'text') for row in data['blocks'] if isinstance(row, dict) and isinstance(row.get('text'), str)]
    if isinstance(data.get('content'), str) and 'start_line' in data:
        return [(data, 'content')]
    result = data.get('result')
    if data.get('tool') == 'graph_source' and isinstance(result, dict) and isinstance(result.get('lines'), list):
        return [(result, 'lines')]
    return []


def single_source(response, cap):
    """Deduplicate validated structured replies and clip source at line boundaries.

    Return None when metadata alone will not fit; callers retain their existing
    bounded error/prefix fallback. No selectors or provenance fields are deleted.
    """
    raw = canonical(response)
    visible = deepcopy(response)
    result = visible.get('result')
    if not isinstance(result, dict) or not isinstance(result.get('structuredContent'), dict):
        return None
    result['content'] = [{'type': 'text', 'text': FALLBACK}]
    records = source_records(result['structuredContent'])
    info = {'mode': PRESENTATION, 'validated_envelope_sha256': sha(raw),
            'validated_envelope_bytes': len(raw), 'truncated_display': False,
            'navigation': 'Request a narrower source range or the returned nextCursor; omitted source is unavailable for citations.'}
    result.setdefault('_meta', {})['dev.moedex/display'] = info
    while len(canonical(visible)) > cap:
        candidates = [(len(canonical(row[key])), row, key) for row, key in records if row[key]]
        if not candidates:
            return None
        _, row, key = max(candidates, key=lambda item: item[0])
        parts = list(row[key]) if key == 'lines' else text_lines(row[key])
        original_end = row.get('end_line')
        def keep(count):
            if key == 'lines':
                row[key] = parts[:count]
                row['location']['endLine'] = parts[count - 1]['number'] if count else 0
            else:
                row[key] = ''.join(parts[:count])
                row['end_line'] = min(original_end, row['start_line'] + count - 1) if count else 0
                row['clipped' if key == 'text' else 'truncated'] = True
        info['truncated_display'] = True
        # Remove this record first. If metadata and the other records fit, find
        # its longest complete-line prefix with logarithmic envelope sizing.
        keep(0)
        if len(canonical(visible)) <= cap:
            lo, hi = 0, len(parts) - 1
            while lo < hi:
                middle = (lo + hi + 1) // 2
                keep(middle)
                if len(canonical(visible)) <= cap:
                    lo = middle
                else:
                    hi = middle - 1
            keep(lo)
    return visible


def prepare(request, inventory):
    if not isinstance(request, str) or not request or not isinstance(inventory, list) or not inventory:
        raise ValueError('nonempty user request and inventory required')
    seen = set()
    for row in inventory:
        if (not isinstance(row, dict) or set(row) != {'id', 'start', 'end', 'quote', 'kind'} or
                not isinstance(row['id'], str) or not row['id'] or row['id'] in seen or
                row['kind'] not in ('requirement', 'terminal_path') or
                type(row['start']) is not int or type(row['end']) is not int or
                not 0 <= row['start'] < row['end'] <= len(request) or
                request[row['start']:row['end']] != row['quote']):
            raise ValueError('inventory must have unique IDs and exact user-request character spans')
        seen.add(row['id'])
    return {'schema': 'solver-workflow-v1', 'request': request,
            'request_sha256': sha(request.encode()), 'inventory': inventory,
            'instructions': (
                'Answer the user request. Track every inventory item, including terminal paths explicitly requested. '
                'Before finalizing, inspect success, rejection, cancellation, and failure outcomes relevant to each item; '
                'record discovered terminal paths separately and mark unavailable evidence unresolved. '
                'Return a JSON object with claims and requirements. Each claim has id, text, source_role '
                '(interface, implementation, or other), and citations or unresolved_reason. Each citation has '
                'display_sha256, repo, path, blob_sha, start_line, end_line, and source_role. '
                'Each requirement has id, status (addressed or unresolved), claim_ids, terminal_paths, and reason '
                'when unresolved. Each terminal path has outcome, status, claim_ids, and reason when unresolved. '
                'For an ordinary requirement with no relevant flow (such as a literal constant), empty terminal_paths '
                'may accompany terminal_path_review={status:not_applicable,reason:...}; explicitly requested terminal '
                'path items cannot use not_applicable. '
                'Use only displayed complete source lines. Keep interface declarations and implementation bodies '
                'in separate claims. Check every claim citation against its own file/blob and narrow shown range. '
                'Source presence and this mechanical check do not establish semantic correctness. '
                'Review the inventory against the full user request; anchored items may still omit a requested obligation.')}


def shown_sources(response):
    result = response.get('result', {})
    if not isinstance(result, dict) or result.get('isError') or 'error' in response:
        return []
    sources = []
    for row, key in source_records(result.get('structuredContent')):
        if key == 'lines':
            loc = row.get('location', {})
            if not isinstance(loc, dict):
                continue
            identity = (loc.get('project'), loc.get('path'), loc.get('blobSha'))
            numbers = [line.get('number') for line in row[key] if isinstance(line, dict) and
                       isinstance(line.get('text'), str) and '\n' not in line['text']]
            total = row.get('totalLines')
            if (len(numbers) != len(row[key]) or any(type(n) is not int or n < 1 for n in numbers) or
                    numbers != sorted(set(numbers)) or type(total) is not int or any(n > total for n in numbers)):
                continue
        else:
            identity = (row.get('repo'), row.get('rel_path', row.get('path')), row.get('blob_sha'))
            start, end = row.get('start_line'), row.get('end_line')
            if type(start) is not int or type(end) is not int or start < 1:
                continue
            parts = text_lines(row[key])
            # A clipped partial final line is not evidence for that whole line.
            if row.get('clipped') and parts and not parts[-1].endswith('\n'):
                parts.pop()
            numbers = list(range(start, min(end + 1, start + len(parts))))
            total = row.get('lines')
            if type(total) is int:
                numbers = [n for n in numbers if n <= total]
        if all(isinstance(value, str) and value for value in identity):
            sources.append((identity, set(numbers)))
    return sources


def check(plan, answer, displays):
    """Report declared completeness and mechanical citation validity separately."""
    expected = prepare(plan['request'], plan['inventory'])
    if plan != expected:
        raise ValueError('plan bytes/anchors or generated instructions differ')
    errors, unresolved, claims = [], [], {}
    if not isinstance(answer, dict):
        raise ValueError('answer must be an object')
    evidence = {}
    for raw in displays:
        try:
            response = strict_json(raw)
            if not isinstance(response, dict):
                raise ValueError('display must be an object')
            evidence[sha(raw)] = shown_sources(response)
        except (ValueError, TypeError, KeyError):
            errors.append('invalid display JSON or source shape')
    rows = answer.get('claims', [])
    if not isinstance(rows, list):
        raise ValueError('claims must be a list')
    for claim in rows:
        if not isinstance(claim, dict):
            errors.append('claim must be an object')
            continue
        cid = claim.get('id')
        if not isinstance(cid, str) or not cid or cid in claims:
            errors.append('duplicate or missing claim ID')
            continue
        claims[cid] = claim
        role = claim.get('source_role')
        if role not in ('interface', 'implementation', 'other') or not isinstance(claim.get('text'), str) or not claim['text'].strip():
            errors.append(f'{cid}: missing text or source_role')
        citations = claim.get('citations', [])
        if not isinstance(citations, list):
            errors.append(f'{cid}: citations must be a list')
            continue
        if not citations:
            if not isinstance(claim.get('unresolved_reason'), str) or not claim['unresolved_reason'].strip():
                errors.append(f'{cid}: citations or unresolved_reason required')
            else:
                unresolved.append(cid)
        for cite in citations:
            if not isinstance(cite, dict):
                errors.append(f'{cid}: citation must be an object')
                continue
            start, end = cite.get('start_line'), cite.get('end_line')
            identity = (cite.get('repo'), cite.get('path'), cite.get('blob_sha'))
            valid = (type(start) is int and type(end) is int and 1 <= start <= end and
                     isinstance(cite.get('display_sha256'), str) and cite.get('source_role') == role and any(identity == source and
                     end - start + 1 <= len(numbers) and all(n in numbers for n in range(start, end + 1))
                     for source, numbers in evidence.get(cite.get('display_sha256'), [])))
            if not valid:
                errors.append(f'{cid}: citation identity, role or displayed range invalid')
    inventory = {row['id']: row['kind'] for row in plan['inventory']}
    seen = set()
    def status(row, label):
        refs = row.get('claim_ids', [])
        if not isinstance(refs, list) or any(not isinstance(ref, str) or ref not in claims for ref in refs):
            errors.append(f'{label}: unknown claim reference')
            return
        if row.get('status') == 'addressed':
            if not refs or any(ref in unresolved for ref in refs):
                errors.append(f'{label}: addressed item needs resolved cited claims')
        elif row.get('status') == 'unresolved':
            if not isinstance(row.get('reason'), str) or not row['reason'].strip():
                errors.append(f'{label}: unresolved reason required')
            unresolved.append(label)
        else:
            errors.append(f'{label}: invalid status')
    requirements = answer.get('requirements', [])
    if not isinstance(requirements, list):
        raise ValueError('requirements must be a list')
    for row in requirements:
        if not isinstance(row, dict):
            errors.append('requirement must be an object')
            continue
        rid = row.get('id')
        if not isinstance(rid, str) or rid not in inventory or rid in seen:
            errors.append('unknown or duplicate requirement ID')
            continue
        seen.add(rid)
        status(row, rid)
        paths = row.get('terminal_paths', [])
        review = row.get('terminal_path_review', {})
        not_applicable = (isinstance(review, dict) and review.get('status') == 'not_applicable' and
                          isinstance(review.get('reason'), str) and bool(review['reason'].strip()) and
                          inventory[rid] == 'requirement')
        if 'terminal_path_review' in row and (not not_applicable or paths):
            errors.append(f'{rid}: invalid not-applicable terminal path review')
        if not isinstance(paths, list) or (row.get('status') == 'addressed' and not paths and not not_applicable):
            errors.append(f'{rid}: terminal path review required (use unresolved when unavailable)')
            continue
        for path in paths:
            if not isinstance(path, dict):
                errors.append(f'{rid}: terminal path must be an object')
                continue
            if not isinstance(path.get('outcome'), str) or not path['outcome'].strip():
                errors.append(f'{rid}: terminal path outcome required')
            status(path, f'{rid}/{path.get("outcome", "?")}')
    errors.extend(f'{rid}: missing requirement disposition' for rid in sorted(set(inventory) - seen))
    return {'schema': 'solver-workflow-check-v1', 'mechanically_valid': not errors,
            'declared_complete': not errors and not unresolved, 'unresolved': unresolved, 'errors': errors,
            'semantic_correctness': 'not assessed', 'inventory_completeness': 'requires user/solver review',
            'source_visibility': 'provided presentation envelopes; client delivery/rendering/consumption not assessed'}


def recorded_displays(root, assignment):
    """Consume audited recorded presentation references, never raw responses.

    A display event precedes relay completion; recording alone cannot establish
    client delivery, structured-channel rendering, or model consumption.
    """
    import run_record
    root = Path(root).resolve()
    report = run_record.audit(root, assignment)
    if report['validation_errors']:
        raise ValueError('invalid assignment recording: ' + '; '.join(report['validation_errors']))
    displays = []
    for path in sorted((root / assignment / 'events').glob('*.json')):
        event = strict_json(path.read_bytes())
        if event['event'] == 'display':
            displays.append(run_record.read_ref(root, event['display']))
    return displays


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    prep = sub.add_parser('prepare')
    prep.add_argument('--request', type=Path, required=True)
    prep.add_argument('--inventory', type=Path, required=True)
    verify = sub.add_parser('check')
    verify.add_argument('--plan', type=Path, required=True)
    verify.add_argument('--answer', type=Path, required=True)
    inputs = verify.add_mutually_exclusive_group(required=True)
    inputs.add_argument('--display', type=Path, action='append')
    inputs.add_argument('--record-root', type=Path)
    verify.add_argument('--assignment')
    args = parser.parse_args()
    try:
        if args.command == 'prepare':
            value = prepare(args.request.read_text(), strict_json(args.inventory.read_bytes()))
        else:
            if bool(args.record_root) != bool(args.assignment):
                raise ValueError('--record-root and --assignment must be used together')
            displays = (recorded_displays(args.record_root, args.assignment) if args.record_root else
                        [path.read_bytes() for path in args.display])
            value = check(strict_json(args.plan.read_bytes()), strict_json(args.answer.read_bytes()),
                          displays)
        sys.stdout.buffer.write(canonical(value) + b'\n')
        return 1 if args.command == 'check' and not value['mechanically_valid'] else 0
    except (ValueError, TypeError, KeyError, AttributeError, OSError) as exc:
        parser.error(str(exc))


if __name__ == '__main__':
    sys.exit(main())
