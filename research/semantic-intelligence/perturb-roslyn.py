#!/usr/bin/env python3
"""Test same-project configuration and declaration-order changes on fresh copies."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    for key in ('dotnet','extractor-dll','framework-dir','fixture','baseline','output'):
        parser.add_argument('--'+key,type=Path,required=True)
    args=parser.parse_args()
    output=args.output.resolve()
    if output.exists():parser.error('output must not exist')
    output.mkdir(parents=True)
    env=os.environ.copy()
    env.update(DOTNET_ROOT=str(args.dotnet.resolve().parent),DOTNET_CLI_HOME=str(output/'cli'),DOTNET_CLI_TELEMETRY_OPTOUT='1',DOTNET_NOLOGO='1')
    checks=[]
    commands=[]
    def check(name,ok):checks.append({'check':name,'pass':bool(ok)})
    def records(path):return [json.loads(l) for l in path.read_text().splitlines()]
    def project(rows,name):return next(r for r in rows if r['record_type']=='project' and r['project']==name)
    def invoke(label,project_file,mutate):
        workspace=output/label
        shutil.copytree(args.fixture,workspace)
        mutate(workspace)
        hashes={str(p.relative_to(workspace)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(workspace.rglob('*')) if p.is_file()}
        command=list(map(str,[args.dotnet.resolve(),args.extractor_dll.resolve(),workspace/project_file,'--root',workspace,'--framework-dir',args.framework_dir.resolve()]))
        path=output/(label+'.jsonl')
        with path.open('wb') as stdout,(output/(label+'.stderr.txt')).open('wb') as stderr:
            process=subprocess.Popen(command,env=env,stdout=stdout,stderr=stderr,start_new_session=True)
            try:code=process.wait(timeout=120)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid,signal.SIGKILL)
                process.wait()
                code=124
        commands.append({'label':label,'argv':command,'exit_code':code,'source_sha256':hashes})
        return records(path),code
    def fast(root):
        path=root/'App/App.csproj'
        path.write_text(path.read_text().replace('</PropertyGroup>','<DefineConstants>FAST</DefineConstants></PropertyGroup>'))
    baseline=records(args.baseline/'App.jsonl')
    changed,code=invoke('config-fast','App/App.csproj',fast)
    check('config:complete',code==0)
    bp,cp=project(baseline,'App/App.csproj'),project(changed,'App/App.csproj')
    check('config:same_source_bytes',bp['sources']==cp['sources'])
    check('config:changed_context',bp['build_context']!=cp['build_context'])
    check('config:dependency_context_retained',project(baseline,'Contracts/Contracts.csproj')['build_context']==project(changed,'Contracts/Contracts.csproj')['build_context'])
    def descriptors(rows,line):
        return {r['symbol']['descriptor'] for r in rows if r['record_type']=='reference' and r['project']=='App/App.csproj' and r['source_path']=='App/Calls.cs' and r['span']['line']==line and r['source_text']=='Pick' and r.get('symbol')}
    gold=json.loads((args.fixture/'gold.json').read_text())
    slow=next(r for r in gold['references'] if r['id']=='conditional-default')['source']['line']
    fast_line=next(r for r in gold['references'] if r['id']=='conditional-fast')['source']['line']
    check('config:binding_switch',descriptors(baseline,slow)=={'M:CompilerFixture.Contracts.Overloads.Pick(System.String)'} and descriptors(changed,fast_line)=={'M:CompilerFixture.Contracts.Overloads.Pick(System.Int32)'} and not descriptors(changed,slow))
    def reorder(root):
        path=root/'Contracts/Types.cs'
        lines=path.read_text().splitlines(keepends=True)
        slots=[i for i,line in enumerate(lines) if 'public static string Pick(' in line]
        if len(slots)!=2:raise ValueError('expected two overloads')
        a,b=slots
        lines[a],lines[b]=lines[b],lines[a]
        path.write_text(''.join(lines))
    reordered,code=invoke('overload-reorder','Contracts/Contracts.csproj',reorder)
    original=records(args.baseline/'Contracts.jsonl')
    def overloads(rows):return {r['symbol']['descriptor'] for r in rows if r['record_type']=='declaration' and r['source_text']=='Pick'}
    check('reorder:complete',code==0)
    check('reorder:descriptors_stable',len(overloads(original))==2 and overloads(original)==overloads(reordered))
    check('reorder:context_invalidated',project(original,'Contracts/Contracts.csproj')['build_context']!=project(reordered,'Contracts/Contracts.csproj')['build_context'])
    result={'pass':all(c['pass'] for c in checks),'checks':checks,'commands':commands,
        'limitation':'Fresh full recompilations only; no incremental cache or dependency-version migration proved.'}
    (output/'results.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps({'pass':result['pass'],'checks':len(checks)}))
    raise SystemExit(0 if result['pass'] else 1)

if __name__=='__main__':main()
