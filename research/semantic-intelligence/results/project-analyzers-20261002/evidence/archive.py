import base64,hashlib,json,shutil,subprocess
from pathlib import Path
base=Path('.local/project-analyzers');out=Path('research/semantic-intelligence/results/project-analyzers-20261002')
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def copy(src,dst):dst.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(src,dst)
assert not out.exists();out.mkdir();prior=[]
for name in ('quartz-capture-attempt','holdout-quartz','build-event-severity','capture-diagnostics','holdout-journeys'):
 p=Path('research/semantic-intelligence/results')/(name+'-20261002')/'result.json';r=json.loads(p.read_text())
 for f in r['files']:assert sha(p.parent/f['path'])==f['sha256'],f
 prior.append(dict(path=str(p),sha256=sha(p),files_verified=len(r['files'])))
manifest=json.loads(Path('.local/holdout-quartz/packet/source-manifest.json').read_text())
for f in manifest['files']:
 for folder in ('source','corpus/Quartz'):assert sha(Path('.local/holdout-quartz')/folder/f['path'])==f['sha256']
for p in Path('tools/semantic-dotnet').glob('*.cs'):assert sha(p)==sha(base/'final-worker'/p.name)
for p in base.iterdir():
 if p.is_file() and p.suffix in ('.py','.json','.log'):copy(p,out/'evidence'/p.name)
for folder in ('quartz-attempt1','quartz-attempt2','quartz-attempt3','quartz-attempt4','quartz-final','diagnostic-suite','worker-suite','legacy-suite','pinned-routing','pinned-routing-final'):
 for p in (base/folder).iterdir():
  if p.is_file():copy(p,out/'evidence'/folder/p.name)
for p in (base/'mcp-final').rglob('*'):
 if p.is_file():copy(p,out/'evidence/mcp-final'/p.relative_to(base/'mcp-final'))
for folder in ('fixture','fixture-multi','fixture-final'):
 r=json.loads((base/folder/'result.json').read_text());copy(base/folder/'result.json',out/'evidence'/folder/'result.json')
 for row in r:
  name=row['name'];argv=row['argv'];commit=argv[argv.index('--commit')+1];repo=base/folder/'corpus/Fixture'
  for p in (base/folder/name).iterdir():
   if p.is_file():copy(p,out/'evidence'/folder/name/p.name)
  paths=subprocess.check_output(['git','-C',str(repo),'ls-tree','-r','--name-only',commit]).decode().splitlines()
  for path in paths:
   data=subprocess.check_output(['git','-C',str(repo),'show',commit+':'+path]);dest=out/'evidence'/folder/name/'source'/path;dest.parent.mkdir(parents=True,exist_ok=True);dest.write_bytes(data)
files=set(json.loads((base/'version-files.json').read_text()))
files.update(str(p) for p in Path('tools/semantic-dotnet').glob('*.cs'))
files.update(['internal/semanticrun/run.go','internal/semanticrun/run_test.go','internal/semanticrun/run_prepare_test.go','internal/semanticrun/process.go','internal/semanticimport/import.go','internal/semanticimport/wire.go','internal/semanticimport/import_test.go','internal/semanticimport/build_diagnostics.go','internal/semanticimport/build_diagnostics_test.go','internal/app/semanticcmd/capture.go','internal/cli/semantic.go','internal/cli/semantic_capture_test.go','internal/semantic/artifact.go','tools/semantic-dotnet/test_project_analyzers.py','tools/semantic-dotnet/test_worker.py','tools/semantic-dotnet/test_workspace_diagnostics.py','tools/semantic-dotnet/Moedex.SemanticWorker.csproj','tools/semantic-dotnet/README.md','docs/adr/0064-project-built-analyzer-preparation.md','docs/adr/README.md','docs/plans/semantic-intelligence/PROGRAM.md','docs/plans/semantic-intelligence/DELIVERY.md'])
for name in files:copy(Path(name),out/'source'/name)
for name in ('go-final.log','go-vet-final.log','go-race.log'):assert 'FAIL' not in (base/name).read_text()
for name in ('worker-suite.log','legacy-suite.log','diagnostic-suite.log','pinned-routing-final.log'):assert 'PASS:' in (base/name).read_text()
fixture=json.loads((base/'fixture-final/result.json').read_text());assert [r['returncode'] for r in fixture]==[0,0,1,1,1]
smoke=json.loads((base/'mcp-final/mcp-smoke.json').read_text());assert smoke['server_stopped'] and len(smoke['calls'])==2
stderr=(base/'quartz-final/stderr').read_text();assert 'encoded size 225620341 bytes' in stderr and not (base/'quartz-final/project.semantic').exists()
result=dict(classification='development acceptance for analyzer preparation; Quartz artifact-size gate remains failed; no heldout or paired score',worker_version='21',final_cli_sha256=sha(base/'moedex-final'),worker_sha256=sha(base/'final-worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll'),final_fixture=fixture,generated_bindings=json.loads((base/'generated-binding-audit.json').read_text()),native_mcp=smoke,quartz={'commit':manifest['commit'],'classification':'development retry; original holdout immutable','worker_stream_budget':268435456,'artifact_limit':67108864,'encoded_artifact_bytes':225620341,'returncode':1,'artifact_published':False,'record':'evidence/quartz-final/command.json','source_hashes_unchanged':len(manifest['files'])},validation={'go_full':True,'go_vet':True,'focused_race':True,'sdk_10_0_401_worker_suite':True,'sdk_10_0_100_worker_suite':True,'native_diagnostic_controls':12,'pinned_roslyn_4_11_net8_routing':True,'ambiguous_output_rejected':True,'source_generator_debug_release_publication':True,'analyzer_mutation_rejected':True},prior_archives_verified=prior,subagents_used=0,gpu_used=False,limitations=['Framework mapping requires a unique evaluated declared-framework output path.','All declared frameworks restore; unselected loaded workspace diagnostics may block capture.','Project code is trusted; this is not an OS sandbox.','No Quartz publication, solver scores or paired CodeGraph claim.','Artifacts and source inputs retain 64 MiB caps; only worker JSONL transport can opt in to 256 MiB.'])
result['files']=[dict(path=str(p.relative_to(out)),bytes=p.stat().st_size,sha256=sha(p)) for p in sorted(out.rglob('*')) if p.is_file()]
(out/'result.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(dict(sha256=sha(out/'result.json'),files=len(result['files']),bytes=sum(f['bytes'] for f in result['files']))))
