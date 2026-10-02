import hashlib,json,shutil,subprocess
from pathlib import Path
root=Path.cwd();local=root/'.local/response-di';dest=root/'research/semantic-intelligence/results/response-di-20261001'
def sha(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1048576),b''):h.update(b)
 return h.hexdigest()
def copy(p,t):
 t.parent.mkdir(parents=True,exist_ok=True);assert not t.exists(),t;shutil.copy2(p,t)
assert not dest.exists();dest.mkdir()
for p in sorted(local.glob('*.log')):copy(p,dest/'logs'/p.name)
for n in ['native-checks.py','native-commands.json','archive.py']:copy(local/n,dest/'runners'/n)
for name in ['ForkJoint','ForkJoint-final','eShopOnWeb']:
 for p in (local/name).iterdir():
  if p.is_file() and p.suffix in ['.json','.stdout','.stderr']:copy(p,dest/'captures'/name/p.name)
for name in ['ForkJoint-public','eShopOnWeb-public']:
 for p in sorted((local/name).rglob('*')):
  if p.is_file():copy(p,dest/'public'/name/p.relative_to(local/name))
for name in ['native','forwarding','selection','bridge']:
 for p in sorted((local/name).rglob('*')):
  rel=p.relative_to(local/name)
  if p.is_file() and not {'bin','obj'}.intersection(rel.parts) and p.suffix in ['.cs','.csproj','.json','.jsonl','.stderr','.Config']:
   copy(p,dest/'native'/name/(str(rel)+('.txt' if p.suffix in ['.cs','.csproj'] else '')))
files=['internal/semantic/domain.go','internal/semantic/domain_version.go','internal/semantic/domain_version_test.go','internal/semantic/domain_v5_test.go','internal/semantic/implementation.go','internal/semantic/default_forwarding.go','internal/semantic/default_forwarding_test.go','internal/semantic/default_selection_test.go','internal/semantic/validate.go','internal/semanticindex/implementation.go','internal/semanticindex/implementations_test.go','internal/semanticimport/revalidate.go','internal/semanticimport/revalidate_test.go','internal/semanticimport/default_selection_public_test.go','internal/semanticimport/response_di_public_test.go','internal/mcp/compiler.go','internal/mcp/compiler_implementation.go','internal/mcp/compiler_implementations.go','internal/app/servecmd/webhooks_holdout_public_test.go','tools/semantic-dotnet/test_response_di.py','tools/semantic-dotnet/test_default_selection.py','tools/semantic-dotnet/test_default_forwarding.py','tools/semantic-dotnet/test_framework_bridge.py','tools/semantic-dotnet/Moedex.SemanticWorker.csproj','tools/semantic-dotnet/README.md','research/semantic-intelligence/check-project-capture.py','research/semantic-intelligence/agent-journeys/check_fresh_paths.py','research/semantic-intelligence/RESPONSE-DI-ACCEPTANCE.md','docs/adr/0052-typed-responses-and-conditional-di.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md']
files += [str(p.relative_to(root)) for p in sorted((root/'tools/semantic-dotnet').glob('*.cs'))]
for name in files:copy(root/name,dest/'source'/(name+'.txt'))
for p in (root/'tools/semantic-dotnet').glob('*.cs'):assert sha(p)==sha(local/'worker'/p.name),p
copy(local/'worker/obj/project.assets.json',dest/'worker/project.assets.json')
worker=local/'worker/bin/Debug/net10.0'
manifest=[{'path':str(p.relative_to(worker)),'sha256':sha(p),'bytes':p.stat().st_size} for p in sorted(worker.rglob('*')) if p.is_file()]
(dest/'worker/files.json').write_text(json.dumps(manifest,indent=2)+'\n')
old=root/'research/semantic-intelligence/results/capture-portability-20261001';assert sha(old/'result.json')=='eb0ec2286c6e6d29a1fd5b7df02ffc065459eda2ecb2f6a3d393ad13377f544f'
# Validate every retained byte of the immediately preceding milestone.
for f in json.loads((old/'result.json').read_text())['files']:assert sha(old/f['path'])==f['sha256'],f['path']
apps={}
for name,folder,repo in [('ForkJoint','ForkJoint-final','Sample-ForkJoint'),('eShopOnWeb','eShopOnWeb','eShopOnWeb')]:
 inp=json.loads((local/folder/'inputs.json').read_text());assert inp['source_unchanged']
 assert sha(local/folder/'project.semantic')==inp['artifact_sha256']
 gold=root/'research/semantic-intelligence/results/fresh-paths-20261001'/(repo+'-source-gold.json');assert sha(gold)==inp['gold_sha256'];copy(gold,dest/'gold'/gold.name)
 assert not subprocess.check_output(['git','status','--porcelain','--untracked-files=no'],cwd=root/'.local/fresh-paths/corpus'/repo,text=True)
 report=json.loads((local/(name+'-public')/'report.json').read_text());rows=report['expectations'];assert all(r['binding_resolved'] for r in rows)
 before=json.loads((root/'.local/capture-portability'/(name+'-public')/'report.json').read_text())
 previous={r['id']:r['fact_present'] for r in before['expectations']}
 changed=[r['id'] for r in rows if r['fact_present']!=previous[r['id']]]
 assert changed==(['fry-response','fryer-registration'] if name=='ForkJoint' else [])
 if name=='ForkJoint':
  response=next(r for r in rows if r['id']=='fry-response')['facts'];assert len(response)==1 and response[0]['kind']=='message_response' and response[0]['targets'][0]['symbol']['descriptor']=='T:ForkJoint.Contracts.FryReady'
  di=next(r for r in rows if r['id']=='fryer-registration')['facts'];assert len(di)==1 and di[0]['kind']=='di_registration_if_absent' and di[0]['lifetime']=='singleton'
  assert [(t['role'],t['symbol']['descriptor']) for t in di[0]['targets']]==[('service','T:ForkJoint.Api.Services.IFryer'),('implementation','T:ForkJoint.Api.Services.Fryer')]
 apps[name]={'artifact_sha256':inp['artifact_sha256'],'source_unchanged':True,'commit':inp['commit'],'gold_sha256':inp['gold_sha256'],'anchors_resolved':len(rows),'facts_present':sum(r['fact_present'] is True for r in rows),'fact_gaps':[r['id'] for r in rows if r['fact_present'] is False],'binding_only':sum(r['fact_present'] is None for r in rows),'changed_expectations':changed,'capture':json.loads((local/folder/'capture.stdout').read_text())}
items=[{'path':str(p.relative_to(dest)),'sha256':sha(p),'bytes':p.stat().st_size} for p in sorted(dest.rglob('*')) if p.is_file()]
result={'status':'typed_responses_and_conditional_di_accepted','classification':'source_authored_development_baseline_not_independent_solver_score','worker_version':'10','compiler_version':'4.11.0.0','new_rule':'csharp-framework-v5','independent_solver_score':{'completed':3,'total':12},'apps':apps,'validation':{'full_go_tests':'passed with four native fixtures enabled','vet':'passed','race':'passed semantic/semanticimport/semanticindex/mcp','new_native_fixture':{'positives':10,'negatives':11,'incomplete_suppression':True},'existing_native_scripts':'three passed','worker9_public_compatibility':'passed','worker6_through10_index_compatibility':'passed','worker_default_build':'zero warnings/errors','worker_pinned_build':'zero warnings/errors','previous_archive_bytes_unchanged':True,'corpus_tracked_sources_unchanged':True},'cli_sha256':sha(local/'moedex-final'),'files':items,'file_count':len(items),'total_bytes':sum(i['bytes'] for i in items)}
(dest/'result.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps({'files':len(items),'bytes':result['total_bytes'],'manifest_sha256':sha(dest/'result.json')},indent=2))
