import json,os,subprocess,time
from pathlib import Path
base=Path('.local/holdout-quartz').resolve();source=base/'source';work=base/'build-source';dotnet=Path('.local/holdout-cleanarchitecture/dotnet/dotnet').resolve()
subprocess.run(['git','clone','--local','--no-hardlinks',str(source),str(work)],check=True,capture_output=True)
env=os.environ.copy();env.update(DOTNET_ROOT=str(dotnet.parent),DOTNET_CLI_HOME=str(base/'dotnet-home'),NUGET_PACKAGES=str(base/'packages'),DOTNET_CLI_TELEMETRY_OPTOUT='1',DOTNET_SKIP_FIRST_TIME_EXPERIENCE='1',DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE='true')
commands=[('sdk',[str(dotnet),'--version']),('restore',[str(dotnet),'restore','src/Quartz/Quartz.csproj','--configfile',str(work/'NuGet.Config'),'--packages',str(base/'packages'),'--disable-parallel']),('build',[str(dotnet),'build','src/Quartz/Quartz.csproj','--no-restore','-c','Debug','-m:1','-p:UseSharedCompilation=false','--nologo'])]
report={'classification':'upstream prerequisite build only; no Moedex capture/query','project':'src/Quartz/Quartz.csproj','configuration':'Debug','framework':'net10.0','commands':[]}
for name,argv in commands:
 start=time.monotonic()
 with (base/(name+'.log')).open('wb') as log:
  try:r=subprocess.run(argv,cwd=work,env=env,stdout=log,stderr=subprocess.STDOUT,timeout=480);code=r.returncode
  except subprocess.TimeoutExpired:code=124
 report['commands'].append(dict(name=name,argv=argv,returncode=code,seconds=time.monotonic()-start))
 (base/'environment.json').write_text(json.dumps(report,indent=2)+'\n')
 if code:break
print(json.dumps(report))
