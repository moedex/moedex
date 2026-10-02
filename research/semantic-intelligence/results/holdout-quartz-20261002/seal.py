import hashlib,json,shutil,subprocess
from pathlib import Path
base=Path('.local/holdout-quartz');packet=base/'packet';source=base/'source';out=Path('research/semantic-intelligence/results/holdout-quartz-20261002')
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def save(p,v):p.write_text(json.dumps(v,indent=2)+'\n')
assert not out.exists()
prior=[]
for n in ('build-event-severity','capture-diagnostics','holdout-journeys'):
 p=Path('research/semantic-intelligence/results')/(n+'-20261002')/'result.json';r=json.loads(p.read_text())
 for f in r['files']:assert sha(p.parent/f['path'])==f['sha256'],f
 prior.append(dict(path=str(p),sha256=sha(p),verified_files=len(r['files'])))
manifest=json.loads((packet/'source-manifest.json').read_text())
for f in manifest['files']:
 for checkout in ('source','build-source','offline-source'):
  assert sha(base/checkout/f['path'])==f['sha256']
for name in ('toolchain-manifest.json','product-manifest.json'):
 m=json.loads((packet/name).read_text())
 for f in m['files']:assert sha(Path(m['root'])/f['path'])==f['sha256']
for f in json.loads((packet/'dependency-manifest.json').read_text())['files']:assert sha(base/'bundle/packages'/f['path'])==f['sha256']
gold=json.loads((packet/'source-gold.json').read_text());ranges=[]
for t in gold['tasks']:
 for a in t['atoms']:
  for e in a['evidence']:
   raw=(source/e['path']).read_bytes();assert hashlib.sha256(raw).hexdigest()==e['raw_sha256'];assert '\n'.join(raw.decode('utf-8-sig').splitlines()[e['start_line']-1:e['end_line']])==e['excerpt'];ranges.append(e)
shutil.copytree(packet,out)
for name in ('environment.json','offline-environment.json','sdk.log','restore.log','build.log','offline-restore.log','offline-build.log','harness-tests.log','prepare-environment.py','check-offline.py','freeze.py','offline.config','seal.py'):
 shutil.copy2(base/name,out/name)
shutil.copytree(base/'frozen/harness',out/'harness')
for name in ('test_view.py','README.md'):shutil.copy2(Path('research/semantic-intelligence/agent-journeys')/name,out/'harness'/name)
for p in sorted(set(e['path'] for e in ranges)|{'LICENSE.txt','global.json','Directory.Build.props','Directory.Build.targets','Directory.Packages.props','NuGet.Config','src/Quartz/Quartz.csproj','src/Quartz.Analyzers/Quartz.Analyzers.csproj'}):
 origin=source/p
 if not origin.exists():
  assert p=='LICENSE.txt';continue
 dest=out/'source'/p;dest.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(origin,dest)
files=[dict(path=str(p.relative_to(out)),bytes=p.stat().st_size,sha256=sha(p)) for p in sorted(out.rglob('*')) if p.is_file()]
result=dict(classification='source-first holdout preparation and environment readiness; NOT an independently reviewed oracle or product quality score',repository=manifest['repository'],commit=manifest['commit'],tracked_files=len(manifest['files']),tasks=len(gold['tasks']),atoms=sum(len(t['atoms']) for t in gold['tasks']),source_evidence_ranges=len(ranges),gold_source_files=len(set(e['path'] for e in ranges)),upstream_build='pass: Debug/net10.0, SDK10.0.401, zero warnings/errors',offline_build='pass: fresh checkout and copied dependencies with package feeds cleared',harness_tests=24,independent_oracle_review='pending authorization under user single-threaded/no-subagents instruction',product_capture='not attempted',product_queries=0,scored_solvers=0,codegraph_paired='not executed; setup readiness remains open',gpu_used=False,subagents_used=0,prior_archives_verified=prior,files=files)
save(out/'result.json',result)
print(json.dumps(dict(result=str(out/'result.json'),sha256=sha(out/'result.json'),files=len(files),bytes=sum(f['bytes'] for f in files))))
