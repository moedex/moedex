#!/usr/bin/env python3
"""Measure frozen source anchors over public MCP without relabelling misses.

This is a source-authored development baseline, not an independent solver score.
"""
import argparse
import hashlib
import json
from pathlib import Path
import client

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--endpoint', required=True)
p.add_argument('--gold', type=Path, required=True)
p.add_argument('--output', type=Path, required=True)
a = p.parse_args()
gold = json.loads(a.gold.read_text())
a.output.mkdir(parents=True, exist_ok=False)
rows = []
calls = []
identities = set()

def call(name, args):
    request = {'jsonrpc': '2.0', 'id': len(calls)+1, 'method': 'tools/call', 'params': {'name': name, 'arguments': args}}
    raw, status, _ = client.http_exchange(a.endpoint, json.dumps(request).encode(), 60)
    num = len(calls)+1
    (a.output/f'{num:03d}-request.json').write_text(json.dumps(request, indent=2)+'\n')
    (a.output/f'{num:03d}-response.json').write_bytes(raw)
    calls.append({'tool': name, 'http_status': status, 'bytes': len(raw)})
    assert status == 200, (status, raw)
    wire = json.loads(raw)
    assert 'error' not in wire and not wire['result'].get('isError'), wire
    data = wire['result']['structuredContent']
    assert not data.get('truncated'), data
    identities.add((data['snapshot_id'], data['artifact_sha256']))
    return data

for expectation in gold['expectations']:
    args = {'repo': gold['repo'], 'path': expectation['path'], 'byte_offset': expectation['byte_offset'], 'raw_sha256': expectation['raw_sha256']}
    discovery = call('compiler_binding_at', args)
    contexts = discovery['contexts']
    assert contexts, ('missing source context', expectation)
    results = []
    for context in contexts:
        selected = call('compiler_binding_at', dict(args, context_id=context['context_id']))
        results.extend(selected['results'])
    exact = [r for r in results if r['byte_offset'] == expectation['byte_offset'] and r['byte_length'] == expectation['byte_length'] and r['raw_sha256'] == expectation['raw_sha256']]
    resolved = [r for r in exact if r['binding_status'] == 'resolved']
    assert resolved, ('missing resolved anchor', expectation, results)
    layer = expectation['layer']
    facts = [fact for r in resolved for fact in r.get('domain_facts' if layer == 'domain' else 'implementation_facts', [])] if layer != 'binding' else []
    # Presence is an observation, not semantic correctness: retain all descriptors
    # and facts for source review before acceptance classification.
    rows.append({'id': expectation['id'], 'claim': expectation['claim'], 'layer': layer, 'binding_resolved': True,
                 'fact_present': bool(facts) if layer != 'binding' else None, 'facts': facts, 'bindings': resolved})
    print(expectation['id'], 'resolved', len(facts), flush=True)
assert len(identities) == 1, identities
report = {'classification': 'source_authored_development_baseline_not_independent_solver_score', 'gold_sha256': hashlib.sha256(a.gold.read_bytes()).hexdigest(),
          'repo': gold['repo'], 'commit': gold['commit'], 'identity': list(next(iter(identities))), 'calls': calls,
          'response_bytes': sum(c['bytes'] for c in calls), 'expectations': rows}
(a.output/'report.json').write_text(json.dumps(report, indent=2)+'\n')
