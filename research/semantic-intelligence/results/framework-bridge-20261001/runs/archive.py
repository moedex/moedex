import base64
import datetime
import hashlib
import json
import shutil
import subprocess
from pathlib import Path

base=Path('research/semantic-intelligence/results/framework-bridge-20261001')
base.mkdir(exist_ok=False)
local=Path('.local/framework-bridge')
def digest(p):
 raw=p.read_bytes();return {'sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw)}
def copy(p,target):
 target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(p,target)
for p in sorted(local.iterdir()):
 if p.is_file() and p.suffix in ('.log','.json','.py','.stdout','.stderr'):
  copy(p,base/'runs'/p.name)
for directory in sorted(local.glob('public-*')):
 if directory.is_dir():
  for p in sorted(directory.rglob('*')):
   if p.is_file() and p.name!='lock':copy(p,base/'runs'/p.relative_to(local))
for pattern in ('fixture-*','legacy-fixture'):
 for directory in sorted(local.glob(pattern)):
  if directory.is_dir():
   for p in sorted(directory.iterdir()):
    if p.is_file():copy(p,base/'runs'/p.relative_to(local))
for directory in sorted(local.glob('eshop-*')):
 if directory.is_dir():
  for p in sorted(directory.iterdir()):
   if p.is_file() and p.suffix in ('.json','.stdout','.stderr'):copy(p,base/'runs'/p.relative_to(local))
for p in sorted((local/'worker-integration-v2').glob('*.jsonl')):
 copy(p,base/'runs'/p.relative_to(local))
sources=['internal/semantic/implementation.go','internal/semantic/framework_bridge_test.go','internal/semantic/domain.go','internal/semantic/domain_version.go','internal/semantic/domain_version_test.go','internal/semantic/domain_v4_test.go','internal/semantic/validate.go','internal/semanticimport/import.go','internal/semanticimport/revalidate.go','internal/semanticimport/revalidate_test.go','internal/semanticimport/framework_bridge_public_test.go','internal/semanticindex/validate.go','internal/semanticindex/domain.go','internal/semanticindex/implementation.go','internal/semanticindex/contract_paths.go','internal/semanticindex/contract_paths_test.go','internal/semanticindex/README.md','internal/mcp/compiler_implementation.go','internal/mcp/compiler_implementations.go','internal/app/servecmd/webhooks_holdout_public_test.go','research/semantic-intelligence/agent-journeys/check_framework_bridge.py','research/semantic-intelligence/agent-journeys/check_discovery_workflow.py','research/semantic-intelligence/agent-journeys/client.py','research/semantic-intelligence/check-project-capture.py','docs/adr/0047-closed-interface-and-keyed-registration-evidence.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md','research/semantic-intelligence/FRAMEWORK-BRIDGE-ACCEPTANCE.md']
sources += [str(p) for p in Path('tools/semantic-dotnet').iterdir() if p.is_file()]
for name in sources:copy(Path(name),base/'source'/(name+'.txt'))
# Every runtime worker source copy must equal the implementation delivered here.
worker_hashes={}
for p in Path('tools/semantic-dotnet').iterdir():
 if p.suffix in ('.cs','.csproj') or p.name=='NuGet.Config':
  assert p.read_bytes()==(local/'worker'/p.name).read_bytes(),p
  worker_hashes[str(p)]=digest(p)
prior=[]
for name in ['eshop-webhooks-holdout-20261001','implementation-discovery-20261001','file-symbol-discovery-20261001']:
 p=Path('research/semantic-intelligence/results')/name/'result.json';m=json.loads(p.read_text())
 for rel,expected in m.get('files',{}).items():
  actual=digest(p.parent/rel)
  assert actual==expected,(p,rel,actual,expected)
 prior.append({'path':str(p),**digest(p),'verified_inventory_entries':len(m.get('files',{}))})
public=json.loads((local/'public-verified/report.json').read_text())
for case in public:
 d=local/'public-verified'/case['case'];state=json.loads((d/'state.json').read_text());responses=list(d.glob('*.response.raw'))
 assert state['calls']==case['calls']==len(responses)
 assert state['response_bytes']==case['response_bytes']==sum(p.stat().st_size for p in responses)
 assert not state['stopped']
 for p in responses:
  wire=json.loads(p.read_text());assert 'error' not in wire and not wire['result'].get('isError')
release=local/'eshop-release';inputs=json.loads((release/'inputs.json').read_text())
assert inputs['source_unchanged'] and inputs['captured_contexts']==5 and inputs['reviewed_sources_present']==57
# Confirm the pinned tracked source and predecessor reviewed roster still match.
source=Path('.local/application-impact/independent/eShop')
assert subprocess.check_output(['git','-C',str(source),'rev-parse','HEAD'],text=True).strip()==inputs['commit']
assert not subprocess.check_output(['git','-C',str(source),'status','--porcelain','--untracked-files=no'])
for name,expected in inputs['source_sha256'].items():assert digest(source/name)['sha256']==expected,name
native=json.loads((local/'native-release-inventory.json').read_text())
assert digest(release/'project.semantic')['sha256']==native['artifact_sha256']==inputs['artifact_sha256']
worker=local/'worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll'
assert digest(worker)['sha256']==inputs['worker_sha256']
files={str(p.relative_to(base)):digest(p) for p in sorted(base.rglob('*')) if p.is_file()}
result={'finalized_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'classification':'tuned_development_successor_not_independent_holdout_or_agent_score','subagents_used':0,'worker_version':'7','index_version':5,'source_commit':inputs['commit'],'source_files_unchanged':len(inputs['source_sha256']),'reviewed_authored_sources':57,'native':native,'public_workflows':[{'case':r['case'],'calls':r['calls'],'response_bytes':r['response_bytes'],'passed':r['passed'],'compiler_identity':r['compiler_identity']} for r in public],'public_total_calls':sum(r['calls'] for r in public),'public_total_response_bytes':sum(r['response_bytes'] for r in public),'previous_supported_assertions':9,'previous_http_negative_controls':2,'selected_new_observations':7,'unscored_new_observations':1,'independent_agent_coverage':{'completed':3,'total':12},'validation':{'go_test_all':'passed','go_vet_all':'passed','go_build_all':'passed','race_packages':['semantic','semanticimport','semanticindex','mcp','app/servecmd'],'default_path_guard_race':'passed','python_client_tests':12,'native_framework_fixture':'passed','sdk10_worker_regression':'passed','old_worker6_public_reverse_regression':'passed','final_native_fixture_public_roundtrip':'passed'},'worker_source':worker_hashes,'retained_artifacts':{str(p):digest(p) for p in [release/'project.semantic',worker,local/'moedex-release',local/'moedex-verified',local/'moedex-final']},'toolchain':subprocess.check_output(['go','version'],text=True).strip(),'prior_archives':prior,'limitations':['Default interface evidence remains an open template; no automatic closed class dispatch or template path hop.','Keyed helper summary observes first-statement one-level configuration; no final container or broker activation proof.','New identities support only qualified nongeneric named arguments, up to eight.','Original five unsupported domain labels are preserved; new method evidence is reported separately.','No additional independent agent tasks, fresh heldout score, CodeGraph production comparison or large-corpus resource claim.'],'files':files}
(base/'result.json').write_text(json.dumps(result,indent=2)+'\n')
print('archived',len(files),'files',sum(v['bytes'] for v in files.values()),'bytes')
print('manifest',digest(base/'result.json'))
