import hashlib,json,shutil,subprocess
from pathlib import Path
base=Path('.local/compact-artifacts');out=Path('research/semantic-intelligence/results/compact-artifacts-20261002')
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def copy(p,d):d.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(p,d)
assert not out.exists();out.mkdir();prior=[]
for name in ('project-analyzers','quartz-capture-attempt','holdout-quartz','build-event-severity','capture-diagnostics','holdout-journeys'):
 p=Path('research/semantic-intelligence/results')/(name+'-20261002')/'result.json';r=json.loads(p.read_text())
 for f in r['files']:assert sha(p.parent/f['path'])==f['sha256'],f
 prior.append(dict(path=str(p),sha256=sha(p),files_verified=len(r['files'])))
manifest=json.loads(Path('.local/holdout-quartz/packet/source-manifest.json').read_text())
for f in manifest['files']:assert sha(Path('.local/holdout-quartz/corpus/Quartz')/f['path'])==f['sha256']
for p in base.iterdir():
 if p.is_file() and p.suffix in ('.py','.json','.log','.stdout','.stderr'):copy(p,out/'evidence'/p.name)
for p in (base/'quartz').iterdir():
 if p.is_file() and p.name!='project.semantic':copy(p,out/'evidence/quartz'/p.name)
for p in (base/'mcp-final').rglob('*'):
 if p.is_file():copy(p,out/'evidence/mcp-final'/p.relative_to(base/'mcp-final'))
fixture=json.loads((base/'fixture/result.json').read_text());assert [r['returncode'] for r in fixture]==[0,0,1,1,1]
copy(base/'fixture/result.json',out/'evidence/fixture/result.json')
for row in fixture:
 folder=base/'fixture'/row['name'];argv=row['argv'];commit=argv[argv.index('--commit')+1];repo=base/'fixture/corpus/Fixture'
 for p in folder.iterdir():
  if p.is_file():copy(p,out/'evidence/fixture'/row['name']/p.name)
 for path in subprocess.check_output(['git','-C',str(repo),'ls-tree','-r','--name-only',commit]).decode().splitlines():
  data=subprocess.check_output(['git','-C',str(repo),'show',commit+':'+path]);dest=out/'evidence/fixture'/row['name']/'source'/path;dest.parent.mkdir(parents=True,exist_ok=True);dest.write_bytes(data)
files=['internal/semantic/artifact.go','internal/semantic/artifact_compact_test.go','internal/semantic/compose.go','internal/app/semanticcmd/compose.go','internal/app/semanticcmd/compose_test.go','tools/semantic-dotnet/test_project_analyzers.py','tools/semantic-dotnet/README.md','docs/adr/0065-compact-semantic-artifacts.md','docs/adr/README.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md','research/semantic-intelligence/COMPACT-ARTIFACT-ACCEPTANCE.md']
files += [str(p) for p in Path('research/semantic-intelligence/agent-journeys').iterdir() if p.is_file() and p.suffix in ('.py','.md')]
for name in files:copy(Path(name),out/'source'/name)
for name in ('final-tests.log','final-vet.log','final-race.log'):assert 'FAIL' not in (base/name).read_text()
assert 'Ran 35 tests' in (base/'harness-tests.log').read_text() and '\nOK' in (base/'harness-tests.log').read_text()
capture=json.loads((base/'quartz/command.json').read_text());publish=json.loads((base/'quartz/publish.json').read_text());assert capture['returncode']==publish['returncode']==0
smoke=json.loads((base/'mcp-final/mcp-smoke.json').read_text());assert smoke['server_stopped'] and len(smoke['calls'])==2
result=dict(classification='development compact-storage acceptance and synthetic paired-harness validation; no heldout or paired quality score',storage=json.loads((base/'size-report.json').read_text()),quartz_summary=json.loads((base/'final-inspect.stdout').read_text()),capture=capture,publication=publish,native_mcp=smoke,fixture=fixture,validation={'go_full':True,'go_vet':True,'focused_race':True,'journey_harness_tests':35},final_cli_sha256=sha(base/'moedex-final'),capture_cli_sha256=sha(base/'moedex'),worker_sha256=sha(Path('.local/project-analyzers/final-worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll')),prior_archives_verified=prior,quartz_source_hashes_verified=len(manifest['files']),subagents_used=1,gpu_used=False,limitations=['Composition retains a 64 MiB canonical legacy-envelope aggregate budget.','Older binaries cannot read compact envelopes.','Serialization bounds do not bound process heap.','No competitive performance or quality claim; native CodeGraph setup remains open.'])
result['files']=[dict(path=str(p.relative_to(out)),bytes=p.stat().st_size,sha256=sha(p)) for p in sorted(out.rglob('*')) if p.is_file()]
(out/'result.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(dict(sha256=sha(out/'result.json'),files=len(result['files']),bytes=sum(f['bytes'] for f in result['files']))))
