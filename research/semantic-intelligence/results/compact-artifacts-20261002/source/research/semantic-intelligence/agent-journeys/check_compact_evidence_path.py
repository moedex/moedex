#!/usr/bin/env python3
"""Paired public comparison of identical class/registration/forwarding evidence.

Starts from preserved discovered identities, so discovery and read_source costs
are outside both arms. This is not an independent solver evaluation.
"""
import argparse
import json
from pathlib import Path
import client

p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--endpoint',required=True)
p.add_argument('--baseline',type=Path,required=True)
p.add_argument('--output',type=Path,required=True)
a=p.parse_args();a.output.mkdir(parents=True,exist_ok=False)
cases=json.loads(a.baseline.read_text());reports=[]
for case in cases:
    name=case['case'];directory=a.output/name;directory.mkdir();calls=[]
    def call(arm,tool,args):
        req={'jsonrpc':'2.0','id':len(calls)+1,'method':'tools/call','params':{'name':tool,'arguments':args}}
        wire,status,_=client.http_exchange(a.endpoint,json.dumps(req).encode(),60)
        n=len(calls)+1;(directory/f'{n:02d}-request.json').write_text(json.dumps(req,indent=2)+'\n');(directory/f'{n:02d}-response.json').write_bytes(wire)
        data=json.loads(wire);assert status==200 and 'error' not in data and not data['result'].get('isError'),data
        result=data['result']['structuredContent'];assert not result.get('truncated'),result
        calls.append({'arm':arm,'tool':tool,'bytes':len(wire)})
        assert (result['snapshot_id'],result['artifact_sha256'])==tuple(case['compiler_identity'])
        return result
    cls=case['class_declaration'];selection=case['selection'];f=selection['forwarding'];contexts=sorted(set([cls['context_id'],f['call_context_id']]))
    old=[]
    for symbol,context in [(cls['symbol']['id'],cls['context_id']),(f['implementation_symbol_id'],cls['context_id']),(selection['default_template_symbol_id'],f['call_context_id'])]:
        old+=call('legacy','compiler_definitions',{'symbol_id':symbol,'context_id':context})['results']
    old+=call('legacy','compiler_binding_at',{'repo':cls['repo'],'path':f['call_path'],'byte_offset':f['call_offset'],'context_id':f['call_context_id'],'raw_sha256':f['call_sha256']})['results']
    registrations=call('legacy','compiler_contract_context',{'symbol_id':cls['symbol']['id'],'context_ids':contexts})
    assert not registrations['evidence_truncated'] and not registrations['definitions_truncated']
    compact=call('compact','compiler_evidence_path',{'symbol_id':cls['symbol']['id'],'context_ids':contexts})
    assert compact['status']=='ok' and not compact['required_context_ids']
    records={r['source']['occurrence_id']:r for r in compact['records']}
    expected=set()
    for r in old:
        expected.add(r['occurrence_id']);c=records[r['occurrence_id']]
        assert c['symbol_id']==r['symbol']['id'] and c['context_id']==r['context_id']
        for key in ['raw_sha256','byte_offset','byte_length','path','source_id','extractor_version']:
            assert c['source'][key]==r[key]
        assert c.get('implementation_facts',[])==r.get('implementation_facts',[])
    for group in registrations['groups']:
        for evidence in group['evidence']:
            assert evidence['fact']['kind']=='di_keyed_registration_configuration',evidence
            expected.add(evidence['source']['occurrence_id']);c=records[evidence['source']['occurrence_id']]
            assert c['context_id']==group['context_id'] and c['source']==evidence['source']
            assert evidence['fact'] in c['domain_facts']
    assert set(records)==expected and len(expected)==5
    # Selecting only the class context must expose the missing explicit scope,
    # never silently return the source-call witness from another project.
    partial=call('scope-control','compiler_evidence_path',{'symbol_id':cls['symbol']['id'],'context_ids':[cls['context_id']]})
    assert partial['required_context_ids']==[f['call_context_id']]
    assert f['call_occurrence_id'] not in {r['source']['occurrence_id'] for r in partial['records']}
    report={'case':name,'evidence_records':len(expected),'equal_source_facts':True,'explicit_scope_control':True,'calls':calls}
    for arm in ['legacy','compact']:
        selected=[c for c in calls if c['arm']==arm];report[arm]={'calls':len(selected),'response_bytes':sum(c['bytes'] for c in selected)}
    reports.append(report);print(name,report['legacy'],report['compact'],flush=True)
(a.output/'report.json').write_text(json.dumps(reports,indent=2)+'\n')
