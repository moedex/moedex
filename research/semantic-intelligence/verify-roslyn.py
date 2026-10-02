#!/usr/bin/env python3
"""Validate raw compiler output provenance and fixture completeness separately."""
import argparse
import hashlib
import json
from pathlib import Path
import evaluate


def verify(fixture, directory):
    gold=json.loads((fixture/'gold.json').read_text())
    all_rows={p['id']:[json.loads(l) for l in (directory/(p['id']+'.jsonl')).read_text().splitlines()] for p in gold['projects']}
    checks=[]
    def check(name,passed,detail=''):
        checks.append({'check':name,'pass':bool(passed),'detail':detail})
    projects={}
    spans=0
    for project in gold['projects']:
        name=project['id']
        rows=all_rows[name]
        own=[r for r in rows if r.get('project')==project['project_file']]
        records=[r for r in own if r['record_type']=='project']
        summaries=[r for r in own if r['record_type']=='summary']
        check(name+':project_status',len(records)==1 and records[0]['compilation_status']==project['expected_analysis'])
        check(name+':summary_status',len(summaries)==1 and summaries[0]['compilation_status']==project['expected_analysis'])
        if records:
            projects[name]=records[0]
            check(name+':source_roster',set(project['sources'])=={s['path'] for s in records[0]['sources']})
        repeat=directory/(name+'-repeat.jsonl')
        check(name+':repeat_bytes',repeat.exists() and repeat.read_bytes()==(directory/(name+'.jsonl')).read_bytes())
        for row in rows:
            if row['record_type'] not in ('declaration','reference'):continue
            raw=(fixture/row['source_path']).read_bytes()
            span=row['span']
            start,length=span['byte_offset'],span['byte_length']
            if start<0 or length<0 or start+length>len(raw):raise ValueError('span outside source')
            text=raw[start:start+length].decode('utf-8')
            if text!=row['source_text']:raise ValueError(f'wrong source span: {name} {row}')
            if row['source_sha256']!=hashlib.sha256(raw).hexdigest():raise ValueError('wrong raw source hash')
            blob=hashlib.sha1(b'blob '+str(len(raw)).encode()+b'\0'+raw).hexdigest()
            if row['blob_sha']!=blob:raise ValueError('wrong Git blob identity')
            spans+=1
    for expected in gold['expected_diagnostics']:
        start,length=evaluate.anchor_span(fixture,expected['anchor'])
        matching=[r for r in all_rows[expected['project']] if r['record_type']=='diagnostic' and r['code']==expected['code'] and r.get('source_path')==expected['anchor']['file']]
        check(expected['project']+':diagnostic:'+expected['code'],any(r['span']['byte_offset']<=start and r['span']['byte_offset']+r['span']['byte_length']>=start+length for r in matching))
    for pair in gold['context_differences']:
        names=pair['projects']
        records=[projects.get(name,{}) for name in names]
        contexts=[r.get('build_context') for r in records]
        hashes=[next((s['sha256'] for s in r.get('sources',[]) if s['path']==pair['shared_source']),None) for r in records]
        check(':'.join(names)+':same_bytes_distinct_context',all(contexts) and len(set(contexts))==len(contexts) and all(hashes) and len(set(hashes))==1)
    for path in gold['generated_inputs']:
        check('generated:'+path,any(s['path']==path and s['generated'] for p in projects.values() for s in p['sources']))
    return {'checks':checks,'pass':all(c['pass'] for c in checks),'validated_occurrence_spans':spans,
            'note':'Context differences and repeat equality are not incremental cache equivalence.'}


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    for key in ('fixture','roslyn','output'):parser.add_argument('--'+key,type=Path,required=True)
    args=parser.parse_args()
    result=verify(args.fixture,args.roslyn)
    args.output.write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps({k:v for k,v in result.items() if k!='checks'}))
    raise SystemExit(0 if result['pass'] else 1)

if __name__=='__main__':main()
