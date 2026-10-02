#!/usr/bin/env python3
"""Source-authored eShop default-selection walkthrough, not an agent score."""
import argparse
import json
from pathlib import Path
import client
from check_discovery_workflow import raw_position

p = argparse.ArgumentParser()
p.add_argument('--endpoint', required=True)
p.add_argument('--output', type=Path, required=True)
a = p.parse_args()
a.output.mkdir(parents=True, exist_ok=False)
raw, status, _ = client.http_exchange(a.endpoint, json.dumps({'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list', 'params': {}}).encode(), 30)
assert status == 200
(a.output / 'catalog.raw').write_bytes(raw)
catalog = a.output / 'catalog.json'
catalog.write_text(json.dumps(json.loads(raw)['result']['tools'], indent=2))
reports = []
for event in ['OrderStatusChangedToPaidIntegrationEvent', 'OrderStatusChangedToShippedIntegrationEvent', 'ProductPriceChangedIntegrationEvent']:
    handler = event + 'Handler'
    prompt = a.output / (event + '.txt')
    prompt.write_text('Find the compiler-selected default body for ' + handler + ' and inspect its forwarding boundary.')
    directory = a.output / event
    client.initialize(directory, a.endpoint, prompt, catalog)
    identities = set()

    def call(name, args):
        raw, accounting = client.request(directory, 'tools/call', {'name': name, 'arguments': args})
        assert accounting['response_complete'] and accounting['http_status'] == 200
        wire = json.loads(raw)
        assert 'error' not in wire and not wire['result'].get('isError'), wire
        result = wire['result']['structuredContent']
        assert not result.get('truncated'), result
        if name.startswith('compiler_'):
            identities.add((result['snapshot_id'], result['artifact_sha256']))
        return result

    search = call('search_context', {'repo': 'eShop', 'query': handler, 'top_k': 5, 'token_budget': 2200})
    paths = sorted({b['rel_path'] for b in search['blocks'] if b['rel_path'].endswith('/' + handler + '.cs')})
    declarations = []
    for path in paths:
        declarations.extend(call('compiler_symbols', {'repo': 'eShop', 'path': path})['results'])
    classes = [r for r in declarations if any(f['kind'] == 'interface_default_selection' for f in r.get('implementation_facts', []))]
    methods = [r for r in declarations if any(f['rule'] == 'csharp-interface-closed-v1' for f in r.get('implementation_facts', []))]
    assert len(classes) == len(methods) == 1, declarations
    cls, method = classes[0], methods[0]
    selection, = cls['implementation_facts']
    correspondence, = method['implementation_facts']
    assert cls['extractor_version'] == method['extractor_version'] == '8'
    assert cls['context_id'] == method['context_id']
    assert cls['symbol']['id'] == selection['implementing_type_symbol_id'] == correspondence['implementing_type_symbol_id']
    symbols = {s['id']: s for s in cls['implementation_symbols'] + method['implementation_symbols']}
    closed = json.loads(symbols[selection['selected_default_symbol_id']]['descriptor'])
    forwarded = json.loads(symbols[correspondence['interface_symbol_id']]['descriptor'])
    assert closed['arguments'] == forwarded['arguments']
    assert closed['arguments'][0]['descriptor'] == 'T:Webhooks.API.IntegrationEvents.' + event
    assert closed['definition'] == symbols[selection['default_template_symbol_id']]['descriptor']
    choices = call('compiler_implementations', {'symbol_id': selection['interface_symbol_id']})
    assert cls['context_id'] in {c['context_id'] for c in choices['contexts']}
    reverse = call('compiler_implementations', {'symbol_id': selection['interface_symbol_id'], 'context_ids': [cls['context_id']]})
    assert len(reverse['matches']) == 3, reverse
    assert all(m['source']['implementation_facts'][m['implementation_fact_index']]['kind'] == 'interface_default_selection' for m in reverse['matches'])
    assert any(m['source']['occurrence_id'] == cls['occurrence_id'] for m in reverse['matches'])
    definitions = call('compiler_definitions', {'symbol_id': selection['default_template_symbol_id']})
    definition, = definitions['results']
    assert definition['symbol']['id'] == selection['default_template_symbol_id']
    body = call('read_source', {'repo': 'eShop', 'path': definition['path']})
    assert body['start_line'] == 1 and body['end_line'] == body['lines']
    offset, bom = raw_position(body['content'], definition['raw_sha256'], 'Handle((TIntegrationEvent)@event)', 'Handle')
    binding = call('compiler_binding_at', {'repo': 'eShop', 'path': definition['path'], 'byte_offset': offset, 'context_id': definition['context_id'], 'raw_sha256': definition['raw_sha256']})
    forward, = binding['results']
    assert forward['symbol']['descriptor'] == forwarded['definition']
    assert forward['symbol']['namespace'] == symbols[correspondence['interface_symbol_id']]['namespace']
    assert len(identities) == 1
    state = json.loads((directory / 'state.json').read_text())
    assert not state['stopped'], state
    report = {'case': event, 'classification': 'source_authored_public_workflow_not_independent_agent_score', 'passed': True,
              'calls': state['calls'], 'response_bytes': state['response_bytes'], 'compiler_identity': list(next(iter(identities))),
              'selection': selection, 'class_declaration': cls, 'closed_correspondence': method, 'template_definition': definition,
              'forwarding_call': forward, 'verified_bom_bytes': bom,
              'boundary': 'Compiler-selected closed default; template forwarding call and cast inspected separately. No automatic forwarding edge or runtime execution proof.'}
    client.atomic_json(directory / 'report.json', report)
    reports.append(report)
    print(event, report['calls'], report['response_bytes'], flush=True)
client.atomic_json(a.output / 'report.json', reports)
