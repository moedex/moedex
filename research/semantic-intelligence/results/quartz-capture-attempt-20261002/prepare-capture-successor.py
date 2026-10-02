import datetime,hashlib,json,subprocess
from pathlib import Path
base=Path('.local/holdout-quartz').resolve();ev=base/'evaluation';plan=json.loads((base/'packet/launch-plan.json').read_text());corpus=base/'corpus';corpus.mkdir(exist_ok=False)
subprocess.run(['git','clone','--local','--no-hardlinks',str(base/'source'),str(corpus/'Quartz')],check=True,capture_output=True)
subprocess.run(['git','-C',str(corpus/'Quartz'),'remote','set-url','origin','https://github.com/quartznet/quartznet.git'],check=True)
for f in json.loads((base/'packet/source-manifest.json').read_text())['files']:assert hashlib.sha256((corpus/'Quartz'/f['path']).read_bytes()).hexdigest()==f['sha256']
argv=plan['capture_argv'];argv[argv.index('--checkout')+1]=str(corpus/'Quartz');argv[argv.index('--workspace')+1]=str(base/'capture-02/workspace');argv[argv.index('--output')+1]=str(base/'capture-02/project.semantic')
plan.update(status='separate setup attempt after pre-worker checkout-name validation failure',setup_amendment={'time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'prior_attempt':str(base/'capture/command.json'),'prior_exit':1,'reason':'Public repo Quartz requires canonical checkout basename Quartz; original source directory was named source. Local canonical clone has all 2713 identical tracked hashes and same commit.','source_changes':False,'product_changes':False,'rubric_changes':False,'prior_failure_preserved':True,'product_queries':0})
(ev/'launch-plan-02.json').write_text(json.dumps(plan,indent=2)+'\n')
s=(ev/'capture.py').read_text().replace("out=base/'capture'","out=base/'capture-02'").replace("plan=json.loads((packet/'launch-plan.json').read_text())","plan=json.loads((ev/'launch-plan-02.json').read_text())")
(ev/'capture-02.py').write_text(s)
(ev/'capture-02-freeze.json').write_text(json.dumps({n:hashlib.sha256((ev/n).read_bytes()).hexdigest() for n in ('launch-plan-02.json','capture-02.py','source-gold-reviewed.json','protocol-reviewed.json')},indent=2)+'\n')
print('Canonical checkout verified; original attempt preserved; successor launch plan frozen.')
