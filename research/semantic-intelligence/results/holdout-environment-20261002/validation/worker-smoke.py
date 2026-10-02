import hashlib,json,os,subprocess
from pathlib import Path
r=Path('.local/holdout-cleanarchitecture').resolve();out=r/'readiness';probe=r/'sdk-probe';probe.mkdir(exist_ok=False)
(probe/'Probe.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup></Project>\n')
(probe/'Probe.cs').write_text('namespace SDKProbe; public interface IProbe { int Run(); } public sealed class Probe : IProbe { public int Run() => 42; }\n')
env=dict(os.environ,DOTNET_CLI_HOME=str(r/'offline-cli-home'),NUGET_PACKAGES=str(r/'worker-packages'),DOTNET_PROCESSOR_COUNT='2',DOTNET_CLI_TELEMETRY_OPTOUT='1')
a=[str(r/'dotnet/dotnet'),'restore',str(probe/'Probe.csproj'),'--configfile',str(out/'offline-nuget.config'),'-p:NuGetAudit=false'];p=subprocess.run(a,env=env,capture_output=True,timeout=60);(out/'probe-restore.stdout').write_bytes(p.stdout);(out/'probe-restore.stderr').write_bytes(p.stderr);p.check_returncode()
a=[str(r/'dotnet/dotnet'),str(r/'worker-source/bin/Debug/net10.0/Moedex.SemanticWorker.dll'),'--repo','SDKProbe','--root',str(probe),'--project','Probe.csproj','--framework','net10.0','--sdk-path',str(r/'dotnet/sdk/10.0.401')]
p=subprocess.run(a,env=env,capture_output=True,timeout=60);(out/'probe-capture.jsonl').write_bytes(p.stdout);(out/'probe-capture.stderr').write_bytes(p.stderr);p.check_returncode()
rows=[json.loads(l) for l in p.stdout.splitlines()];assert rows[-1]['record_type']=='stream_summary' and rows[-1]['compilation_status']=='complete';assert rows[0]['extractor_version']=='18'
c=json.loads(rows[0]['capture_json']);assert any(s['path']=='Probe.cs' and s['sha256']==hashlib.sha256((probe/'Probe.cs').read_bytes()).hexdigest() for s in c['sources'])
report={'classification':'synthetic SDK/worker smoke; no holdout product queries','argv':a,'summary':rows[-1],'compiler_version':rows[0]['compiler_version'],'extractor_version':rows[0]['extractor_version'],'source_hash_verified':True}
(out/'worker-smoke.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
