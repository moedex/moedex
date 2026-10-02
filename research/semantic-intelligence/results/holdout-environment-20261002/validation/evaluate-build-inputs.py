import hashlib,json,os,subprocess
from pathlib import Path
r=Path('.local/holdout-cleanarchitecture').resolve();out=r/'readiness';workspace=r/'build-source'
env=dict(os.environ,DOTNET_CLI_HOME=str(r/'cli-home'),NUGET_PACKAGES=str(r/'packages'),DOTNET_PROCESSOR_COUNT='2',DOTNET_CLI_TELEMETRY_OPTOUT='1')
projects=['Web','Application','Domain','Infrastructure','Shared','ServiceDefaults'];roster=[]
for name in projects:
 p=workspace/'src'/name/(name+'.csproj')
 argv=[str(r/'dotnet/dotnet'),'msbuild',str(p),'-p:OpenApiGenerateDocuments=false','-getProperty:TargetFramework,Configuration,DefineConstants','-getItem:Compile,ProjectReference']
 result=subprocess.run(argv,cwd=workspace,env=env,capture_output=True,check=True,timeout=60)
 (out/(name+'-evaluation.json')).write_bytes(result.stdout)
 d=json.loads(result.stdout);files=[]
 for item in d['Items']['Compile']:
  path=Path(item['FullPath']);files.append({'path':str(path.relative_to(workspace)),'sha256':hashlib.sha256(path.read_bytes()).hexdigest()})
 roster.append({'project':str(p.relative_to(workspace)),'properties':d['Properties'],'compile_files':files,'references':[str(Path(i['FullPath']).relative_to(workspace)) for i in d['Items']['ProjectReference']]})
gold=json.loads(Path('research/semantic-intelligence/results/holdout-cleanarchitecture-20261002/source-gold.json').read_text())
expected={(c['path'],c['raw_sha256']) for t in gold['tasks'] for a in t['atoms'] for c in a['evidence']}
actual={(c['path'],c['sha256']) for p in roster for c in p['compile_files']}
assert expected<=actual
manifest=json.loads(Path('research/semantic-intelligence/results/holdout-cleanarchitecture-20261002/source-manifest.json').read_text())
for d in [r/'source',workspace]:
 for f in manifest['files']: assert hashlib.sha256((d/f['path']).read_bytes()).hexdigest()==f['sha256']
report={'classification':'MSBuild input evaluation, not semantic extraction or product query','projects':roster,'gold_source_files_present':len(expected),'source_tracked_files_unchanged':len(manifest['files']),'unique_compile_files':len(actual)}
(out/'build-inputs.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps({k:v for k,v in report.items() if k!='projects'}))
