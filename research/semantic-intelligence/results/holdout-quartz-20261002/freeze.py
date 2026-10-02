import hashlib,json,os,shutil,subprocess
from pathlib import Path
base=Path('.local/holdout-quartz').resolve();frozen=base/'frozen';frozen.mkdir()
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def manifest(root):
 return [dict(path=str(p.relative_to(root)),size=p.stat().st_size,sha256=sha(p),**({'executable':True} if p.stat().st_mode&0o111 else {})) for p in sorted(root.rglob('*')) if p.is_file() and '__pycache__' not in p.parts]
def save(p,v):p.write_text(json.dumps(v,indent=2)+'\n')
shutil.copy2('.local/build-event-severity/moedex',frozen/'moedex')
shutil.copytree('.local/build-event-severity/accepted-worker/worker/bin/Debug/net10.0',frozen/'worker')
(frozen/'worker-source').mkdir()
for p in Path('tools/semantic-dotnet').iterdir():
 if p.suffix in ('.cs','.csproj'):shutil.copy2(p,frozen/'worker-source'/p.name)
(frozen/'harness').mkdir()
for name in ('client.py','browse.py','prepare.py','view.py'):
 shutil.copy2(Path('research/semantic-intelligence/agent-journeys')/name,frozen/'harness'/name)
(base/'bundle').mkdir();shutil.copytree(base/'packages',base/'bundle/packages')
packages=manifest(base/'bundle/packages');save(base/'bundle/manifest.json',dict(version=1,files=packages))
sdk=Path('.local/holdout-cleanarchitecture/dotnet').resolve()
save(base/'packet/toolchain-manifest.json',dict(root=str(sdk),version='10.0.401',files=manifest(sdk)))
save(base/'packet/product-manifest.json',dict(root=str(frozen),extractor_version=20,files=manifest(frozen)))
plan=dict(status='not executed; independent oracle review required before capture',namespace='Quartz',source_commit='15d90a9c2681cd9e273dcd3901c0fbbdbdc5fe90',capture_argv=[str(frozen/'moedex'),'semantic','capture','--checkout',str(base/'source'),'--commit','15d90a9c2681cd9e273dcd3901c0fbbdbdc5fe90','--origin','https://github.com/quartznet/quartznet.git','--repo','Quartz','--project','src/Quartz/Quartz.csproj','--framework','net10.0','--configuration','Debug','--dotnet',str(sdk/'dotnet'),'--sdk-path',str(sdk/'sdk/10.0.401'),'--worker',str(frozen/'worker/Moedex.SemanticWorker.dll'),'--workspace',str(base/'capture/workspace'),'--output',str(base/'capture/project.semantic'),'--dependency-bundle',str(base/'bundle'),'--restore-offline','--restore-standard-evaluation','--timeout','8m'],publication='fresh isolated index; graph=false; embedding=none; retain lexical and compiler capabilities separately',failure_policy='Retain failed capture; no tuning, rerun substitution or silent source-only fallback. A product correction retires this packet to development evidence.',setup='Use frozen prepare.py before assignment under same loopback permissions as solver. Freeze and retain native initialization and catalog. Full assignment deadline begins when fresh solver is launched; inspect instructions via view.py before calls.',CodeGraph='Paired arm pending verified dependency closure, binary provenance and isolated service setup. No claim of competitive success without it.')
save(base/'packet/launch-plan.json',plan)
shutil.copy2(base/'bundle/manifest.json',base/'packet/dependency-manifest.json')
print(json.dumps(dict(product_files=len(manifest(frozen)),packages=len(packages),package_bytes=sum(p['size'] for p in packages))))
