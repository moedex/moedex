#!/usr/bin/env python3
"""Protocol correction: retain attributable EF bound-API calls as partial evidence.
The frozen initial scorer/report remains unchanged; no new semantic successes.
"""
import argparse
import importlib.util
import json
import pathlib
spec=importlib.util.spec_from_file_location('initial',pathlib.Path(__file__).with_name('score-codegraph-component.py'))
initial=importlib.util.module_from_spec(spec);spec.loader.exec_module(initial)

def score(protocol,raw,source_root):
    report=initial.score(protocol,raw,source_root)
    root=pathlib.Path(source_root).resolve()
    cases={c['id']:c for c in protocol['positive_cases']}
    for row in report['rows']:
        case=cases[row['case_id']]
        kind=case.get('expected_kind')
        if kind not in ('storage_entity_use','storage_entity_mapping'):continue
        entity=initial.canonical(case['expected_targets'][0]['symbol'])
        api=('Microsoft.EntityFrameworkCore.DbContext.Set' if kind=='storage_entity_use' else 'Microsoft.EntityFrameworkCore.ModelBuilder.Entity')+'<'+entity+'>()'
        found=[]
        for ri,result in enumerate(raw):
            owner=case['owner'];source=case['source']
            owners=[n for n in result.get('nodes',[]) if initial.canonical(n.get('qualifiedName'))==initial.canonical(owner) and n.get('dotnetProject')==pathlib.Path(source['project']).stem and initial.relative(n.get('filePath',''),root)==source['path']]
            if not owners:continue
            for ei,e in enumerate(result.get('edges',[])):
                if e.get('type')=='CALLS' and initial.canonical(e.get('sourceQN'))==initial.canonical(owner) and initial.canonical(e.get('targetQN'))==api and (e.get('properties') or {}).get('confidence')==1:
                    found.append({'result_index':ri,'edge_index':ei,'native':e})
        if found:
            row.update(outcome='partial_native_evidence',provenance='owner_level_bound_API_no_domain_relation_or_callsite',evidence=found,reason='Exact native bound framework API call is available; required entity semantic relation and exact callsite remain unrepresented.')
    report['outcome_counts']=dict(initial.collections.Counter(r['outcome'] for r in report['rows']))
    report['correction']='Version2 corrects two EF outcomes to partial_native_evidence per the frozen protocol generic-CALLS rule. Initial report preserved; full semantic support unchanged.'
    return report

def main():
    p=argparse.ArgumentParser();p.add_argument('--protocol',required=True);p.add_argument('--extraction',required=True);p.add_argument('--source-root',required=True);p.add_argument('--output',required=True);p.add_argument('--capture-status',default='unassessed');a=p.parse_args()
    if initial.sha(a.protocol)!=initial.PROTOCOL_SHA:raise ValueError('protocol changed')
    r=score(initial.load(a.protocol),initial.load(a.extraction),a.source_root);r.update(protocol_sha256=initial.sha(a.protocol),native_extraction_sha256=initial.sha(a.extraction),capture_status=a.capture_status,scorer_sha256=initial.sha(__file__))
    pathlib.Path(a.output).write_text(json.dumps(r,indent=2)+'\n')
if __name__=='__main__':main()
