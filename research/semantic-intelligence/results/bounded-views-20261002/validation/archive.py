import hashlib,json,shutil
from pathlib import Path
base=Path('research/semantic-intelligence/results')
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def finish(out,classification):
 files=[{'path':str(p.relative_to(out)),'bytes':p.stat().st_size,'sha256':sha(p)} for p in sorted(out.rglob('*')) if p.is_file() and p.name!='result.json']
 (out/'result.json').write_text(json.dumps({'classification':classification,'files':files},indent=2)+'\n')
 for f in files:assert sha(out/f['path'])==f['sha256']
 print(out/'result.json',sha(out/'result.json'),len(files))
def copy(out,source,target):
 dest=out/target;dest.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(source,dest)
out=base/'bounded-views-20261002';out.mkdir(exist_ok=False)
for name in ['check.py','replay.json','live.py','live-report.json','live-views.json','tests.log','archive.py']:
 copy(out,Path('.local/bounded-views')/name,'validation/'+name)
for p in Path('.local/bounded-views/live-run').iterdir():
 if p.is_file() and p.name!='lock':copy(out,p,'validation/live-run/'+p.name)
for source in ['research/semantic-intelligence/agent-journeys/browse.py','research/semantic-intelligence/agent-journeys/test_browse.py','research/semantic-intelligence/agent-journeys/client.py','research/semantic-intelligence/agent-journeys/README.md','research/semantic-intelligence/BOUNDED-VIEWS-ACCEPTANCE.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md']:
 copy(out,source,'source/'+source+'.txt')
finish(out,'Scripted bounded presentation and accounting regression; not independent agent quality')
out=base/'holdout-cleanarchitecture-20261002'
copy(out,'.local/holdout-cleanarchitecture/prepare.py','prepare.py')
copy(out,'research/semantic-intelligence/HOLDOUT-CLEANARCHITECTURE-READINESS.md','readiness.md')
finish(out,'Source-only holdout preparation; coordinator-authored oracle pending independent review; no product evaluation')
