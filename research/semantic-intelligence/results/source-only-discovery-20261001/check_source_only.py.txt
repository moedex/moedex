#!/usr/bin/env python3
"""Post-pilot native MCP regression, not an independent agent journey."""
import argparse
import hashlib
import json
from pathlib import Path

import client


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--endpoint', required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--catalog', type=Path, required=True)
    parser.add_argument('--prompt', type=Path, required=True)
    args = parser.parse_args()
    client.initialize(args.output, args.endpoint, args.prompt, args.catalog)
    checks = []
    fingerprints = set()

    def call(name, arguments, unavailable=False):
        raw, accounting = client.request(args.output, 'tools/call',
                                         {'name': name, 'arguments': arguments})
        assert accounting['response_complete'], accounting
        assert accounting['http_status'] == 200, accounting
        wire = json.loads(raw)
        assert 'error' not in wire, wire
        result = wire['result']
        content = result.get('structuredContent', {})
        if unavailable:
            assert result.get('isError'), result
            assert content.get('error', {}).get('code') == 'graph_unavailable', result
        else:
            assert not result.get('isError'), result
            snapshot = result['_meta']['dev.moedex/snapshot']
            assert snapshot['corpus_fingerprint'], snapshot
            assert snapshot['cacheable'], snapshot
            fingerprints.add(snapshot['corpus_fingerprint'])
        checks.append({'tool': name, 'expected_graph_unavailable': unavailable,
                       'response_sha256': hashlib.sha256(raw).hexdigest(),
                       'bytes': len(raw)})
        return content

    repos = call('list_repos', {'filter': 'Sample-Outbox'})
    assert repos['repos'] == [{'name': 'Sample-Outbox', 'files': 40}], repos
    source = call('read_source', {'repo': 'Sample-Outbox',
                  'path': 'src/Sample.Worker/Program.cs', 'start_line': 62, 'end_line': 62})
    assert source['start_line'] == source['end_line'] == 62, source
    assert 'AddScoped<IRegistrationValidationService, RegistrationValidationService>()' in source['content'], source
    tree = call('file_tree', {'repo': 'Sample-Outbox', 'prefix': 'src/Sample.Components/Services/'})
    assert tree['files'] == ['src/Sample.Components/Services/IRegistrationValidationService.cs',
                             'src/Sample.Components/Services/RegistrationValidationService.cs'], tree
    symbols = call('list_symbols', {'repo': 'Sample-Outbox', 'query': 'ValidateRegistration'})
    assert symbols['total'] > 0, symbols
    search = call('search_context', {'repo': 'Sample-Outbox', 'query': 'ValidateRegistration', 'top_k': 2})
    assert search['blocks'], search
    for name, arguments in [
        ('graph_schema', {}), ('graph_neighbors', {'symbol': 'RegistrationService'}),
        ('list_clusters', {}), ('impact_analysis', {'symbol': 'RegistrationService'}),
        ('trace_calls', {'symbol': 'RegistrationService'}),
        ('trace_consumers', {'name': 'RegistrationSubmitted'}),
        ('trace_hierarchy', {'symbol': 'RegistrationService'}),
        ('trace_queries', {'symbol': 'RegistrationService'}),
        ('trace_renders', {'symbol': 'RegistrationService'}),
    ]:
        call(name, arguments, unavailable=True)
    assert len(fingerprints) == 1, fingerprints
    state = json.loads((args.output/'state.json').read_text())
    assert not state['stopped'], state
    report = {'classification': 'post_pilot_native_source_only_regression_not_agent_run',
              'passed': True, 'calls': state['calls'], 'response_bytes': state['response_bytes'],
              'corpus_fingerprint': next(iter(fingerprints)), 'checks': checks}
    client.atomic_json(args.output/'report.json', report)
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
