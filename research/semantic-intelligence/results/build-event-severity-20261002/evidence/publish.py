import base64,json,subprocess,time
from pathlib import Path
r=Path('.local/build-event-severity').resolve();capture=r/'accepted-capture';summary=json.loads((capture/'capture.stdout').read_text());artifact=json.loads(base64.b64decode(json.loads((capture/'project.semantic').read_text())['payload']))
roots={s['id']:summary['workspace'] for s in artifact['snapshots']};(r/'roots.json').write_text(json.dumps(roots,indent=2)+'\n')
corpus=r/'corpus';corpus.mkdir(exist_ok=False)
subprocess.run(['git','clone','--no-hardlinks',str(Path('.local/holdout-cleanarchitecture/capture-input/CleanArchitecture').resolve()),str(corpus/'CleanArchitecture')],check=True,capture_output=True)
subprocess.run(['git','-C',str(corpus/'CleanArchitecture'),'remote','set-url','origin','https://github.com/jasontaylordev/CleanArchitecture.git'],check=True)
a=[str(r/'moedex'),'index','snapshot','build','--corpus',str(corpus),'--index-dir',str(r/'index'),'--id','cleanarchitecture-worker20','--graph=false','--semantic-artifact',str(capture/'project.semantic'),'--semantic-workspaces',str(r/'roots.json')]
start=time.monotonic()
with (r/'publish.stdout').open('wb') as stdout,(r/'publish.stderr').open('wb') as stderr:p=subprocess.run(a,stdout=stdout,stderr=stderr,timeout=120)
(r/'publication.json').write_text(json.dumps({'classification':'isolated development snapshot; no production CURRENT replaced','argv':a,'returncode':p.returncode,'seconds':time.monotonic()-start},indent=2)+'\n');p.check_returncode();print('Compiler artifact attached to isolated snapshot')
