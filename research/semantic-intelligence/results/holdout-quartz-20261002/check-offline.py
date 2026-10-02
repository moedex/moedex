import json,os,shutil,subprocess,time
from pathlib import Path
base=Path('.local/holdout-quartz').resolve();work=base/'offline-source';packages=base/'offline-packages';dotnet=Path('.local/holdout-cleanarchitecture/dotnet/dotnet').resolve()
subprocess.run(['git','clone','--local','--no-hardlinks',str(base/'source'),str(work)],check=True,capture_output=True)
shutil.copytree(base/'bundle/packages',packages)
config=base/'offline.config';config.write_text('<configuration><packageSources><clear /></packageSources><fallbackPackageFolders><clear /></fallbackPackageFolders></configuration>\n')
env=os.environ.copy();env.update(DOTNET_ROOT=str(dotnet.parent),DOTNET_CLI_HOME=str(base/'offline-home'),NUGET_PACKAGES=str(packages),DOTNET_CLI_TELEMETRY_OPTOUT='1',DOTNET_SKIP_FIRST_TIME_EXPERIENCE='1',DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE='true')
report={'classification':'fresh upstream restore/build using copied bundle and cleared feeds, no product queries','commands':[]}
for name,argv in [('offline-restore',[str(dotnet),'restore','src/Quartz/Quartz.csproj','--configfile',str(config),'--packages',str(packages),'--disable-parallel','-p:NuGetAudit=false']),('offline-build',[str(dotnet),'build','src/Quartz/Quartz.csproj','--no-restore','-c','Debug','-m:1','-p:UseSharedCompilation=false','--nologo'])]:
 start=time.monotonic()
 with (base/(name+'.log')).open('wb') as log:
  try:code=subprocess.run(argv,cwd=work,env=env,stdout=log,stderr=subprocess.STDOUT,timeout=480).returncode
  except subprocess.TimeoutExpired:code=124
 report['commands'].append(dict(name=name,argv=argv,returncode=code,seconds=time.monotonic()-start))
 (base/'offline-environment.json').write_text(json.dumps(report,indent=2)+'\n')
 if code:break
print(json.dumps(report))
