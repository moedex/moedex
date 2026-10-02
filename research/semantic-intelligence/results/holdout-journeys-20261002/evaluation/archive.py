import hashlib,json,shutil
from pathlib import Path
root=Path('.local/holdout-cleanarchitecture');base=root/'evaluation';dest=Path('research/semantic-intelligence/results/holdout-journeys-20261002');dest.mkdir(exist_ok=False)
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
def copy(p,target):
 d=dest/target;d.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(p,d)
for p in sorted(base.rglob('*')):
 if p.is_file() and p.name not in ['lock','view.lock'] and '__pycache__' not in p.parts:copy(p,'evaluation/'+str(p.relative_to(base)))
copy(root/'oracle-review.json','oracle/independent-source-review.json')
initial=Path('research/semantic-intelligence/results/holdout-cleanarchitecture-20261002')
for name in ['source-gold.json','protocol.json','source-manifest.json']:copy(initial/name,'oracle/original/'+name)
for name in ['commands.json','capture.stdout','capture.stderr']:copy(root/'capture'/name,'capture-failure/'+name)
copy(root/'readiness/launch-plan.json','capture-failure/launch-plan.json')
for name in ['research/semantic-intelligence/agent-journeys/client.py','research/semantic-intelligence/agent-journeys/browse.py','research/semantic-intelligence/HOLDOUT-CLEANARCHITECTURE-ACCEPTANCE.md','research/semantic-intelligence/HOLDOUT-CLEANARCHITECTURE-READINESS.md','research/semantic-intelligence/HOLDOUT-CLEANARCHITECTURE-ENVIRONMENT.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md']:
 copy(Path(name),'source/'+name+'.txt')
prior={'holdout-cleanarchitecture-20261002':'a8210ebaed72e218f2f848e080e01179190a0942b3dae0e4dd25f18e31a8a878','holdout-environment-20261002':'9329d633a952c2e715424e9b18c34ad9915190bd6cfe1f539ed2cff4511b018e','bounded-views-20261002':'4535a5d90cc47868cf55209b548939d2ce4a2b7e01d3d979f902a3f43369dc85','agent-journeys-worker18-20261001':'59103873e0233cc7a115ff879954cc217a9939a0d151f006f1dd4c219ce4392c'}
for name,digest in prior.items():assert sha(Path('research/semantic-intelligence/results')/name/'result.json')==digest
for p,d in json.loads((base/'pre-capture-freeze.json').read_text()).items():assert sha(base/p)==d
final=json.loads((base/'adjudicated-results.json').read_text())
files=[{'path':str(p.relative_to(dest)),'bytes':p.stat().st_size,'sha256':sha(p)} for p in sorted(dest.rglob('*')) if p.is_file()]
report={'classification':final['classification'],'source_commit':'5353a9edae000d576eade1a4f2c0d72d3b1c1785','assigned':final['assigned'],'conditional_evaluable':final['conditional_evaluable'],'material_unsupported_claims':0,'compiler_arm_setup':'failed; no compiler artifact published','transport_blocked_task':'locate-title-validation','paired_codegraph_executed':False,'attempted_calls':88,'observed_response_bytes':409123,'complete_transport_accounting_for_all_tasks':False,'displayed_views':116,'source_windows_verified':148,'adjudicated_disagreements':1,'agents':{'oracle_reviewers':1,'solvers':6,'answer_reviewers':6,'max_concurrent_children':1},'server_stopped':True,'prior_archive_hashes':prior,'files':files}
(dest/'result.json').write_text(json.dumps(report,indent=2)+'\n')
for f in files:assert sha(dest/f['path'])==f['sha256']
print(json.dumps({'manifest':str(dest/'result.json'),'sha256':sha(dest/'result.json'),'files':len(files),'bytes':sum(f['bytes'] for f in files)},indent=2))
