#!/usr/bin/env python3
"""Build and run the dependency-free Roslyn experiment in a writable projection."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    for name in ('dotnet','fixture','output','framework-dir'):
        parser.add_argument('--'+name,type=Path,required=True)
    parser.add_argument('--extractor',type=Path,default=Path(__file__).parent/'roslyn')
    args=parser.parse_args()
    output=args.output.resolve()
    if output.exists():parser.error('output must not exist; preserve earlier results')
    output.mkdir(parents=True)
    workspace=output/'workspace'
    shutil.copytree(args.fixture,workspace)
    shutil.copytree(args.extractor,output/'extractor',ignore=shutil.ignore_patterns('bin','obj'))
    env=os.environ.copy()
    env.update(DOTNET_ROOT=str(args.dotnet.resolve().parent),DOTNET_CLI_HOME=str(output/'cli'),
        DOTNET_CLI_TELEMETRY_OPTOUT='1',DOTNET_NOLOGO='1',NUGET_PACKAGES=str(output/'nuget'),
        PATH=str(args.dotnet.resolve().parent)+os.pathsep+env.get('PATH',''))
    manifest={'format_version':1,'commands':[],
        'source_sha256':{str(p.relative_to(workspace)):hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(workspace.rglob('*')) if p.is_file()},
        'extractor_sha256':{str(p.relative_to(args.extractor)):hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(args.extractor.rglob('*')) if p.is_file() and 'bin' not in p.parts and 'obj' not in p.parts}}
    def run(label,argv,stdout=None,timed=False):
        command=list(map(str,argv))
        if timed:command=['/usr/bin/time','-l','-o',str(output/(label+'.time.txt'))]+command
        start=time.monotonic()
        with (output/(stdout or label+'.stdout.txt')).open('wb') as out,(output/(label+'.stderr.txt')).open('wb') as err:
            process=subprocess.Popen(command,cwd=output,env=env,stdout=out,stderr=err,start_new_session=True)
            try:code=process.wait(timeout=300)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid,signal.SIGKILL)
                process.wait()
                code=124
        manifest['commands'].append({'label':label,'argv':command,'exit_code':code,'elapsed_seconds':time.monotonic()-start})
        (output/'run.json').write_text(json.dumps(manifest,indent=2)+'\n')
        return code
    run('dotnet-info',[args.dotnet.resolve(),'--info'])
    if run('extractor-build',[args.dotnet.resolve(),'build',output/'extractor/Moedex.RoslynSpike.csproj','-o',output/'bin'],timed=True):
        raise SystemExit('extractor build failed; see logs')
    gold=json.loads((workspace/'gold.json').read_text())
    for project in gold['projects']:
        command=[args.dotnet.resolve(),output/'bin/Moedex.RoslynSpike.dll',workspace/project['project_file'],
            '--root',workspace,'--framework-dir',args.framework_dir.resolve()]
        code=run(project['id'],command,project['id']+'.jsonl',True)
        if code not in (0,2):raise SystemExit(f'extraction failed for {project["id"]}; see logs')
        # Repeated extraction measures a warm process launch/filesystem pass,
        # not an in-process incremental compiler or language server session.
        repeat_code=run(project['id']+'-repeat',command,project['id']+'-repeat.jsonl',True)
        if repeat_code != code:raise SystemExit(f'repeat status changed for {project["id"]}; see logs')
    print(output/'run.json')

if __name__=='__main__':main()
