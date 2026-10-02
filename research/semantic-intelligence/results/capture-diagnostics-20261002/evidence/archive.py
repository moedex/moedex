import ast, gzip, hashlib, json, shutil
from pathlib import Path
r=Path('.local/workspace-diagnostics');out=Path('research/semantic-intelligence/results/capture-diagnostics-20261002');out.mkdir(exist_ok=False)
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
def copy(source,dest):
 dest=out/dest;dest.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(source,dest)
paths=set(str(p.relative_to(r/'before')) for p in (r/'before').rglob('*') if p.is_file())
paths.update(['tools/semantic-dotnet/Worker.cs','tools/semantic-dotnet/TupleSymbols.cs','tools/semantic-dotnet/test_workspace_diagnostics.py','tools/semantic-dotnet/README.md','research/semantic-intelligence/agent-journeys/prepare.py','research/semantic-intelligence/agent-journeys/test_prepare.py','research/semantic-intelligence/agent-journeys/browse.py','research/semantic-intelligence/agent-journeys/client.py','research/semantic-intelligence/agent-journeys/README.md','research/semantic-intelligence/CAPTURE-DIAGNOSTICS-ACCEPTANCE.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md'])
for name in sorted(paths):
 p=Path(name)
 if p.suffix=='.py':ast.parse(p.read_text())
 copy(p,Path('source')/p)
for p in r.iterdir():
 if p.is_file() and p.suffix in ('.py','.log','.json','.stderr','.stdout'):
  copy(p,Path('evidence')/p.name)
for folder in ['final-live-setup','final-live-run']:
 for p in (r/folder).iterdir():
  if p.is_file() and p.name!='lock':copy(p,Path('evidence')/folder/p.name)
for p in (r/'regression-final').iterdir():
 if p.is_file():copy(p,Path('evidence/regression')/p.name)
for p in (r/'regression-final').rglob('Probe.*'):
 if 'worker' not in p.parts:copy(p,Path('evidence/regression')/p.relative_to(r/'regression-final'))
copy(r/'probe-worker/Worker.cs','evidence/probe-worker/Worker.cs')
raw=r/'worker19-final.jsonl';compressed=out/'evidence/worker19-final.jsonl.gz';compressed.write_bytes(gzip.compress(raw.read_bytes(),mtime=0));assert gzip.decompress(compressed.read_bytes())==raw.read_bytes()
roslyn=Path('.local/source-discovery-scale/corpus/roslyn')
for rel in ['src/Workspaces/MSBuild/Core/MSBuild/DiagnosticReporter.cs','src/Workspaces/MSBuild/BuildHost/MSBuild/Logging/MSBuildDiagnosticLogger.cs','src/Workspaces/MSBuild/BuildHost/MSBuild/Logging/DiagnosticLog.cs','License.txt']:
 p=roslyn/rel
 if p.exists():copy(p,Path('upstream-roslyn')/rel)
report=json.loads((r/'report.json').read_text());report['upstream_source_commit']='36d26c5466e4d25940657ccb8d5b9557ccaf7be1';report['prior_holdout_manifest_sha256']='c0abef1b8b406ab92cf748342b26d1916cefbba5d9c2e4770ef69be3eb2cbf08'
report['files']=[{'path':str(p.relative_to(out)),'bytes':p.stat().st_size,'sha256':sha(p)} for p in sorted(out.rglob('*')) if p.is_file()]
(out/'result.json').write_text(json.dumps(report,indent=2)+'\n')
for f in report['files']:assert sha(out/f['path'])==f['sha256']
print(json.dumps({'manifest_sha256':sha(out/'result.json'),'files':len(report['files']),'bytes':sum(f['bytes'] for f in report['files'])}))
