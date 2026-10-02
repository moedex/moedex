#!/usr/bin/env python3
"""Compare compiler observations at source-authored anchors, without guessing IDs.

Experimental harness, not the production semantic import contract. Adapter-specific
IDs are compared only within each adapter; resolved references must equal the ID
at their independently labeled declaration site.
"""
import argparse
import json
from pathlib import Path


def anchor_span(root, anchor):
    raw = (root / anchor['file']).read_bytes()
    lines = raw.splitlines(keepends=True)
    line = lines[anchor['line'] - 1]
    token = anchor['text'].encode()
    if line.count(token) != 1:
        raise ValueError(f'anchor must occur exactly once: {anchor}')
    start = sum(map(len, lines[:anchor['line'] - 1])) + line.index(token)
    return start, len(token)


def scip_observations(root, directory, projects):
    result = {}
    for project in projects:
        source = directory / (project['id'] + '.json')
        if not source.exists():
            result[project['id']] = []
            continue
        data = json.loads(source.read_text())
        observations = []
        for document in data.get('documents', []):
            name = document.get('relative_path', document.get('relativePath', ''))
            file = root / name
            if not file.is_file():
                continue
            lines = file.read_bytes().splitlines(keepends=True)
            for occurrence in document.get('occurrences', []):
                span = occurrence.get('range', [])
                if len(span) not in (3, 4):
                    continue
                start_line, start_col = span[:2]
                end_line, end_col = (start_line, span[2]) if len(span) == 3 else span[2:]
                # scip-dotnet uses Roslyn UTF-16 source columns. Convert using
                # exact original bytes, never assume columns equal UTF-8 bytes.
                def offset(line, col):
                    if line < 0 or line >= len(lines):
                        raise ValueError('SCIP range line outside source')
                    text = lines[line].decode('utf-8')
                    encoded = text.encode('utf-16-le')
                    if col < 0 or col * 2 > len(encoded):
                        raise ValueError('SCIP range column outside source')
                    prefix = encoded[:col * 2].decode('utf-16-le')
                    return sum(map(len, lines[:line])) + len(prefix.encode('utf-8'))
                start, end = offset(start_line, start_col), offset(end_line, end_col)
                if end < start:
                    raise ValueError('SCIP range ends before start')
                roles = occurrence.get('symbol_roles', occurrence.get('symbolRoles', 0))
                symbol = occurrence.get('symbol', '')
                observations.append({'file': name, 'byte_offset': start,
                    'byte_length': end-start, 'kind': 'declaration' if roles & 1 else 'reference',
                    'symbol': symbol, 'status': 'resolved' if symbol else 'unresolved'})
        result[project['id']] = observations
    return result


def roslyn_observations(directory, projects):
    result = {}
    for project in projects:
        rows = []
        for line in (directory/(project['id']+'.jsonl')).read_text().splitlines():
            row = json.loads(line)
            if row.get('project') != project['project_file'] or row.get('record_type') not in ('declaration','reference'):
                continue
            symbol = row.get('symbol') or {}
            rows.append({'file':row['source_path'], 'byte_offset':row['span']['byte_offset'],
                'byte_length':row['span']['byte_length'], 'kind':row['record_type'],
                'symbol':symbol.get('id',''), 'descriptor':symbol.get('descriptor',''),
                'status':row['binding_status'],
                'candidate_symbols':[c['id'] for c in row.get('candidates',[]) if c]})
        result[project['id']] = rows
    return result


def find(observations, project, root, anchor, kind):
    start, length = anchor_span(root, anchor)
    return [o for o in observations.get(project, []) if o['file'] == anchor['file']
            and o['byte_offset'] == start and o['byte_length'] == length and o['kind'] == kind]


def evaluate(root, gold, observations):
    results = []
    def symbols(project, anchor):
        return {o['symbol'] for o in find(observations, project, root, anchor, 'declaration') if o.get('symbol')}
    for label in gold['references']:
        found = find(observations, label['project'], root, label['source'], 'reference')
        actual = {o.get('symbol') for o in found if o.get('symbol')}
        status = label['status']
        if status == 'resolved':
            expected = symbols(label['target']['project'], label['target']['anchor'])
            passed = len(expected) == 1 and actual == expected and all(o.get('status') == 'resolved' for o in found)
        elif status == 'inactive':
            expected = set()
            passed = not found and any(o['file'] == label['source']['file'] for o in observations.get(label['project'], []))
        elif status in ('unresolved', 'ambiguous'):
            expected = set()
            passed = any(o.get('status') == status for o in found) and not actual
            if status == 'ambiguous' and passed:
                expected = set().union(*(symbols(t['project'], t['anchor']) for t in label['candidate_targets']))
                candidates = set().union(*(set(o.get('candidate_symbols', [])) for o in found))
                passed = len(expected) == len(label['candidate_targets']) and candidates == expected
        else:  # External descriptors are adapter-specific; don't infer equivalence.
            expected = {label['external_symbol']}
            passed = any(o.get('descriptor') == label['external_symbol'] and o.get('status') == 'resolved' and o.get('symbol') for o in found)
        outcome = 'matched' if passed else 'not_matched'
        if status == 'external' and found and not any(o.get('descriptor') for o in found):
            outcome = 'descriptor_mapping_unsupported'
        results.append({'id': label['id'], 'pass': passed, 'outcome':outcome, 'expected_status':status,
                        'observed_symbols':sorted(actual), 'expected_symbols':sorted(expected),
                        'observations':found})
    identities = []
    for group in gold['identity_groups']:
        ids = [symbols(t['project'], t['anchor']) for t in group['declarations']]
        complete = all(len(s) == 1 for s in ids)
        distinct = len(set().union(*ids))
        passed = complete and (distinct == 1 if group['relation'] == 'same_symbol' else distinct == len(ids))
        identities.append({'id':group['id'], 'pass':passed, 'symbols':[sorted(s) for s in ids]})
    return {'references':results, 'identities':identities,
            'unscored_contracts':['project_completeness','expected_diagnostics','generated_input_coverage','context_digest_changes'],
            'reference_pass':sum(r['pass'] for r in results), 'reference_total':len(results),
            'identity_pass':sum(r['pass'] for r in identities), 'identity_total':len(identities)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--fixture', type=Path, required=True)
    parser.add_argument('--scip', type=Path)
    parser.add_argument('--roslyn', type=Path)
    parser.add_argument('--observations', type=Path, help='Normalized per-project observations JSON')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    gold = json.loads((args.fixture/'gold.json').read_text())
    if args.scip:
        observations = scip_observations(args.fixture,args.scip,gold['projects'])
    elif args.roslyn:
        observations = roslyn_observations(args.roslyn,gold['projects'])
    else:
        observations = json.loads(args.observations.read_text())
    result = evaluate(args.fixture,gold,observations)
    args.output.write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps({k:v for k,v in result.items() if k not in ('references','identities')}))

if __name__ == '__main__':
    main()
