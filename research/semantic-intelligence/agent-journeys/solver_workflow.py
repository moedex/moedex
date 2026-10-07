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
REFERENCE_MODE = 'broker-ordinal-v1'
LEDGER_MODE = 'explicit-gaps-v2'
REFERENCE_KEY = 'dev.moedex/presentation-reference'
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


def presentation_reference(ordinal):
    if type(ordinal) is not int or ordinal < 1:
        raise ValueError('positive native ordinal required for presentation reference')
    return {'mode': REFERENCE_MODE, 'id': 'display-' + str(ordinal)}


def reference_notice(reference):
    return 'Presentation reference: ' + reference['id'] + '. Cite presentation_id for this excerpt; the offline checker resolves its exact recorded hash.'


def stamp_reference(response, ordinal):
    visible = deepcopy(response)
    result = visible.get('result')
    if not isinstance(result, dict) or result.get('isError') or 'error' in visible:
        raise ValueError('presentation references require a successful scope-accepted reply')
    reference = presentation_reference(ordinal)
    result.setdefault('_meta', {})[REFERENCE_KEY] = reference
    result.setdefault('content', []).append({'type': 'text', 'text': reference_notice(reference)})
    return visible


def reference_from_response(response):
    if not isinstance(response, dict):
        raise ValueError('presentation envelope must be an object')
    result = response.get('result', {})
    if not isinstance(result, dict):
        raise ValueError('invalid presentation result')
    meta = result.get('_meta', {})
    if not isinstance(meta, dict):
        raise ValueError('invalid presentation metadata')
    reference = meta.get(REFERENCE_KEY)
    if reference is None:
        return None
    if (not isinstance(reference, dict) or set(reference) != {'mode', 'id'} or
            reference.get('mode') != REFERENCE_MODE or not isinstance(reference.get('id'), str)):
        raise ValueError('invalid presentation reference')
    number = reference['id'][8:]
    if not number.isascii() or not number.isdecimal() or number.startswith('0') or reference['id'] != 'display-' + number:
        raise ValueError('invalid presentation reference ID')
    return reference


def validate_recorded_reference(raw, decision, mode):
    """Bind reference mode/ordinal to the exact presentation recording."""
    if mode not in (None, REFERENCE_MODE) or decision.get('citation_reference_mode') != mode:
        raise ValueError('citation reference mode differs from freeze')
    expected = presentation_reference(decision.get('ordinal')) if mode and decision.get('accepted') is True else None
    if decision.get('presentation_id') != (expected['id'] if expected else None):
        raise ValueError('citation reference differs from accepted native ordinal')
    if mode is None:
        return
    response = strict_json(raw)
    actual = reference_from_response(response)
    if decision.get('accepted') is False and not ('error' in response or response.get('result', {}).get('isError')):
        raise ValueError('denied scope presentation must retain error signaling')
    if actual == expected:
        return
    # The historical bounded prefix fallback discards parsed source/metadata.
    # Its prefix is never registered as a citeable presentation reference.
    result = response.get('result', {})
    content = result.get('content', [])
    if expected and actual is None and 'structuredContent' not in result and len(content) == 1:
        try:
            payload = strict_json(content[0]['text'])
            if (payload.get('truncated_display') is True and type(payload.get('native_envelope_bytes')) is int and
                    isinstance(payload.get('prefix'), str)):
                return
        except (ValueError, KeyError, TypeError, AttributeError):
            pass
    raise ValueError('recorded presentation reference differs from scope decision')


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
    reference = reference_from_response(visible)
    fallback = FALLBACK + (' ' + reference_notice(reference) if reference else '')
    result['content'] = [{'type': 'text', 'text': fallback}]
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


def prepare(request, inventory, citation_reference_mode=None, ledger_mode=None):
    if ledger_mode not in (None, LEDGER_MODE):
        raise ValueError('unsupported ledger mode')
    if citation_reference_mode not in (None, REFERENCE_MODE):
        raise ValueError('unsupported citation reference mode')
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
    plan = {'schema': 'solver-workflow-v1', 'request': request,
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
    if citation_reference_mode:
        plan['citation_reference_mode'] = citation_reference_mode
        plan['instructions'] = plan['instructions'].replace(
            'display_sha256, repo, path, blob_sha, start_line, end_line, and source_role.',
            'presentation_id copied from a broker notice (or exact display_sha256 when independently known), '
            'repo, path, blob_sha, start_line, end_line, and source_role. '
            'Do not calculate hashes of unknown JSON-RPC/MCP wrappers. The offline checker resolves presentation IDs.')
    if ledger_mode:
        plan['ledger_mode'] = ledger_mode
        plan['instructions'] += (
            ' Ledger mode explicit-gaps-v2: use status=partial with a nonempty reason when an item '
            'has cited findings plus unavailable evidence; link both kinds of claims with claim_ids. '
            'Partial items remain incomplete and grant no citation credit to unavailable claims. '
            'Use unresolved for an entirely unavailable item, and addressed only when every linked '
            'claim has valid citations. Do not label a requirement addressed merely because its '
            'evidence limitation was explained. Static-only evidence cannot establish runtime outcomes. '
            'Use source_role=other and citations=[] with unresolved_reason for an evidence limitation '
            'or process statement that has no source support; never invent source citations for it. '
            'Each citation must repeat the claim source_role exactly. Copy repo, path, blob_sha, '
            'and inclusive complete-line bounds from that same displayed source record. '
            'With nonempty terminal_paths, omit terminal_path_review or use '
            '{"status":"reviewed","reason":"Relevant outcomes inventoried below"}; '
            'not_applicable is allowed only with empty paths for ordinary requirements. '
            'Example requirement with a gap: '
            '{"id":"request-item","status":"partial","reason":"Runtime evidence unavailable",'
            '"claim_ids":["source-fact","runtime-gap"],"terminal_paths":'
            '[{"outcome":"runtime success","status":"unresolved","claim_ids":["runtime-gap"],'
            '"reason":"No execution record supplied"}]}. '
            'Example non-source claim: {"id":"runtime-gap","text":"Runtime success is unverified",'
            '"source_role":"other","citations":[],"unresolved_reason":"No execution record supplied"}. '
            'Before submission check that every claim_ids entry names an existing claim, every '
            'addressed item links only cited claims, and every unresolved or partial item has a reason. '
            'The examples show structure only; substitute actual request IDs and observed evidence.')
    return plan


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
    expected = prepare(plan['request'], plan['inventory'], plan.get('citation_reference_mode'), plan.get('ledger_mode'))
    explicit_gaps = plan.get('ledger_mode') == LEDGER_MODE
    if plan != expected:
        raise ValueError('plan bytes/anchors or generated instructions differ')
    errors, unresolved, claims = [], [], {}
    if not isinstance(answer, dict):
        raise ValueError('answer must be an object')
    evidence, references, ambiguous, resolved = {}, {}, set(), []
    for raw in displays:
        try:
            response = strict_json(raw)
            if not isinstance(response, dict):
                raise ValueError('display must be an object')
            exact_hash = sha(raw)
            evidence[exact_hash] = shown_sources(response)
            reference = reference_from_response(response)
            if reference and not response.get('result', {}).get('isError') and 'error' not in response:
                identity = reference['id']
                if identity in references and references[identity] != exact_hash:
                    ambiguous.add(identity)
                else:
                    references[identity] = exact_hash
        except (ValueError, TypeError, KeyError):
            errors.append('invalid display JSON or source shape')
    for identity in sorted(ambiguous):
        references.pop(identity, None)
        errors.append('ambiguous presentation ID: ' + identity)
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
        for index, cite in enumerate(citations):
            if not isinstance(cite, dict):
                errors.append(f'{cid}: citation must be an object')
                continue
            start, end = cite.get('start_line'), cite.get('end_line')
            identity = (cite.get('repo'), cite.get('path'), cite.get('blob_sha'))
            presentation_id = cite.get('presentation_id')
            exact_hash = cite.get('display_sha256')
            locator_valid = True
            if 'presentation_id' in cite:
                mapped = references.get(presentation_id) if isinstance(presentation_id, str) else None
                locator_valid = mapped is not None and ('display_sha256' not in cite or exact_hash == mapped)
                exact_hash = mapped
            valid = (type(start) is int and type(end) is int and 1 <= start <= end and
                     locator_valid and isinstance(exact_hash, str) and cite.get('source_role') == role and any(identity == source and
                     end - start + 1 <= len(numbers) and all(n in numbers for n in range(start, end + 1))
                     for source, numbers in evidence.get(exact_hash, [])))
            if not valid:
                errors.append(f'{cid}: citation identity, role or displayed range invalid')
            else:
                resolved.append({'claim_id': cid, 'citation_index': index, 'display_sha256': exact_hash})
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
        elif row.get('status') == 'unresolved' or (explicit_gaps and row.get('status') == 'partial'):
            if not isinstance(row.get('reason'), str) or not row['reason'].strip():
                errors.append(f'{label}: {row.get("status") if explicit_gaps else "unresolved"} reason required')
            if row.get('status') == 'partial' and not any(
                    ref not in unresolved and claims[ref].get('citations') for ref in refs):
                errors.append(f'{label}: partial item needs a cited finding (use unresolved when unavailable)')
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
        reviewed = (explicit_gaps and isinstance(review, dict) and review.get('status') == 'reviewed' and
                    isinstance(review.get('reason'), str) and bool(review['reason'].strip()) and
                    isinstance(paths, list) and bool(paths))
        if 'terminal_path_review' in row and not (not_applicable and not paths) and not reviewed:
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
            'presentation_hashes': references, 'resolved_citations': resolved,
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
    prep.add_argument('--citation-reference-mode', choices=[REFERENCE_MODE])
    prep.add_argument('--ledger-mode', choices=[LEDGER_MODE])
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
            value = prepare(args.request.read_text(), strict_json(args.inventory.read_bytes()), args.citation_reference_mode, args.ledger_mode)
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
