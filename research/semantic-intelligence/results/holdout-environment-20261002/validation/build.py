import json,os,subprocess,time
from pathlib import Path
r=Path('.local/holdout-cleanarchitecture').resolve();out=r/'readiness'
env=dict(os.environ,DOTNET_CLI_HOME=str(r/'cli-home'),NUGET_PACKAGES=str(r/'packages'),DOTNET_PROCESSOR_COUNT='2',DOTNET_CLI_TELEMETRY_OPTOUT='1',DOTNET_SKIP_FIRST_TIME_EXPERIENCE='1')
a=[str(r/'dotnet/dotnet'),'build',str(r/'build-source/src/Web/Web.csproj'),'--no-restore','-p:OpenApiGenerateDocuments=false','-m:2','-v:minimal'];start=time.monotonic()
with (out/'build.stdout').open('w') as stdout,(out/'build.stderr').open('w') as stderr:
 try:p=subprocess.run(a,cwd=r/'build-source',env=env,stdout=stdout,stderr=stderr,timeout=300);code=p.returncode
 except subprocess.TimeoutExpired:code='timeout'
(out/'build-result.json').write_text(json.dumps({'argv':a,'returncode':code,'seconds':time.monotonic()-start},indent=2)+'\n');print('build',code)
