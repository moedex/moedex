import hashlib,json,shutil,subprocess
from pathlib import Path
root=Path.cwd();local=root/'.local/routing-slip';dest=root/'research/semantic-intelligence/results/routing-slip-20261001';old=root/'research/semantic-intelligence/results/masstransit-scan-20261001'
def sha(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1048576),b''):h.update(b)
 return h.hexdigest()
def copy(p,t):
 t.parent.mkdir(parents=True,exist_ok=True);assert not t.exists(),t;shutil.copy2(p,t)
assert not dest.exists();dest.mkdir()
assert sha(old/'result.json')=='3d6431fd748302b8be9e3579a7f19430a5a99611c37de856b244e91b41059f03'
prior=json.loads((old/'result.json').read_text())
for f in prior['files']:assert sha(old/f['path'])==f['sha256'],f['path']
for p in sorted(local.glob('*.log')):copy(p,dest/'logs'/p.name)
for n in ['archive.py','native-checks.py','native-commands.json','capture-commands.json','validate.py','validation-commands.json','check-navigation.py','compat-command.json']:copy(local/n,dest/'runners'/n)
for name in ['ForkJoint','eShopOnWeb']:
 for p in (local/name).iterdir():
  if p.is_file() and p.suffix in ['.json','.stdout','.stderr']:copy(p,dest/'captures'/name/p.name)
for name in ['ForkJoint-public','eShopOnWeb-public','navigation']:
 for p in sorted((local/name).rglob('*')):
  if p.is_file():copy(p,dest/'public'/name/p.relative_to(local/name))
for name in ['native','forwarding','selection','bridge','response-di','request-response','endpoint','mediatr','generic-handler','open-di','mediatr-scan','masstransit-scan']:
 for p in sorted((local/name).rglob('*')):
  rel=p.relative_to(local/name)
  if p.is_file() and not {'bin','obj'}.intersection(rel.parts) and p.suffix in ['.cs','.csproj','.json','.jsonl','.stderr','.Config']:
   copy(p,dest/'native'/name/(str(rel)+('.txt' if p.suffix in ['.cs','.csproj'] else '')))
files=[f['path'].removeprefix('source/').removesuffix('.txt') for f in prior['files'] if f['path'].startswith('source/') and 'MASSTRANSIT-SCAN-ACCEPTANCE' not in f['path'] and '0059-' not in f['path']]
files+=['internal/semantic/routing_slip_test.go','internal/semanticimport/routing_slip_public_test.go','tools/semantic-dotnet/RoutingSlipFacts.cs','tools/semantic-dotnet/test_routing_slip.py','research/semantic-intelligence/ROUTING-SLIP-ACCEPTANCE.md','docs/adr/0060-bounded-routing-slip-configuration.md']
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
 changed=[r['id'] for r in rows if r['fact_present']!=previous[r['id']]];assert changed==(['routing-slip'] if name=='ForkJoint' else [])
 if name=='ForkJoint':
  row=next(r for r in rows if r['id']=='routing-slip');assert len(row['facts'])==1
  f=row['facts'][0];assert f['kind']=='routing_slip_activity_configuration' and f['rule']=='csharp-routing-slip-v1' and f['evidence_scope']=='compile_time'
  assert [t['role'] for t in f['targets']]==['activity','arguments','address_field','formatter_api']
  assert [t['symbol']['descriptor'] for t in f['targets']]==['T:ForkJoint.Api.Components.Activities.GrillBurgerActivity','T:ForkJoint.Api.Components.Activities.GrillBurgerArguments','F:ForkJoint.Api.Components.ItineraryPlanners.BurgerItineraryPlanner._grillAddress','M:MassTransit.IEndpointNameFormatter.ExecuteActivity``2']
  assert row['bindings'][0]['byte_offset']==1003 and row['bindings'][0]['raw_sha256']=='d1f3dd05432cf43ddcb94d8bf80c5a3f26d8af116da46a5308a9bbeeac4b6494'

 apps[name]={'artifact_sha256':inp['artifact_sha256'],'source_unchanged':True,'commit':inp['commit'],'gold_sha256':inp['gold_sha256'],'anchors_resolved':len(rows),'facts_present':sum(r['fact_present'] is True for r in rows),'fact_gaps':[r['id'] for r in rows if r['fact_present'] is False],'binding_only':sum(r['fact_present'] is None for r in rows),'changed_expectations':changed,'capture':json.loads((local/name/'capture.stdout').read_text())}
for commands in ['validation-commands.json','native-commands.json','capture-commands.json']:assert all(c['returncode']==0 for c in json.loads((local/commands).read_text()))
assert all(c['passed'] for c in json.loads((local/'navigation/report.json').read_text())['calls'])
assert json.loads((local/'compat-command.json').read_text())['returncode']==0
for name in ['worker-build-final.log','worker-default-build-final.log']:
 log=(local/name).read_text();assert '0 Warning(s)' in log and '0 Error(s)' in log
items=[{'path':str(p.relative_to(dest)),'sha256':sha(p),'bytes':p.stat().st_size} for p in sorted(dest.rglob('*')) if p.is_file()]
result={'status':'bounded_routing_slip_configuration_accepted','classification':'source_authored_development_baseline_not_independent_solver_score','worker_version':'18','compiler_version':'4.11.0.0','new_rule':'csharp-routing-slip-v1','new_kind':'routing_slip_activity_configuration','new_symbol_descriptor_kind':None,'runtime_execution_inferred':False,'resolved_uri_inferred':False,'constructor_bounds':{'max_statements':32,'max_nodes':1024,'max_field_references':1,'instance_constructors':1},'independent_solver_score':{'completed':3,'total':12},'apps':apps,'validation':{'full_go_tests':'passed with twelve native fixtures enabled','vet':'passed','race':'passed with twelve native fixtures enabled for semantic/semanticimport/semanticindex/mcp','new_native_fixture':{'positives':5,'negatives':19,'incomplete_suppression':True},'existing_native_scripts':'eleven passed','worker17_public_compatibility':'passed','worker6_through18_index_compatibility':'passed','worker_default_build':'zero warnings/errors','worker_pinned_build':'zero warnings/errors','public_navigation':'routing targets, source definitions, reverse context and distinct sibling activity verified','previous_archive_bytes_unchanged':True,'corpus_tracked_sources_unchanged':True},'cli_sha256':sha(local/'moedex'),'files':items,'file_count':len(items),'total_bytes':sum(i['bytes'] for i in items)}
(dest/'result.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps({'files':len(items),'bytes':result['total_bytes'],'manifest_sha256':sha(dest/'result.json')},indent=2))
