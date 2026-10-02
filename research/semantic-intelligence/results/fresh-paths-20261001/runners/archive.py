import hashlib,json,shutil
from pathlib import Path
root=Path.cwd();local=root/'.local/fresh-paths';dest=root/'research/semantic-intelligence/results/fresh-paths-20261001'
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def copy(src,target):
 target.parent.mkdir(parents=True,exist_ok=True);assert not target.exists(),target;shutil.copy2(src,target)
for path in sorted(local.glob('*.log')):copy(path,dest/'logs'/path.name)
for name in ['provision-commands.json','eShopOnWeb-packages.json','Sample-ForkJoint-packages.json','Sample-ForkJoint-offline-candidates.json','worker6-compat.json']:
 copy(local/name,dest/'provisioning'/name)
for name in ['freeze.py','provision.py','split.py','archive.py']:
 copy(local/name,dest/'runners'/name)
copy(local/'dotnet8-only/dotnet-trace',dest/'runners/dotnet-trace.txt')
for folder in sorted(local.glob('*capture*')):
 if not folder.is_dir():continue
 for path in sorted(folder.iterdir()):
  if path.is_file() and path.suffix in ['.json','.stderr','.stdout']:copy(path,dest/'captures'/folder.name/path.name)
for folder in [local/'ForkJoint-public-v1',local/'compact-public-v1',local/'compact-public-v2',local/'compact-public-v3']:
 for path in sorted(folder.rglob('*')):
  if path.is_file():copy(path,dest/'public'/folder.name/path.relative_to(folder))
for name in ['eShopOnWeb-diagnostic.jsonl','Sample-ForkJoint-diagnostic.jsonl','Sample-ForkJoint-managed-diagnostic.jsonl']:
 rows=[json.loads(line) for line in (local/name).read_text().splitlines()]
 evidence=[r for r in rows if r['record_type'] in ['diagnostic','summary','stream_summary']]
 target=dest/'diagnostics'/name;target.parent.mkdir(exist_ok=True);target.write_text(''.join(json.dumps(r)+'\n' for r in evidence))
for name in ['ForkJoint-complete-bundle','eShopOnWeb-bundle','Sample-ForkJoint-bundle','Sample-ForkJoint-bundle-v2']:
 copy(local/name/'manifest.json',dest/'dependencies'/(name+'-manifest.json'))
files=['internal/semanticindex/evidence_path.go','internal/semanticindex/evidence_path_test.go','internal/mcp/compiler.go','internal/mcp/compiler_contract_test.go','internal/mcp/compiler_evidence_path.go','internal/mcp/compiler_evidence_path_test.go','internal/semanticimport/default_forwarding_public_test.go','research/semantic-intelligence/agent-journeys/check_compact_evidence_path.py','research/semantic-intelligence/agent-journeys/check_fresh_paths.py','research/semantic-intelligence/check-project-capture.py','research/semantic-intelligence/COMPACT-EVIDENCE-ACCEPTANCE.md','docs/adr/0050-compact-class-evidence-path.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md']
for name in files:copy(root/name,dest/'source'/(name+'.txt'))
freeze=json.loads((dest/'pre-capture-freeze.json').read_text())
for name,h in freeze['frozen_tools'].items():assert sha(root/name)==h,name
for repo,v in freeze['apps'].items():assert sha(dest/(repo+'-source-gold.json'))==v['gold_sha256']
items=[{'path':str(p.relative_to(dest)),'sha256':sha(p),'bytes':p.stat().st_size} for p in sorted(dest.rglob('*')) if p.is_file() and p.name!='result.json']
result={'status':'compact_query_accepted_fresh_baseline_mixed','worker_version_unchanged':'9','independent_solver_tasks_completed':3,'independent_solver_tasks_total':12,'fresh_baseline':{'ForkJoint':{'published':True,'source_anchors_resolved':11,'source_anchors_total':11,'higher_level_facts_recorded':4,'higher_level_fact_gaps':7,'artifact_sha256':'5b50a58a0da90254f877d51c7351928558cab3c0ac0d5180d8180a376dfb1584'},'eShopOnWeb':{'published':False,'public_anchors_unmeasured':10,'complete_dependency_cache_bytes':1166409179,'dependency_bundle_cap_bytes':1073741824,'additional_diagnostic':'Razor generated component types unresolved in isolated provisioning copy'}},'paired_compact':{'cases':3,'evidence_records_per_case':5,'legacy_calls':15,'compact_calls':3,'legacy_response_bytes':60040,'compact_response_bytes':39422,'byte_reduction_percent':34.34,'discovery_included':False,'scope_controls_passed':True},'validation':{'full_go_tests':'passed with native forwarding enabled','vet':'passed','race':'passed for semanticindex/mcp/semanticimport/app-servecmd','worker6_7_8_compatibility':'passed','frozen_inputs_unchanged':True},'new_binary_sha256':sha(local/'moedex-final'),'files':items,'file_count':len(items),'total_bytes':sum(p['bytes'] for p in items)}
(dest/'result.json').write_text(json.dumps(result,indent=2)+'\n');print(result['file_count'],result['total_bytes'],sha(dest/'result.json'))
