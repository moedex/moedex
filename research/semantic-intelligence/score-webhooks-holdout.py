#!/usr/bin/env python3
"""Independent frozen Webhooks native-artifact gate; no detector changes."""
import argparse,base64,collections,importlib.util,json,pathlib
spec=importlib.util.spec_from_file_location('native',pathlib.Path(__file__).with_name('score-moedex-component.py'))
native=importlib.util.module_from_spec(spec);spec.loader.exec_module(native)
GOLD_SHA='8d703f654a091d8c803b173612d3af4401b7e1034a1eb2ba20d34d3f00ac4f62'

def source_location(source):return {k:source[k] for k in ('project','path','byte_offset','byte_length','source_sha256')}
def protocol_for(gold):
    cases=[]
    for c in gold['cases']:
        if c['classification']!='supported':continue
        row={'id':c['id'],'source':source_location(c['source'])}
        if c['family']=='domain':
            row.update(family='domain_observation',expected_kind=c['kind'],expected_rule=c['rule'],expected_targets=c['targets'],owner=c['owner'])
            for key in ('lifetime','table','schema'):
                if key in c:row['expected_'+key]=c[key]
        elif c['family']=='implementation':
            row.update(family='method_implementation',**{k:c[k] for k in ('interface_member','implementation_method','implementing_type')})
        elif c['family']=='resolved_call':row.update(family='resolved_call',target=c['target'],owner=c['owner'])
        else:raise ValueError('unknown gold family')
        cases.append(row)
    return {'classification':gold['classification'],'positive_cases':cases,'negative_cases':[], 'reviewed_source_closure':{'paths':list(gold['reviewed_closure']['source_sha256']),'source_sha256':gold['reviewed_closure']['source_sha256']}}

def score(gold,artifact):
    report=native.score(protocol_for(gold),artifact);records=native.native_records(artifact)
    contexts={};incomplete=set()
    for c in artifact['contexts']:
        contexts.setdefault(c['project'],[]).append(c['id'])
        if c['status']!='complete':incomplete.add(c['project'])
    for row in report['positive_cases']:
        c=next(c for c in gold['cases'] if c['id']==row['case_id'])
        if c['source']['project'] in incomplete:row['outcome']='capture_incomplete'
    report['outcomes']=dict(collections.Counter(x['outcome'] for x in report['positive_cases']))
    report['unsupported_cases']=[];report['negative_controls']=[]
    for c in gold['cases']:
        if c['classification']=='supported':continue
        rows=[r for r in records if r['source']==source_location(c['source'])]
        want=sorted(contexts.get(c['source']['project'],[]));seen=sorted({r['context_id'] for r in rows})
        row={'case_id':c['id'],'classification':c['classification'],'expected_contexts':want,'observed_contexts':seen,'site_evidence':rows}
        if not want or want!=seen or c['source']['project'] in incomplete:row['outcome']='capture_incomplete'
        elif c['classification']=='unsupported':
            row['unexpected_domain_assertions']=[f for r in rows for f in r['domain_facts']]
            row['outcome']='unexpected_domain_assertion' if row['unexpected_domain_assertions'] else 'explicit_coverage_gap'
        else:
            row['violations']=[f for r in rows for f in r['domain_facts'] if f['kind'] in c['forbidden_kinds']]
            row['outcome']='wrong_assertion' if row['violations'] else 'no_forbidden_assertion_observed'
        report['unsupported_cases' if c['classification']=='unsupported' else 'negative_controls'].append(row)
    report['limitations']=['Independent capability-stratified selected application closure; not whole-eShop or agent-task quality.','Five unsupported patterns remain explicit gaps; no negative credit for them.','Native implementation evidence is distinct from public MCP exposure; this report alone establishes no public-tool behavior.','Unmatched semantic facts retained for review; no broad precision denominator from nine selected positives.']
    return report

def main():
    p=argparse.ArgumentParser()
    for name in ('gold','artifact','source-root','output'):p.add_argument('--'+name,type=pathlib.Path,required=True)
    a=p.parse_args();raw=a.gold.read_bytes()
    if native.digest(raw)!=GOLD_SHA:raise ValueError('frozen holdout gold changed')
    gold=json.loads(raw)
    for path,sha in gold['reviewed_closure']['source_sha256'].items():
        if native.digest((a.source_root/path).read_bytes())!=sha:raise ValueError('source drift '+path)
    encoded=a.artifact.read_bytes()
    if len(encoded)>64<<20:raise ValueError('artifact limit exceeded')
    envelope=json.loads(encoded);payload=base64.b64decode(envelope['payload'],validate=True)
    if envelope['format']!='moedex.semantic' or envelope['version']!=1 or native.digest(payload)!=envelope['sha256']:raise ValueError('invalid native artifact envelope')
    artifact=json.loads(payload)
    if any(s['repo']!=gold['repo'] or s['commit']!=gold['commit'] for s in artifact['snapshots']):raise ValueError('source pin mismatch')
    project_configs=set()
    for c in artifact['contexts']:
        props=json.loads(c['capture'])['global_properties'];project_configs.add((c['project'],props['Configuration'],props['TargetFramework']))
        if c['extractor']!='msbuild-roslyn' or c['extractor_version']!='6':raise ValueError('holdout requires unchanged worker6')
    expected={(p,gold['configuration'],gold['framework']) for p in gold['reviewed_closure']['projects']}
    if project_configs!=expected:raise ValueError('captured project/config closure differs from frozen closure')
    sources={s['id']:s for s in artifact['sources']}
    for path,sha in gold['reviewed_closure']['source_sha256'].items():
        matching=[s for s in artifact['sources'] if s['path']==path and s['raw_sha256']==sha]
        if not matching:raise ValueError('reviewed source absent '+path)
        projects=[p for p in gold['reviewed_closure']['projects'] if path.startswith(str(pathlib.Path(p).parent)+'/')]
        for c in artifact['contexts']:
            if c['project'] in projects and not any(sources[s]['path']==path and sources[s]['raw_sha256']==sha for s in c['source_ids']):raise ValueError('context source membership missing '+path)
    r=score(gold,artifact);r['provenance']={'gold_sha256':native.digest(raw),'artifact_sha256':native.digest(encoded),'source_commit':gold['commit'],'contexts':artifact['contexts'],'scorer_sha256':native.digest(pathlib.Path(__file__).read_bytes()),'shared_scorer_sha256':native.digest(pathlib.Path(__file__).with_name('score-moedex-component.py').read_bytes())}
    with a.output.open('x') as f:json.dump(r,f,indent=2);f.write('\n')
    print(json.dumps({'outcomes':r['outcomes'],'gaps':len(r['unsupported_cases']),'negative_outcomes':[n['outcome'] for n in r['negative_controls']]}))
if __name__=='__main__':main()
