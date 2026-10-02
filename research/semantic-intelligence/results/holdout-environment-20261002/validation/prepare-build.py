import hashlib,json,os,subprocess,time
from pathlib import Path
root=Path('.local/holdout-cleanarchitecture').resolve();out=root/'readiness';source=root/'source';workspace=root/'build-source'
subprocess.run(['git','clone','--no-hardlinks',str(source),str(workspace)],check=True)
config=out/'public-nuget.config';config.write_text('<configuration><packageSources><clear/><add key="nuget.org" value="https://api.nuget.org/v3/index.json"/></packageSources></configuration>\n')
env=dict(os.environ,DOTNET_CLI_HOME=str(root/'cli-home'),NUGET_PACKAGES=str(root/'packages'),DOTNET_PROCESSOR_COUNT='2',DOTNET_CLI_TELEMETRY_OPTOUT='1',DOTNET_SKIP_FIRST_TIME_EXPERIENCE='1')
argv=[str(root/'dotnet/dotnet'),'restore',str(workspace/'src/Web/Web.csproj'),'--configfile',str(config),'--disable-parallel','-p:OpenApiGenerateDocuments=false','-v:minimal']
start=time.monotonic()
with (out/'restore.stdout').open('w') as stdout,(out/'restore.stderr').open('w') as stderr:
 try:r=subprocess.run(argv,cwd=workspace,env=env,stdout=stdout,stderr=stderr,timeout=480);code=r.returncode
 except subprocess.TimeoutExpired:code='timeout'
(out/'build-preparation.json').write_text(json.dumps({'argv':argv,'returncode':code,'seconds':time.monotonic()-start,'variant':'checked-in template source; no custom DefineConstants; default SQLite branch; no publish or application execution','openapi_generation':False,'dependency_source':'public nuget.org only','source_commit':subprocess.check_output(['git','-C',str(workspace),'rev-parse','HEAD']).decode().strip()},indent=2)+'\n')
print('restore',code)
