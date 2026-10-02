import hashlib,json,shutil,subprocess
from pathlib import Path
root=Path.cwd(); local=root/'.local/capture-portability'; dest=root/'research/semantic-intelligence/results/capture-portability-20261001'
def sha(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1048576),b''):h.update(b)
 return h.hexdigest()
def copy(src,target):
 target.parent.mkdir(parents=True,exist_ok=True);assert not target.exists(),target;shutil.copy2(src,target)
assert not dest.exists();dest.mkdir()
for p in sorted(local.glob('*.log')):copy(p,dest/'logs'/p.name)
for n in ['native-commands.json','eshop-inventory.json','native-checks.py','archive.py','NetCoreBuildHost.cs.txt']:
 copy(local/n,dest/'runners'/n)
for folder in sorted(local.glob('eShopOnWeb-*'))+[local/'ForkJoint-final']:
 if folder.name.endswith('bundle') or folder.name.endswith('public'):continue
 for p in folder.iterdir():
  if p.is_file() and p.suffix in ['.json','.stdout','.stderr']:copy(p,dest/'captures'/folder.name/p.name)
for name in ['eShopOnWeb-public','ForkJoint-public']:
 for p in sorted((local/name).rglob('*')):
  if p.is_file():copy(p,dest/'public'/name/p.relative_to(local/name))
for name in ['forwarding','selection','bridge']:
 for p in sorted((local/name).rglob('*')):
  rel=p.relative_to(local/name)
  if p.is_file() and not {'bin','obj'}.intersection(rel.parts) and p.suffix in ['.cs','.csproj','.json','.jsonl','.stderr','.Config']:
   copy(p,dest/'native'/name/(str(rel)+('.txt' if p.suffix in ['.cs','.csproj'] else '')))
for p in [local/'eShopOnWeb-bundle/manifest.json',root/'.local/fresh-paths/ForkJoint-complete-bundle/manifest.json']:
 copy(p,dest/'dependencies'/(p.parent.name+'-manifest.json'))
copy(local/'worker/obj/project.assets.json',dest/'worker/project.assets.json')
worker_manifest=[{'path':str(p.relative_to(local/'worker/bin/Debug/net10.0')),'bytes':p.stat().st_size,'sha256':sha(p)} for p in sorted((local/'worker/bin/Debug/net10.0').rglob('*')) if p.is_file()]
(dest/'worker/files.json').write_text(json.dumps(worker_manifest,indent=2)+'\n')
files=['internal/semanticrun/dependency.go','internal/semanticrun/dependency_pack.go','internal/semanticrun/run.go','internal/semanticrun/toolchain.go','internal/semanticrun/run_test.go','internal/semanticrun/run_bundle_test.go','internal/semanticrun/dependency_pack_test.go','internal/semanticrun/diagnostic_test.go','internal/semanticrun/toolchain_test.go','internal/app/semanticcmd/dependencies.go','internal/app/semanticcmd/capture.go','internal/cli/semantic.go','tools/semantic-dotnet/Moedex.SemanticWorker.csproj','tools/semantic-dotnet/README.md','research/semantic-intelligence/check-project-capture.py','research/semantic-intelligence/agent-journeys/check_fresh_paths.py','research/semantic-intelligence/CAPTURE-PORTABILITY-ACCEPTANCE.md','docs/adr/0051-explicit-capture-toolchain-and-dependency-budgets.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md']
files+= [str(p.relative_to(root)) for p in sorted((root/'tools/semantic-dotnet').glob('*.cs'))]
for name in files:copy(root/name,dest/'source'/(name+'.txt'))
old=root/'research/semantic-intelligence/results/fresh-paths-20261001'
assert sha(old/'result.json')=='cb0149796fc8cade88639708a28c6b15995fe5962afb298ca5febd8ce238514d'
freeze=json.loads((old/'pre-capture-freeze.json').read_text())
for name,h in freeze['frozen_tools'].items():
 if name.endswith('.cs'):assert sha(root/name)==h,name
apps={}
for name,repo in [('eShopOnWeb','eShopOnWeb'),('ForkJoint','Sample-ForkJoint')]:
 inp=json.loads((local/(name+'-final')/'inputs.json').read_text()); assert inp['source_unchanged']
 assert sha(local/(name+'-final')/'project.semantic')==inp['artifact_sha256']
 gold=old/(repo+'-source-gold.json');assert sha(gold)==inp['gold_sha256'];copy(gold,dest/'gold'/gold.name)
 tracked=subprocess.check_output(['git','status','--porcelain','--untracked-files=no'],cwd=root/'.local/fresh-paths/corpus'/repo,text=True);assert not tracked,tracked
 report=json.loads((local/(name+'-public')/'report.json').read_text());rows=report['expectations'];assert all(r['binding_resolved'] for r in rows)
 apps[name]={'artifact_sha256':inp['artifact_sha256'],'source_unchanged':True,'commit':inp['commit'],'gold_sha256':inp['gold_sha256'],'anchors_resolved':len(rows),'facts_present':sum(r['fact_present'] is True for r in rows),'fact_gaps':[r['id'] for r in rows if r['fact_present'] is False],'binding_only':sum(r['fact_present'] is None for r in rows),'capture':json.loads((local/(name+'-final')/'capture.stdout').read_text())}
items=[{'path':str(p.relative_to(dest)),'sha256':sha(p),'bytes':p.stat().st_size} for p in sorted(dest.rglob('*')) if p.is_file()]
result={'status':'capture_portability_accepted','classification':'source_authored_development_baseline_not_independent_solver_score','worker_version':'9','compiler_version':'4.11.0.0','extractor_csharp_unchanged':True,'independent_solver_score':{'completed':3,'total':12},'apps':apps,'validation':{'full_go_tests':'passed with three native fixtures enabled','vet':'passed','race':'passed semanticrun/semanticcmd/cli','native_scripts':'three passed','worker_default_build':'zero warnings/errors','worker_pinned_build':'zero warnings/errors','historical_archive_unchanged':True,'corpus_tracked_sources_unchanged':True},'cli_sha256':sha(local/'moedex-v3'),'files':items,'file_count':len(items),'total_bytes':sum(i['bytes'] for i in items)}
(dest/'result.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps({'files':len(items),'bytes':result['total_bytes'],'manifest_sha256':sha(dest/'result.json'),'apps':{k:{a:b for a,b in v.items() if a!='capture'} for k,v in apps.items()}},indent=2))
