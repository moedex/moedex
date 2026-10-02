import hashlib,json,shutil
from pathlib import Path
root=Path('.local/holdout-cleanarchitecture');ready=root/'readiness';dest=Path('research/semantic-intelligence/results/holdout-environment-20261002');dest.mkdir(exist_ok=False)
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def copy(p,target):
 d=dest/target;d.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(p,d)
for p in sorted(ready.iterdir()):
 if p.is_file():copy(p,'validation/'+p.name)
copy(root/'bundle/manifest.json','validation/dependency-manifest.json')
for p in sorted((root/'sdk-probe').glob('*.cs*')):copy(p,'validation/synthetic-fixture/'+p.name)
for name in ['research/semantic-intelligence/HOLDOUT-CLEANARCHITECTURE-ENVIRONMENT.md','research/semantic-intelligence/HOLDOUT-CLEANARCHITECTURE-READINESS.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md']:
 copy(Path(name),'source/'+name+'.txt')
prior={'holdout-cleanarchitecture-20261002':'a8210ebaed72e218f2f848e080e01179190a0942b3dae0e4dd25f18e31a8a878','bounded-views-20261002':'4535a5d90cc47868cf55209b548939d2ce4a2b7e01d3d979f902a3f43369dc85','agent-journeys-worker18-20261001':'59103873e0233cc7a115ff879954cc217a9939a0d151f006f1dd4c219ce4392c'}
for name,digest in prior.items():assert sha(Path('research/semantic-intelligence/results')/name/'result.json')==digest
inputs=json.loads((ready/'launch-plan.json').read_text())
for path,digest in inputs['frozen_inputs'].items():assert sha(Path(path))==digest
files=[{'path':str(p.relative_to(dest)),'bytes':p.stat().st_size,'sha256':sha(p)} for p in sorted(dest.rglob('*')) if p.is_file()]
report={'classification':'Environment preparation only; source oracle not independently reviewed; no holdout product run','source_commit':inputs['source_commit'],'sdk':'10.0.401','worker':'18','compiler':'5.9.0.0','projects':6,'source_files_unchanged':258,'oracle_files_in_msbuild_inputs':17,'build_errors':0,'build_warnings':9,'offline_restore_passed':True,'synthetic_worker_smoke_passed':True,'holdout_extraction_started':False,'holdout_product_queries':0,'agents_launched':0,'paired_arm_ready':False,'prior_archive_hashes':prior,'files':files}
(dest/'result.json').write_text(json.dumps(report,indent=2)+'\n')
for f in files:assert sha(dest/f['path'])==f['sha256']
print(json.dumps({'manifest':str(dest/'result.json'),'sha256':sha(dest/'result.json'),'files':len(files),'bytes':sum(f['bytes'] for f in files)},indent=2))
