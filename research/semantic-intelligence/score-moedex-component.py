#!/usr/bin/env python3
"""Score native Moedex audit evidence under the frozen component protocol.

This is a development extraction diagnostic, not an agent/product benchmark.
No missing facts or source locations are reconstructed from gold.
"""
import argparse
import base64
import hashlib
import json
from collections import Counter
from pathlib import Path

PROTOCOL_SHA = "c249d3d5f8eb4101c9c0f68cd1739847afd2251be72ece43e1b89a4c96bfb644"
# The pre-existing, production-validated worker6 development baseline. Pinning
# it also prevents dropping an entire context alternative to improve a score.
BASELINE_SHA = "24a95f5ad7307d6f3fdaf151e230b26e5cf72d2fec41971476fc4f7135cdac70"


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def validate_source_coverage(protocol, artifact):
    sources = {s['id']: s for s in artifact['sources']}
    reviewed = protocol['reviewed_source_closure']['source_sha256']
    projects = [c['project'] for c in protocol['project_configuration']]
    for context in artifact['contexts']:
        members = {sources[s]['path']: sources[s] for s in context['source_ids']}
        expected = {p: sha for p, sha in reviewed.items()
                    if p.startswith(str(Path(context['project']).parent) + '/')}
        for path, sha in expected.items():
            if path not in members or members[path]['raw_sha256'] != sha:
                raise ValueError('native source closure mismatch: ' + path)
    for path in reviewed:
        if not any(path.startswith(str(Path(p).parent) + '/') for p in projects):
            raise ValueError('reviewed source has no project mapping: ' + path)


def native_records(artifact):
    sources = {s['id']: s for s in artifact['sources']}
    contexts = {c['id']: c for c in artifact['contexts']}
    occurrences = {o['id']: o for o in artifact['occurrences']}
    symbols = {s['id']: s['key'] for s in artifact['symbols']}
    records = []
    for binding in artifact['bindings']:
        occurrence = occurrences[binding['occurrence_id']]
        source = sources[occurrence['source_id']]
        context = contexts[occurrence['context_id']]
        domains = []
        for fact in binding.get('domain_facts', []):
            domains.append({**fact, 'targets': [
                {'role': t['role'], 'symbol': symbols[t['symbol_id']]}
                for t in fact['targets']]})
        implementations = []
        for fact in binding.get('implementation_facts', []):
            implementations.append({
                'kind': fact['kind'], 'rule': fact['rule'],
                'evidence_scope': fact['evidence_scope'],
                'interface_member': symbols[fact['interface_symbol_id']],
                'implementing_type': symbols[fact['implementing_type_symbol_id']]})
        records.append({
            'binding_id': binding['id'], 'occurrence_id': occurrence['id'],
            'context_id': context['id'], 'snapshot_id': context['snapshot_id'],
            'source': {'project': context['project'], 'path': source['path'],
                       'byte_offset': occurrence['offset'], 'byte_length': occurrence['length'],
                       'source_sha256': source['raw_sha256']},
            'status': binding['status'], 'role': occurrence['role'], 'kind': occurrence['kind'],
            'method': binding['method'], 'extractor': binding['extractor'],
            'extractor_version': binding['extractor_version'],
            'symbol': symbols.get(binding.get('symbol_id')),
            'owner': symbols.get(binding.get('enclosing_symbol_id')),
            'domain_facts': domains, 'implementation_facts': implementations})
    return records


def expected_fact(case):
    if case['family'] == 'domain_observation':
        want = {'kind': case['expected_kind'], 'rule': case['expected_rule'],
                'evidence_scope': 'compile_time', 'targets': case['expected_targets']}
        for key in ('lifetime', 'table', 'schema'):
            if case.get('expected_' + key):
                want[key] = case['expected_' + key]
        return 'domain_facts', want
    if case['family'] == 'method_implementation':
        return 'implementation_facts', {
            'kind': 'interface_method_implementation', 'rule': 'csharp-interface-v1',
            'evidence_scope': 'compile_time', 'interface_member': case['interface_member'],
            'implementing_type': case['implementing_type']}
    return None, None


def matches(case, record):
    if record['source'] != case['source'] or record['status'] != 'resolved':
        return False
    family = case['family']
    field, want = expected_fact(case)
    if family == 'domain_observation':
        if case.get('owner') is not None and record['owner'] != case['owner']:
            return False
        return want in record[field]
    if family == 'resolved_call':
        return (record['kind'] == 'invocation' and record['role'] == 'reference'
                and record['owner'] == case['owner'] and record['symbol'] == case['target'])
    if family == 'method_implementation':
        return (record['role'] == 'declaration' and record['kind'] == 'declaration'
                and record['symbol'] == case['implementation_method']
                and want in record[field])
    raise ValueError('unknown frozen family: ' + family)


def score(protocol, artifact):
    records = native_records(artifact)
    by_project = {}
    for context in artifact['contexts']:
        by_project.setdefault(context['project'], []).append(context['id'])
    cases = []
    used = set()
    used_facts = set()
    for case in protocol['positive_cases']:
        scoped = [r for r in records if r['source'] == case['source']]
        expected_contexts = sorted(by_project.get(case['source']['project'], []))
        matched = [r for r in scoped if matches(case, r)]
        matched_contexts = sorted(set(r['context_id'] for r in matched))
        outcome = ('supported_exact_occurrence' if expected_contexts and matched_contexts == expected_contexts
                   else 'not_represented')
        if not expected_contexts:
            outcome = 'capture_incomplete'
        elif matched_contexts and matched_contexts != expected_contexts:
            outcome = 'capture_incomplete'
        elif scoped and any(r['domain_facts'] or r['implementation_facts'] for r in scoped):
            if not matched:
                outcome = 'wrong_assertion'
        used.update(r['binding_id'] for r in matched)
        field, want = expected_fact(case)
        if field:
            used_facts.update((r['binding_id'], field, i) for r in matched
                              for i, fact in enumerate(r[field]) if fact == want)
        cases.append({'case_id': case['id'], 'family': case['family'], 'outcome': outcome,
                      'expected_contexts': expected_contexts, 'matched_contexts': matched_contexts,
                      'evidence': matched, 'other_site_evidence': [r for r in scoped if r not in matched]})
    negatives = []
    for case in protocol['negative_cases']:
        violations = []
        expected_contexts = sorted(by_project.get(case['source']['project'], []))
        site = [r for r in records if r['source'] == case['source']]
        observed_contexts = sorted({r['context_id'] for r in site})
        for record in records:
            if case.get('caller_owner'):
                forbidden = (record['owner'] == case['caller_owner'] and any(
                    f['kind'] == 'message_publish' and any(t['symbol'] == case['message'] for t in f['targets'])
                    for f in record['domain_facts']))
            else:
                forbidden = record['source'] == case['source'] and bool(record['domain_facts'])
            if forbidden:
                violations.append(record)
        outcome = 'no_forbidden_assertion_observed'
        if not expected_contexts or observed_contexts != expected_contexts:
            outcome = 'capture_incomplete'
        if violations:
            outcome = 'wrong_assertion'
        negatives.append({'case_id': case['id'], 'outcome': outcome,
                          'expected_contexts': expected_contexts, 'observed_contexts': observed_contexts,
                          'site_evidence': site, 'evidence': violations})
    closure = set(protocol['reviewed_source_closure']['paths'])
    inventory = [r for r in records if r['source']['path'] in closure and (
        r['domain_facts'] or r['implementation_facts'] or r['kind'] == 'invocation')]
    outcomes = Counter(c['outcome'] for c in cases)
    families = {}
    for c in cases:
        families.setdefault(c['family'], Counter())[c['outcome']] += 1
    return {'arm': 'moedex', 'classification': protocol['classification'],
            'positive_cases': cases, 'negative_controls': negatives,
            'outcomes': dict(outcomes), 'by_family': {k: dict(v) for k, v in families.items()},
            'reviewed_inventory': inventory,
            'unscored_inventory_binding_ids': sorted({r['binding_id'] for r in inventory} - used),
            'unscored_native_facts': [
                {'binding_id': r['binding_id'], 'field': field, 'ordinal': i, 'fact': fact}
                for r in inventory for field in ('domain_facts', 'implementation_facts')
                for i, fact in enumerate(r[field]) if (r['binding_id'], field, i) not in used_facts],
            'limitations': ['Selected development observations, not held-out quality or a product comparison.',
                            'Unmatched inventory is unscored; no broad precision claim.',
                            'Exact native compiler contexts are retained; no runtime assertion.']}


def main():
    parser = argparse.ArgumentParser()
    for arg in ('protocol', 'artifact', 'source', 'output'):
        parser.add_argument('--' + arg, type=Path, required=True)
    args = parser.parse_args()
    raw_protocol = args.protocol.read_bytes()
    if digest(raw_protocol) != PROTOCOL_SHA:
        raise ValueError('protocol differs from frozen hash')
    protocol = json.loads(raw_protocol)
    for name, sha in protocol['reviewed_source_closure']['source_sha256'].items():
        if digest((args.source / name).read_bytes()) != sha:
            raise ValueError('source drift: ' + name)
    raw_artifact = args.artifact.read_bytes()
    if digest(raw_artifact) != BASELINE_SHA:
        raise ValueError('artifact differs from pinned worker6 development baseline')
    if len(raw_artifact) > 64 << 20:
        raise ValueError('artifact exceeds format limit')
    envelope = json.loads(raw_artifact)
    payload = base64.b64decode(envelope['payload'], validate=True)
    if envelope['format'] != 'moedex.semantic' or envelope['version'] != 1 or digest(payload) != envelope['sha256']:
        raise ValueError('invalid native artifact envelope')
    artifact = json.loads(payload)
    if any(s['commit'] != protocol['commit'] or s['repo'] != 'Sample-Outbox' for s in artifact['snapshots']):
        raise ValueError('source snapshot pin mismatch')
    configs = {(c['project'], c['configuration'], c['framework']) for c in protocol['project_configuration']}
    observed = set()
    for context in artifact['contexts']:
        capture = json.loads(context['capture'])
        properties = capture['global_properties']
        observed.add((context['project'], properties['Configuration'], properties['TargetFramework']))
        if context['status'] != 'complete' or context['extractor'] != 'msbuild-roslyn' or context['extractor_version'] != '6':
            raise ValueError('expected complete worker6 baseline')
    if observed != configs:
        raise ValueError('project/configuration closure mismatch')
    validate_source_coverage(protocol, artifact)
    report = score(protocol, artifact)
    report['provenance'] = {'protocol_sha256': digest(raw_protocol), 'artifact_sha256': digest(raw_artifact),
                            'source_commit': protocol['commit'],
                            'contexts': [{k: v for k, v in c.items() if k != 'capture'} for c in artifact['contexts']],
                            'scorer_sha256': digest(Path(__file__).read_bytes())}
    with args.output.open('x') as target:
        json.dump(report, target, indent=2)
        target.write('\n')
    print(json.dumps({'outcomes': report['outcomes'], 'negative_violations': sum(bool(n['evidence']) for n in report['negative_controls'])}))


if __name__ == '__main__':
    main()
