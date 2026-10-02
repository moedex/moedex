import base64,datetime,hashlib,json,os,subprocess,time
from pathlib import Path
base=Path('.local/holdout-quartz').resolve();ev=base/'evaluation';packet=base/'packet';out=base/'capture'
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
for name in ('toolchain-manifest.json','product-manifest.json'):
 m=json.loads((packet/name).read_text())
 for f in m['files']:assert sha(Path(m['root'])/f['path'])==f['sha256'],f
for f in json.loads((packet/'dependency-manifest.json').read_text())['files']:assert sha(base/'bundle/packages'/f['path'])==f['sha256']
for f in json.loads((packet/'source-manifest.json').read_text())['files']:assert sha(base/'source'/f['path'])==f['sha256']
for name,digest in json.loads((ev/'pre-capture-freeze.json').read_text()).items():assert sha(ev/name)==digest
out.mkdir(exist_ok=False);plan=json.loads((packet/'launch-plan.json').read_text());record={'started_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'gold_sha256':sha(ev/'source-gold-reviewed.json'),'argv':plan['capture_argv']}
(out/'command.json').write_text(json.dumps(record,indent=2)+'\n');start=time.monotonic()
with (out/'capture.stdout').open('wb') as stdout,(out/'capture.stderr').open('wb') as stderr:
 try:code=subprocess.run(record['argv'],env=dict(os.environ,DOTNET_PROCESSOR_COUNT='2',DOTNET_CLI_TELEMETRY_OPTOUT='1'),stdout=stdout,stderr=stderr,timeout=500).returncode
 except subprocess.TimeoutExpired:code=124
record.update(returncode=code,seconds=time.monotonic()-start);(out/'command.json').write_text(json.dumps(record,indent=2)+'\n')
if code:
 print(json.dumps(record));raise SystemExit(code)
summary=json.loads((out/'capture.stdout').read_text());assert summary['complete']
a=json.loads(base64.b64decode(json.loads((out/'project.semantic').read_text())['payload']))
assert all(c['status']=='complete' and c['extractor_version']=='20' for c in a['contexts'])
gold=json.loads((ev/'source-gold-reviewed.json').read_text());expected={(e['path'],e['raw_sha256']) for t in gold['tasks'] for atom in t['atoms'] for e in atom['evidence']};actual={(s['path'],s['raw_sha256']) for s in a['sources']};assert expected<=actual
(out/'roots.json').write_text(json.dumps({s['id']:summary['workspace'] for s in a['snapshots']},indent=2)+'\n')
report=dict(complete=True,contexts=len(a['contexts']),sources=len(a['sources']),required_sources_present=len(expected),artifact_sha256=sha(out/'project.semantic'),gold_sha256=sha(ev/'source-gold-reviewed.json'),context_roster=[{k:c[k] for k in ('project','status','id')} for c in a['contexts']])
(out/'report.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
