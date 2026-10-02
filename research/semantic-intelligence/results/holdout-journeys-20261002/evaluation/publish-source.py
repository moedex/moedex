import datetime,hashlib,json,subprocess,time
from pathlib import Path
r=Path('.local/holdout-cleanarchitecture').resolve();out=r/'evaluation';plan=json.loads((r/'readiness/launch-plan.json').read_text());moe=plan['capture_argv'][0]
corpus=r/'source-only/corpus';corpus.mkdir(parents=True,exist_ok=False)
subprocess.run(['git','clone','--no-hardlinks',str(r/'capture-input/CleanArchitecture'),str(corpus/'CleanArchitecture')],check=True)
args=[moe,'index','snapshot','build','--corpus',str(corpus),'--index-dir',str(r/'source-only/index'),'--id','holdout-cleanarchitecture','--graph=false']
protocol=json.loads((out/'protocol-reviewed.json').read_text());protocol['version']=3;protocol['status']='source-only fallback arm frozen before first solver; compiler-enabled arm setup failed';protocol['arm']='Moedex native source-only; graph=false; embed=none; no semantic artifact';protocol['capture_status']='Frozen compiler capture failed on workspace diagnostics containing pinned upstream TFM compatibility warnings. No source or detector changes and no warning suppression. Source-only arm uses identical commit, six prompts, thirty reviewed atoms and budgets.'
(out/'protocol-executed.json').write_text(json.dumps(protocol,indent=2)+'\n')
start=time.monotonic()
with (out/'publish.stdout').open('w') as stdout,(out/'publish.stderr').open('w') as stderr:
 p=subprocess.run(args,stdout=stdout,stderr=stderr,timeout=180)
(out/'publication.json').write_text(json.dumps({'argv':args,'returncode':p.returncode,'seconds':time.monotonic()-start,'started_after_oracle_freeze':True,'arm':protocol['arm'],'protocol_sha256':hashlib.sha256((out/'protocol-executed.json').read_bytes()).hexdigest()},indent=2)+'\n');p.check_returncode();print('source-only publication ready')
