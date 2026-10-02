import ast,gzip,hashlib,json,shutil
from pathlib import Path
r=Path('.local/build-event-severity');out=Path('research/semantic-intelligence/results/build-event-severity-20261002');out.mkdir(exist_ok=False);sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
def copy(p,dest):
 dest=out/dest;dest.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(p,dest)
paths={str(p.relative_to(r/'before')) for p in (r/'before').rglob('*') if p.is_file()}
paths.update(['tools/semantic-dotnet/Worker.cs','tools/semantic-dotnet/BuildDiagnostics.cs','tools/semantic-dotnet/TupleSymbols.cs','tools/semantic-dotnet/Moedex.SemanticWorker.csproj','tools/semantic-dotnet/test_worker.py','tools/semantic-dotnet/test_workspace_diagnostics.py','tools/semantic-dotnet/README.md','internal/semanticimport/build_diagnostics.go','internal/semanticimport/build_diagnostics_test.go','internal/semanticimport/import.go','internal/semanticimport/revalidate.go','internal/mcp/compiler_implementations_test.go','research/semantic-intelligence/results/go.mod','research/semantic-intelligence/BUILD-EVENT-SEVERITY-ACCEPTANCE.md','docs/adr/0063-native-build-diagnostic-severity.md','docs/adr/README.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md'])
for name in sorted(paths):
 p=Path(name)
 if p.suffix=='.py':ast.parse(p.read_text())
 copy(p,Path('source')/p)
for p in r.iterdir():
 if p.is_file() and p.suffix in ('.py','.log','.json','.stdout','.stderr'):
  copy(p,Path('evidence')/p.name)
for folder in ['accepted-capture','mcp-setup','mcp-run']:
 for p in (r/folder).iterdir():
  if p.is_file() and p.suffix in ('.json','.raw','.jsonl','.stdout','.stderr','.txt'):copy(p,Path('evidence')/folder/p.name)
for p in (r/'accepted-worker').iterdir():
 if p.is_file():copy(p,Path('evidence/regressions')/p.name)
for p in (r/'accepted-worker').rglob('Probe.*'):
 if 'worker' not in p.parts and 'fault-worker' not in p.parts:copy(p,Path('evidence/regressions')/p.relative_to(r/'accepted-worker'))
for folder in ['legacy-policy-suite','native-policy-suite','pinned-routing']:
 for name in ['BuildWarning.jsonl','capture.jsonl','capture.stderr','result.json','results.json']:
  p=r/folder/name
  if p.is_file():copy(p,Path('evidence')/folder/name)
# The minimal native replay probe records original typed build events, without
# retaining raw binlogs or large corpus captures.
copy(r/'probe-worker/Worker.cs','evidence/probe-worker/Worker.cs')
report=json.loads((r/'report.json').read_text());report['files']=[{'path':str(p.relative_to(out)),'bytes':p.stat().st_size,'sha256':sha(p)} for p in sorted(out.rglob('*')) if p.is_file()]
(out/'result.json').write_text(json.dumps(report,indent=2)+'\n')
for f in report['files']:assert sha(out/f['path'])==f['sha256']
print(json.dumps({'manifest_sha256':sha(out/'result.json'),'files':len(report['files']),'bytes':sum(f['bytes'] for f in report['files'])}))
