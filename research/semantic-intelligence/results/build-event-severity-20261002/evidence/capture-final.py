import base64,hashlib,json,os,subprocess,time
from pathlib import Path
r=Path('.local/build-event-severity').resolve();out=r/'managed-capture-final';out.mkdir(exist_ok=False)
a=json.loads(Path('.local/holdout-cleanarchitecture/readiness/launch-plan.json').read_text())['capture_argv'];a[0]=str(r/'moedex')
for flag,value in [('--worker',r/'regression-bounded/worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll'),('--workspace',out/'workspace'),('--output',out/'project.semantic')]:a[a.index(flag)+1]=str(value)
record={'classification':'development-corpus capture after severity fix; not a fresh heldout score','argv':a};(out/'command.json').write_text(json.dumps(record,indent=2)+'\n')
start=time.monotonic()
with (out/'capture.stdout').open('wb') as stdout,(out/'capture.stderr').open('wb') as stderr:
 p=subprocess.run(a,env=dict(os.environ,DOTNET_PROCESSOR_COUNT='2',DOTNET_CLI_TELEMETRY_OPTOUT='1'),stdout=stdout,stderr=stderr,timeout=500)
record.update(returncode=p.returncode,seconds=time.monotonic()-start);(out/'command.json').write_text(json.dumps(record,indent=2)+'\n');p.check_returncode()
summary=json.loads((out/'capture.stdout').read_text());assert summary['complete']
artifact=json.loads(base64.b64decode(json.loads((out/'project.semantic').read_text())['payload']))
assert len(artifact['contexts'])==6
for c in artifact['contexts']:
 assert c['status']=='complete' and c['extractor_version']=='20'
 proof=json.loads(c['capture'])['build_diagnostics'];assert proof['verified'] and proof['matched_warnings']==15 and not proof['errors']
report={'classification':record['classification'],'complete':True,'contexts':6,'sources':len(artifact['sources']),'artifact_sha256':hashlib.sha256((out/'project.semantic').read_bytes()).hexdigest(),'summary':summary}
(out/'report.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
