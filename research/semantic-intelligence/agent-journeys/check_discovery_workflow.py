#!/usr/bin/env python3
"""Scripted source-to-compiler regression; no independent agent quality score."""
import argparse
import hashlib
import json
from pathlib import Path

import client

CASES = {
    'validation': ('IRegistrationValidationService', 'ValidateRegistration(', 'ValidateRegistration', 'RegistrationValidationService'),
    'submission': ('IRegistrationService', 'SubmitRegistration(', 'SubmitRegistration', 'RegistrationService'),
    'bom': ('RegistrationDbContext', 'class RegistrationDbContext', 'RegistrationDbContext', None),
}


def raw_position(indexed_text, raw_sha, anchor, token):
    """Accept only a complete byte representation proven by the compiler hash.

    Ingest strips one leading UTF8 BOM, but compiler offsets include it. Do not
    assume absence/presence, normalize newlines, or read the live filesystem.
    """
    text = indexed_text.encode('utf-8')
    needle = anchor.encode('utf-8')
    if text.count(needle) != 1 or anchor.count(token) != 1:
        raise ValueError('token anchor must be unique')
    offset = text.index(needle) + anchor.encode('utf-8').index(token.encode('utf-8'))
    for prefix in (b'', b'\xef\xbb\xbf'):
        if hashlib.sha256(prefix + text).hexdigest() == raw_sha:
            return offset + len(prefix), len(prefix)
    raise ValueError('indexed source does not match recorded raw hash with or without a UTF8 BOM')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--case', choices=CASES, required=True)
    parser.add_argument('--endpoint', required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--catalog', type=Path, required=True)
    parser.add_argument('--prompt', type=Path, required=True)
    args = parser.parse_args()
    client.initialize(args.output, args.endpoint, args.prompt, args.catalog)
    evidence = []

    def call(name, arguments):
        raw, accounting = client.request(args.output, 'tools/call', {'name': name, 'arguments': arguments})
        assert accounting['response_complete'] and accounting['http_status'] == 200, accounting
        wire = json.loads(raw)
        assert 'error' not in wire and not wire['result'].get('isError'), wire
        result = wire['result']['structuredContent']
        assert not result.get('truncated'), result
        return result

    repo = 'Sample-Outbox'
    subject, anchor, token, implementation = CASES[args.case]
    repos = call('list_repos', {'filter': repo})
    assert any(r['name'] == repo for r in repos['repos']), repos
    search = call('search_context', {'repo': repo, 'query': subject, 'top_k': 10, 'token_budget': 6000})
    paths = {b['rel_path'] for b in search['blocks'] if b['rel_path'].endswith('/' + subject + '.cs')}
    assert len(paths) == 1, paths
    path = paths.pop()
    source = call('read_source', {'repo': repo, 'path': path})
    assert source['start_line'] == 1 and source['end_line'] == source['lines'], source
    contexts = call('compiler_binding_at', {'repo': repo, 'path': path, 'byte_offset': 0})
    assert contexts['status'] == 'context_required' and len(contexts['contexts']) == 2, contexts
    symbols = set()
    for context in contexts['contexts']:
        offset, bom = raw_position(source['content'], context['raw_sha256'], anchor, token)
        binding = call('compiler_binding_at', {'repo': repo, 'path': path,
                       'byte_offset': offset, 'context_id': context['context_id'], 'raw_sha256': context['raw_sha256']})
        assert binding['status'] == 'ok' and len(binding['results']) == 1, binding
        bound = binding['results'][0]
        assert bound['binding_status'] == 'resolved' and bound['byte_offset'] == offset, bound
        symbol = bound['symbol']['id']
        assert token in bound['symbol']['descriptor'], bound
        symbols.add(symbol)
        definitions = call('compiler_definitions', {'repo': repo, 'symbol_id': symbol, 'context_id': context['context_id']})
        assert definitions['status'] == 'ok' and any(d['path'] == path and d['raw_sha256'] == context['raw_sha256'] for d in definitions['results']), definitions
        evidence.append({'path': path, 'context_id': context['context_id'], 'raw_sha256': context['raw_sha256'], 'raw_byte_offset': offset, 'verified_bom_bytes': bom, 'symbol_id': symbol})
    assert len(symbols) == 1, symbols
    symbol = symbols.pop()
    if implementation:
        reverse = call('compiler_implementations', {'symbol_id': symbol})
        assert reverse['status'] == 'context_required' and len(reverse['contexts']) == 2, reverse
        assert {c['context_id'] for c in reverse['contexts']} == {c['context_id'] for c in contexts['contexts']}, reverse
        for context in reverse['contexts']:
            matches = call('compiler_implementations', {'symbol_id': symbol, 'context_ids': [context['context_id']]})
            assert matches['status'] == 'ok' and len(matches['matches']) == 1, matches
            match = matches['matches'][0]
            fact = match['source']['implementation_facts'][match['implementation_fact_index']]
            assert fact['interface_symbol_id'] == symbol, match
            assert implementation + '.' + token in match['source']['symbol']['descriptor'], match
    else:
        assert all(e['verified_bom_bytes'] == 3 for e in evidence), evidence
    state = json.loads((args.output/'state.json').read_text())
    assert not state['stopped'], state
    report = {'classification': 'scripted_public_source_to_compiler_workflow_not_independent_agent_run',
              'case': args.case, 'passed': True, 'calls': state['calls'], 'response_bytes': state['response_bytes'],
              'inputs': {'repo': repo, 'subject_name': subject, 'member_name': token},
              'discovered_evidence': evidence,
              'limitations': ['Source-authored expected names and algorithm, not autonomous discovery quality.',
                              'Paths, offsets, hashes, context IDs and symbol IDs come only from public responses and hash-verified returned content.',
                              'Compiler relationships are historical static evidence, not runtime dispatch.']}
    client.atomic_json(args.output/'report.json', report)
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
