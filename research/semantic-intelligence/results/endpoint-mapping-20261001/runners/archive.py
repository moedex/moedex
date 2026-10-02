import hashlib,json,shutil,subprocess
from pathlib import Path
root=Path.cwd();local=root/'.local/endpoint-mapping';dest=root/'research/semantic-intelligence/results/endpoint-mapping-20261001';old=root/'research/semantic-intelligence/results/request-response-20261001'
def sha(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1048576),b''):h.update(b)
 return h.hexdigest()
def copy(p,t):
 t.parent.mkdir(parents=True,exist_ok=True);assert not t.exists(),t;shutil.copy2(p,t)
assert not dest.exists();dest.mkdir()
assert sha(old/'result.json')=='d3b7b803aaa772654e7b1d8d8317fa00f04d015e603c15489c562212c6c5cb27'
prior=json.loads((old/'result.json').read_text())
for f in prior['files']:assert sha(old/f['path'])==f['sha256'],f['path']
for p in sorted(local.glob('*.log')):copy(p,dest/'logs'/p.name)
for n in ['archive.py','native-checks.py','native-commands.json','validate.py','validation-commands.json','check-route-navigation.py']:copy(local/n,dest/'runners'/n)
for name in ['ForkJoint','eShopOnWeb']:
 for p in (local/name).iterdir():
  if p.is_file() and p.suffix in ['.json','.stdout','.stderr']:copy(p,dest/'captures'/name/p.name)
for name in ['ForkJoint-public','eShopOnWeb-public','route-navigation']:
 for p in sorted((local/name).rglob('*')):
  if p.is_file():copy(p,dest/'public'/name/p.relative_to(local/name))
for name in ['native','native-v2','forwarding','selection','bridge','response-di','request-response']:
 for p in sorted((local/name).rglob('*')):
  rel=p.relative_to(local/name)
  if p.is_file() and not {'bin','obj'}.intersection(rel.parts) and p.suffix in ['.cs','.csproj','.json','.jsonl','.stderr','.Config']:
   copy(p,dest/'native'/name/(str(rel)+('.txt' if p.suffix in ['.cs','.csproj'] else '')))
files=[f['path'].removeprefix('source/').removesuffix('.txt') for f in prior['files'] if f['path'].startswith('source/') and 'REQUEST-RESPONSE-ACCEPTANCE' not in f['path'] and '0053-' not in f['path']]
files+=['internal/semantic/endpoint_test.go','internal/semanticimport/endpoint_public_test.go','internal/semanticimport/import.go','internal/semanticimport/wire.go','internal/mcp/compiler_contract_context.go','internal/mcp/compiler_evidence_path.go','tools/semantic-dotnet/EndpointFacts.cs','tools/semantic-dotnet/test_endpoint_mapping.py','research/semantic-intelligence/agent-journeys/client.py','research/semantic-intelligence/ENDPOINT-MAPPING-ACCEPTANCE.md','docs/adr/0054-source-static-post-endpoint-mapping.md']
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
 changed=[r['id'] for r in rows if r['fact_present']!=previous[r['id']]];assert changed==(['route-post'] if name=='ForkJoint' else [])
 if name=='ForkJoint':
  route=next(r for r in rows if r['id']=='route-post');assert len(route['facts'])==1
  f=route['facts'][0];assert f['kind']=='endpoint_post_configuration' and f['rule']=='csharp-endpoint-v1' and f['evidence_scope']=='compile_time' and f['route_pattern']=='/order'
  assert len(f['targets'])==1 and f['targets'][0]['role']=='handler'
  assert f['targets'][0]['symbol']['descriptor']=='M:ForkJoint.Api.OrderRoutes.SubmitOrder(ForkJoint.Api.OrderRoutes.Order,MassTransit.IRequestClient{ForkJoint.Contracts.SubmitOrder},System.Threading.CancellationToken)'
  request=next(r for r in rows if r['id']=='request-response')['bindings'][0];assert request['enclosing_symbol_id']==f['targets'][0]['symbol']['id'] and request['context_id']==route['bindings'][0]['context_id']
 apps[name]={'artifact_sha256':inp['artifact_sha256'],'source_unchanged':True,'commit':inp['commit'],'gold_sha256':inp['gold_sha256'],'anchors_resolved':len(rows),'facts_present':sum(r['fact_present'] is True for r in rows),'fact_gaps':[r['id'] for r in rows if r['fact_present'] is False],'binding_only':sum(r['fact_present'] is None for r in rows),'changed_expectations':changed,'capture':json.loads((local/name/'capture.stdout').read_text())}
items=[{'path':str(p.relative_to(dest)),'sha256':sha(p),'bytes':p.stat().st_size} for p in sorted(dest.rglob('*')) if p.is_file()]
result={'status':'source_static_post_endpoint_mapping_accepted','classification':'source_authored_development_baseline_not_independent_solver_score','worker_version':'12','compiler_version':'4.11.0.0','new_rule':'csharp-endpoint-v1','independent_solver_score':{'completed':3,'total':12},'apps':apps,'validation':{'full_go_tests':'passed with six native fixtures enabled','vet':'passed','race':'passed with six native fixtures enabled for semantic/semanticimport/semanticindex/mcp','new_native_fixture':{'positives':7,'negatives':15,'incomplete_suppression':True},'existing_native_scripts':'five passed','worker11_public_compatibility':'passed','worker6_through12_index_compatibility':'passed','worker_default_build':'zero warnings/errors','worker_pinned_build':'zero warnings/errors','public_handler_navigation':'exact definition and request enclosing method/context verified','previous_archive_bytes_unchanged':True,'corpus_tracked_sources_unchanged':True},'cli_sha256':sha(local/'moedex'),'files':items,'file_count':len(items),'total_bytes':sum(i['bytes'] for i in items)}
(dest/'result.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps({'files':len(items),'bytes':result['total_bytes'],'manifest_sha256':sha(dest/'result.json')},indent=2))
