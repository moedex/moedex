import os,json,subprocess,time,hashlib
from pathlib import Path
root=Path.cwd(); local=root/'.local/fresh-paths'; sdk=root/'.local/application-impact/independent/dotnet8/dotnet'; packages=local/'packages'; rows=[]
def run(label,args,cwd):
 row={'label':label,'argv':list(map(str,args)),'cwd':str(cwd)};rows.append(row); start=time.time()
 with (local/(label+'.log')).open('w') as f:r=subprocess.run(row['argv'],cwd=cwd,env=dict(os.environ,DOTNET_PROCESSOR_COUNT='2',DOTNET_NOLOGO='1',DOTNET_CLI_TELEMETRY_OPTOUT='1',NUGET_PACKAGES=str(packages)),stdout=f,stderr=subprocess.STDOUT,timeout=900)
 row.update(returncode=r.returncode,elapsed_seconds=time.time()-start);(local/'provision-commands.json').write_text(json.dumps(rows,indent=2)+'\n');print(label,r.returncode,flush=True);r.check_returncode()
for repo,project in [('eShopOnWeb','src/Web/Web.csproj'),('Sample-ForkJoint','src/ForkJoint.Api/ForkJoint.Api.csproj')]:
 dest=local/'provision'/repo;dest.parent.mkdir(exist_ok=True);run(repo+'-clone',['git','clone','--no-hardlinks',local/'corpus'/repo,dest],root)
 (dest/'global.json').write_text('{"sdk":{"version":"8.0.400","rollForward":"disable"}}\n')
 run(repo+'-restore',[sdk,'restore',project,'--packages',packages,'--source','https://api.nuget.org/v3/index.json','--disable-parallel','-p:NuGetAudit=false','-p:TargetFramework=net8.0','-p:Configuration=Debug'],dest)
run('pack',[root/'.local/default-forwarding/moedex-final','semantic','dependencies','pack','--packages',packages,'--output',local/'bundle'],root)
