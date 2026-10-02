#!/usr/bin/env python3
"""Check frozen selected CodeGraph labels against raw worker capture.

This is a binding-case acceptance gate, not a whole-project quality estimate.
The source project is never rewritten or executed by this evaluator.
"""
import argparse
import base64
import hashlib
import json
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-root', type=Path, required=True)
    inputs = parser.add_mutually_exclusive_group(required=True)
    inputs.add_argument('--wire', type=Path)
    inputs.add_argument('--artifact', type=Path)
    parser.add_argument('--gold', type=Path, default=Path(__file__).with_name('codegraph-abstractions-gold.json'))
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    gold_bytes = args.gold.read_bytes()
    gold = json.loads(gold_bytes)
    if gold.get('schema') not in ('moedex.real-project-acceptance.v1', 'moedex.roslyn-compiler-acceptance.v1'):
        raise ValueError('unsupported gold schema')
    source_files = gold['inputs'] if gold['schema'] == 'moedex.roslyn-compiler-acceptance.v1' else gold['source_files']
    if not source_files or not gold['labels']:
        raise ValueError('gold must contain source inputs and labels')
    for source in source_files:
        raw = (args.source_root / source['path']).read_bytes()
        if hashlib.sha256(raw).hexdigest() != source['sha256']:
            raise ValueError('original source drift: ' + source['path'])
    evidence_path = args.wire or args.artifact
    if args.artifact:
        envelope = json.loads(args.artifact.read_bytes())
        payload = base64.b64decode(envelope['payload'], validate=True)
        if envelope['format'] != 'moedex.semantic' or envelope['version'] != 1 or hashlib.sha256(payload).hexdigest() != envelope['sha256']:
            raise ValueError('invalid semantic artifact envelope')
        artifact = json.loads(payload)
        contexts = {c['id']: c for c in artifact['contexts']}
        sources = {s['id']: s for s in artifact['sources']}
        symbols = {s['id']: s['key'] for s in artifact['symbols']}
        occurrences = {o['id']: o for o in artifact['occurrences']}
        raw_sources = {sid: base64.b64decode(s['content']) if s.get('content') else (args.source_root / s['path']).read_bytes() for sid, s in sources.items()}
        records = [{'record_type': 'project', 'project': c['project'], 'build_context': c['id']} for c in contexts.values()]
        for binding in artifact['bindings']:
            occurrence = occurrences[binding['occurrence_id']]
            source = sources[occurrence['source_id']]
            offset, length = occurrence['offset'], occurrence['length']
            records.append({'record_type': occurrence['role'], 'build_context': occurrence['context_id'],
                            'source_path': source['path'], 'source_sha256': source['raw_sha256'],
                            'source_text': raw_sources[source['id']][offset:offset + length].decode('utf-8'),
                            'span': {'byte_offset': offset, 'byte_length': length}, 'reference_kind': occurrence['kind'],
                            'binding_status': binding['status'], 'symbol': symbols.get(binding.get('symbol_id'))})
        records.append({'record_type': 'stream_summary', 'projects': len(contexts),
                        'compilation_status': 'complete' if all(c['status'] == 'complete' for c in contexts.values()) else 'incomplete',
                        'declarations': sum(o['role'] == 'declaration' for o in occurrences.values()),
                        'references': sum(o['role'] == 'reference' for o in occurrences.values())})
    else:
        records = [json.loads(line) for line in args.wire.read_text().splitlines() if line.strip()]
    terminal = records[-1] if records else {}
    if terminal.get('record_type') != 'stream_summary' or terminal.get('compilation_status') != 'complete':
        raise ValueError('worker capture has no complete terminal summary')
    projects = [r for r in records if r.get('record_type') == 'project']
    if len(projects) != 1 or projects[0]['project'] != gold['project']:
        raise ValueError('unexpected project coverage')
    context = projects[0]['build_context']
    outcomes = []
    for label in gold['labels']:
        raw = (args.source_root / label['path']).read_bytes()
        start, length = label['byte_offset'], label['byte_length']
        if raw[start:start + length].decode('utf-8') != label['token']:
            raise ValueError('gold span drift: ' + label['id'])
        matched = [r for r in records if r.get('record_type') == 'reference'
                   and r.get('build_context') == context
                   and r.get('source_path') == label['path']
                   and r.get('span') == {'byte_offset': start, 'byte_length': length}
                   and r.get('reference_kind') == label['reference_kind']]
        valid = [r for r in matched if r.get('binding_status') == 'resolved'
                 and r.get('source_sha256') == label['source_sha256']
                 and r.get('source_text') == label['token']
                 and (r.get('symbol') or {}).get('descriptor') == label['expected_descriptor']]
        binding_passed = len(matched) == 1 and len(valid) == 1
        passed = binding_passed
        declarations = []
        if passed and label['definition_expected']:
            symbol = valid[0]['symbol']
            key = ('language', 'namespace_kind', 'namespace', 'descriptor', 'descriptor_kind')
            declarations = [r for r in records if r.get('record_type') == 'declaration'
                            and r.get('build_context') == context
                            and all((r.get('symbol') or {}).get(k) == symbol.get(k) for k in key)]
            passed = bool(declarations)
        outcomes.append({'id': label['id'], 'passed': passed, 'binding_passed': binding_passed,
                         'expected_descriptor': label['expected_descriptor'],
                         'actual': [{'status': r.get('binding_status'), 'symbol': r.get('symbol')} for r in matched],
                         'matching_declaration_count': len(declarations)})
    result = {'schema': 'moedex.real-project-acceptance-results.v1',
              'gold_sha256': hashlib.sha256(gold_bytes).hexdigest(),
              'evidence_sha256': hashlib.sha256(evidence_path.read_bytes()).hexdigest(),
              'evidence_format': 'artifact' if args.artifact else 'wire',
              'scope': gold['scope'], 'project': gold['project'], 'context': context,
              'summary': terminal, 'passed': sum(x['passed'] for x in outcomes),
              'total': len(outcomes), 'bindings_passed': sum(x['binding_passed'] for x in outcomes),
              'outcomes': outcomes}
    encoded = json.dumps(result, indent=2) + '\n'
    if args.output:
        args.output.write_text(encoded)
    print(encoded, end='')
    return 0 if all(x['passed'] for x in outcomes) else 1


if __name__ == '__main__':
    raise SystemExit(main())
