import hashlib,json,shutil,subprocess
from pathlib import Path
root=Path.cwd();local=root/'.local/mediatr-dispatch';dest=root/'research/semantic-intelligence/results/mediatr-dispatch-20261001';old=root/'research/semantic-intelligence/results/endpoint-mapping-20261001'
def sha(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1048576),b''):h.update(b)
 return h.hexdigest()
def copy(p,t):
 t.parent.mkdir(parents=True,exist_ok=True);assert not t.exists(),t;shutil.copy2(p,t)
assert not dest.exists();dest.mkdir()
assert sha(old/'result.json')=='b58270beee45e0a604cbea891b4d2a090dd520002ed42b6ebb5584c4e0b5ab11'
prior=json.loads((old/'result.json').read_text())
for f in prior['files']:assert sha(old/f['path'])==f['sha256'],f['path']
for p in sorted(local.glob('*.log')):copy(p,dest/'logs'/p.name)
for n in ['archive.py','native-checks.py','native-commands.json','validate.py','validation-commands.json','check-navigation.py']:copy(local/n,dest/'runners'/n)
for name in ['ForkJoint','eShopOnWeb']:
 for p in (local/name).iterdir():
  if p.is_file() and p.suffix in ['.json','.stdout','.stderr']:copy(p,dest/'captures'/name/p.name)
for name in ['ForkJoint-public','eShopOnWeb-public','navigation']:
 for p in sorted((local/name).rglob('*')):
  if p.is_file():copy(p,dest/'public'/name/p.relative_to(local/name))
for name in ['native','forwarding','selection','bridge','response-di','request-response','endpoint']:
 for p in sorted((local/name).rglob('*')):
  rel=p.relative_to(local/name)
  if p.is_file() and not {'bin','obj'}.intersection(rel.parts) and p.suffix in ['.cs','.csproj','.json','.jsonl','.stderr','.Config']:
   copy(p,dest/'native'/name/(str(rel)+('.txt' if p.suffix in ['.cs','.csproj'] else '')))
files=[f['path'].removeprefix('source/').removesuffix('.txt') for f in prior['files'] if f['path'].startswith('source/') and 'ENDPOINT-MAPPING-ACCEPTANCE' not in f['path'] and '0054-' not in f['path']]
files+=['internal/semantic/constructed_type.go','internal/semantic/mediatr_test.go','internal/semanticimport/mediatr_public_test.go','internal/semanticindex/validate.go','tools/semantic-dotnet/MediatorFacts.cs','tools/semantic-dotnet/test_mediatr_dispatch.py','research/semantic-intelligence/MEDIATR-DISPATCH-ACCEPTANCE.md','docs/adr/0055-direct-mediatr-dispatch.md']
for name in sorted(set(files)):copy(root/name,dest/'source'/(name+'.txt'))
for p in (root/'tools/semantic-dotnet').glob('*.cs'):assert sha(p)==sha(local/'worker'/p.name),p
copy(local/'worker/obj/project.assets.json',dest/'worker/project.assets.json')
worker=local/'worker/bin/Debug/net10.0'
manifest=[{'path':str(p.relative_to(worker)),'sha256':sha(p),'bytes':p.stat().st_size} for p in sorted(worker.rglob('*')) if p.is_file()]
(dest/'worker/files.json').write_text(json.dumps(manifest,indent=2)+'\n')
apps={}
for name,repo in [('ForkJoint','Sample-ForkJoint'),('eShopOnWeb','eShopOnWeb')]:
 inp=json.loads((local/name/'inputs.json').read_text());assert inp['source_unchanged'];assert sha(local/name/'project.semantic')==inp['artifact_sha256']
 gold=root/'research/semantic-intelligence/results/fresh-paths-20261001'/(repo+'-source-gold.json');assert sha(gold)==inp['gold_sha256'];copy(gold,dest/'gold'/gold.name)
 assert not subprocess.check_output(['git','status','--porcelain','--untracked-files=no'],cwd=root/'.local/fresh-paths/corpus'/repo,text=True)
 report=json.loads((local/(name+'-public')/'report.json').read_text());rows=report['expectations'];assert all(r['binding_resolved'] for r in rows)
 previous={r['id']:r['fact_present'] for r in json.loads((old/'public'/(name+'-public')/'report.json').read_text())['expectations']}
 changed=[r['id'] for r in rows if r['fact_present']!=previous[r['id']]];assert changed==(['mediator-orders','mediator-details'] if name=='eShopOnWeb' else [])
 if name=='eShopOnWeb':
  for label,request in [('mediator-orders','MyOrders.GetMyOrders'),('mediator-details','OrderDetails.GetOrderDetails')]:
   facts=next(r for r in rows if r['id']==label)['facts'];assert len(facts)==1
   f=facts[0];assert f['kind']=='mediator_send_configuration' and f['rule']=='csharp-mediatr-v1' and f['evidence_scope']=='compile_time'
   assert [t['role'] for t in f['targets']]==['request','response'];assert f['targets'][0]['symbol']['descriptor']=='T:Microsoft.eShopWeb.Web.Features.'+request
   response=f['targets'][1]['symbol']
   if label=='mediator-orders':
    assert response['descriptor_kind']=='constructed_named_type_v1';value=json.loads(response['descriptor']);assert value['definition']=='T:System.Collections.Generic.IEnumerable`1' and len(value['arguments'])==1
    arg=value['arguments'][0];assert arg['descriptor']=='T:Microsoft.eShopWeb.Web.ViewModels.OrderViewModel' and arg['namespace_kind']=='project' and arg['namespace']=='eShopOnWeb/src/Web/Web.csproj'
   else:assert response['descriptor_kind']=='documentation_comment_id' and response['descriptor']=='T:Microsoft.eShopWeb.Web.ViewModels.OrderDetailViewModel'
  assert next(r for r in rows if r['id']=='orders-handler')['fact_present'] is False
 apps[name]={'artifact_sha256':inp['artifact_sha256'],'source_unchanged':True,'commit':inp['commit'],'gold_sha256':inp['gold_sha256'],'anchors_resolved':len(rows),'facts_present':sum(r['fact_present'] is True for r in rows),'fact_gaps':[r['id'] for r in rows if r['fact_present'] is False],'binding_only':sum(r['fact_present'] is None for r in rows),'changed_expectations':changed,'capture':json.loads((local/name/'capture.stdout').read_text())}
items=[{'path':str(p.relative_to(dest)),'sha256':sha(p),'bytes':p.stat().st_size} for p in sorted(dest.rglob('*')) if p.is_file()]
result={'status':'direct_mediatr_dispatch_accepted','classification':'source_authored_development_baseline_not_independent_solver_score','worker_version':'13','compiler_version':'4.11.0.0','new_rule':'csharp-mediatr-v1','new_symbol_descriptor_kind':'constructed_named_type_v1','handler_selection_inferred':False,'independent_solver_score':{'completed':3,'total':12},'apps':apps,'validation':{'full_go_tests':'passed with seven native fixtures enabled','vet':'passed','race':'passed with seven native fixtures enabled for semantic/semanticimport/semanticindex/mcp','new_native_fixture':{'positives':7,'negatives':16,'incomplete_suppression':True},'existing_native_scripts':'six passed','worker12_public_compatibility':'passed','worker6_through13_index_compatibility':'passed','worker_default_build':'zero warnings/errors','worker_pinned_build':'zero warnings/errors','public_navigation':'both request definitions and exact response context identities verified','previous_archive_bytes_unchanged':True,'corpus_tracked_sources_unchanged':True},'cli_sha256':sha(local/'moedex'),'files':items,'file_count':len(items),'total_bytes':sum(i['bytes'] for i in items)}
(dest/'result.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps({'files':len(items),'bytes':result['total_bytes'],'manifest_sha256':sha(dest/'result.json')},indent=2))
