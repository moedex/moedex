import base64,datetime,hashlib,json,os,subprocess,time
from pathlib import Path
r=Path('.local/holdout-cleanarchitecture').resolve();ev=r/'evaluation';out=r/'capture';out.mkdir(exist_ok=False)
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
plan=json.loads((r/'readiness/launch-plan.json').read_text())
for p,d in plan['frozen_inputs'].items():assert sha(Path(p))==d
for p,d in json.loads((ev/'pre-capture-freeze.json').read_text()).items():assert sha(ev/p)==d
record={'started_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'gold_sha256':sha(ev/'source-gold-reviewed.json'),'argv':plan['capture_argv']};(out/'commands.json').write_text(json.dumps(record,indent=2)+'\n')
env=dict(os.environ,DOTNET_PROCESSOR_COUNT='2',DOTNET_CLI_TELEMETRY_OPTOUT='1');start=time.monotonic()
with (out/'capture.stdout').open('w') as stdout,(out/'capture.stderr').open('w') as stderr:
 p=subprocess.run(plan['capture_argv'],env=env,stdout=stdout,stderr=stderr,timeout=500)
record.update(returncode=p.returncode,seconds=time.monotonic()-start);(out/'commands.json').write_text(json.dumps(record,indent=2)+'\n');p.check_returncode()
summary=json.loads((out/'capture.stdout').read_text());assert summary['complete']
a=json.loads(base64.b64decode(json.loads((out/'project.semantic').read_text())['payload']))
assert {c['project'] for c in a['contexts']}==set(plan['expected_project_closure'])
assert all(c['status']=='complete' and c['extractor_version']=='18' for c in a['contexts'])
gold=json.loads((ev/'source-gold-reviewed.json').read_text());expected={(c['path'],c['raw_sha256']) for t in gold['tasks'] for atom in t['atoms'] for c in atom['evidence']};actual={(s['path'],s['raw_sha256']) for s in a['sources']};assert expected<=actual
for path,digest in plan['required_source_hashes'].items():assert sha(r/'capture-input/CleanArchitecture'/path)==digest
roots={s['id']:summary['workspace'] for s in a['snapshots']};(out/'roots.json').write_text(json.dumps(roots,indent=2)+'\n')
report={'classification':'capture readiness after reviewed oracle freeze; not a scored journey','complete':True,'contexts':len(a['contexts']),'sources':len(a['sources']),'required_sources_present':len(expected),'artifact_sha256':sha(out/'project.semantic'),'gold_sha256':sha(ev/'source-gold-reviewed.json'),'source_unchanged':True,'context_roster':[{'project':c['project'],'status':c['status'],'id':c['id']} for c in a['contexts']]};(out/'report.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
