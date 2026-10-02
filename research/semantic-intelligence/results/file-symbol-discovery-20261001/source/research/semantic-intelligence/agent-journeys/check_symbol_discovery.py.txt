#!/usr/bin/env python3
"""Scripted declaration discovery and reverse lookup; not an agent score."""
import argparse
import json
from pathlib import Path
import client
from check_discovery_workflow import CASES


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--endpoint', required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--catalog', type=Path, required=True)
    args = p.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    reports = []
    for case, (subject, _, token, implementation) in CASES.items():
        prompt = args.output / (case+'.txt')
        prompt.write_text('Discover ' + subject + '.' + token + ' and recorded implementation alternatives.')
        directory = args.output / case
        client.initialize(directory, args.endpoint, prompt, args.catalog)
        identities = set()
        def call(name, arguments):
            raw, accounting = client.request(directory, 'tools/call', {'name': name, 'arguments': arguments})
            assert accounting['response_complete'] and accounting['http_status'] == 200
            wire = json.loads(raw)
            assert 'error' not in wire and not wire['result'].get('isError'), wire
            result = wire['result']['structuredContent']
            assert not result.get('truncated'), result
            if name.startswith('compiler_'):
                identities.add((result['snapshot_id'], result['artifact_sha256']))
            return result
        repo = 'Sample-Outbox'
        repos = call('list_repos', {'filter': repo})
        assert any(r['name'] == repo for r in repos['repos'])
        # Same search parameters as the prior offset-based workflow.
        search = call('search_context', {'repo': repo, 'query': subject, 'top_k': 10, 'token_budget': 6000})
        paths = {b['rel_path'] for b in search['blocks'] if b['rel_path'].endswith('/'+subject+'.cs')}
        assert len(paths) == 1, paths
        path = paths.pop()
        # T: restricts the class case to its type declaration, excluding ctor/members.
        query = token if implementation else 'T:Sample.Components.'+token
        symbols = call('compiler_symbols', {'repo': repo, 'path': path, 'query': query})
        assert symbols['status'] == 'ok' and len(symbols['results']) == 2 and len(symbols['contexts']) == 2, symbols
        evidence = []
        for record in symbols['results']:
            assert record['role'] == 'declaration' and record['binding_status'] == 'resolved'
            assert record['repo'] == repo and record['path'] == path
            evidence.append({'path': path, 'context_id': record['context_id'], 'raw_sha256': record['raw_sha256'], 'raw_byte_offset': record['byte_offset'], 'symbol_id': record['symbol']['id']})
        ids = {r['symbol_id'] for r in evidence}
        assert len(ids) == 1
        if implementation:
            reverse = call('compiler_implementations', {'symbol_id': next(iter(ids))})
            assert reverse['status'] == 'context_required' and len(reverse['contexts']) == 2
            assert {c['context_id'] for c in reverse['contexts']} == {r['context_id'] for r in evidence}
            for context in reverse['contexts']:
                selected = call('compiler_implementations', {'symbol_id': next(iter(ids)), 'context_ids': [context['context_id']]})
                assert selected['status'] == 'ok' and len(selected['matches']) == 1
                assert implementation+'.'+token in selected['matches'][0]['source']['symbol']['descriptor']
        assert len(identities) == 1
        state = json.loads((directory/'state.json').read_text())
        assert not state['stopped']
        report = {'case': case, 'classification': 'scripted_public_declaration_discovery_not_independent_agent_run', 'calls': state['calls'], 'response_bytes': state['response_bytes'], 'discovered_evidence': evidence, 'compiler_identity': list(next(iter(identities))), 'passed': True}
        client.atomic_json(directory/'report.json', report)
        reports.append(report)
        print(case, report['calls'], report['response_bytes'], flush=True)
    client.atomic_json(args.output/'report.json', reports)

if __name__ == '__main__':
    main()
