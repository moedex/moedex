import hashlib,json,os,shutil,subprocess,time
from pathlib import Path
r=Path('.local/holdout-cleanarchitecture').resolve();out=r/'readiness';source=Path('tools/semantic-dotnet');dest=r/'worker-source';dest.mkdir(exist_ok=False)
files=[]
for p in sorted(source.rglob('*')):
 rel=p.relative_to(source)
 if p.is_file() and not any(x in ('bin','obj') for x in rel.parts) and p.suffix in ('.cs','.csproj'):
  target=dest/rel;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(p,target);files.append({'path':str(rel),'sha256':hashlib.sha256(p.read_bytes()).hexdigest()})
env=dict(os.environ,DOTNET_CLI_HOME=str(r/'cli-home'),NUGET_PACKAGES=str(r/'worker-packages'),DOTNET_PROCESSOR_COUNT='2',DOTNET_CLI_TELEMETRY_OPTOUT='1')
args=[str(r/'dotnet/dotnet'),'build',str(dest/'Moedex.SemanticWorker.csproj'),'--configfile',str(out/'public-nuget.config'),'-v:minimal','-m:2'];start=time.monotonic()
with (out/'worker-build.stdout').open('w') as stdout,(out/'worker-build.stderr').open('w') as stderr:
 p=subprocess.run(args,env=env,stdout=stdout,stderr=stderr,timeout=120)
result={'argv':args,'returncode':p.returncode,'seconds':time.monotonic()-start,'source_files':files}
if p.returncode==0:
 result['worker_files']=[{'path':str(f.relative_to(dest)),'sha256':hashlib.sha256(f.read_bytes()).hexdigest()} for f in sorted((dest/'bin/Debug/net10.0').rglob('*')) if f.is_file()]
(out/'worker-build.json').write_text(json.dumps(result,indent=2)+'\n');print('worker build',p.returncode)
