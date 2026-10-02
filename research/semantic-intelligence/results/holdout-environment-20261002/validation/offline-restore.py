import hashlib,json,os,subprocess,time
from pathlib import Path
r=Path('.local/holdout-cleanarchitecture').resolve();out=r/'readiness';workspace=r/'offline-source'
subprocess.run(['git','clone','--no-hardlinks',str(r/'source'),str(workspace)],check=True)
config=out/'offline-nuget.config';config.write_text('<configuration><packageSources><clear/></packageSources></configuration>\n')
env=dict(os.environ,DOTNET_CLI_HOME=str(r/'offline-cli-home'),NUGET_PACKAGES=str(r/'bundle/packages'),DOTNET_PROCESSOR_COUNT='2',DOTNET_CLI_TELEMETRY_OPTOUT='1',DOTNET_SKIP_FIRST_TIME_EXPERIENCE='1')
a=[str(r/'dotnet/dotnet'),'restore',str(workspace/'src/Web/Web.csproj'),'--configfile',str(config),'--disable-parallel','-p:NuGetAudit=false','-p:OpenApiGenerateDocuments=false','-v:minimal'];start=time.monotonic()
with (out/'offline-restore.stdout').open('w') as stdout,(out/'offline-restore.stderr').open('w') as stderr:
 p=subprocess.run(a,cwd=workspace,env=env,stdout=stdout,stderr=stderr,timeout=120)
manifest=json.loads((r/'bundle/manifest.json').read_text())
for f in manifest['files']:
 raw=(r/'bundle/packages'/f['path']).read_bytes();assert len(raw)==f['size'];assert hashlib.sha256(raw).hexdigest()==f['sha256']
source_manifest=json.loads(Path('research/semantic-intelligence/results/holdout-cleanarchitecture-20261002/source-manifest.json').read_text())
for f in source_manifest['files']:assert hashlib.sha256((workspace/f['path']).read_bytes()).hexdigest()==f['sha256']
report={'argv':a,'returncode':p.returncode,'seconds':time.monotonic()-start,'bundle_files_verified_after_restore':len(manifest['files']),'tracked_source_files_unchanged':len(source_manifest['files']),'network_package_sources':[],'nuget_audit':False,'scope':'Restore only; no application or Moedex extraction run'}
(out/'offline-restore.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
