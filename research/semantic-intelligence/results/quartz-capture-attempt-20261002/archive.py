import datetime,hashlib,json,shutil
from pathlib import Path
base=Path('.local/holdout-quartz');ev=base/'evaluation';packet=base/'packet';out=Path('research/semantic-intelligence/results/quartz-capture-attempt-20261002')
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def save(p,v):p.write_text(json.dumps(v,indent=2)+'\n')
assert not out.exists();prior=[]
for name in ('holdout-quartz','build-event-severity','capture-diagnostics','holdout-journeys'):
 p=Path('research/semantic-intelligence/results')/(name+'-20261002')/'result.json';r=json.loads(p.read_text())
 for f in r['files']:assert sha(p.parent/f['path'])==f['sha256'],f
 prior.append(dict(path=str(p),sha256=sha(p),files_verified=len(r['files'])))
for name in ('toolchain-manifest.json','product-manifest.json'):
 m=json.loads((packet/name).read_text())
 for f in m['files']:assert sha(Path(m['root'])/f['path'])==f['sha256']
for f in json.loads((packet/'dependency-manifest.json').read_text())['files']:assert sha(base/'bundle/packages'/f['path'])==f['sha256']
sm=json.loads((packet/'source-manifest.json').read_text())
for f in sm['files']:
 for d in ('source','corpus/Quartz'):assert sha(base/d/f['path'])==f['sha256']
for n,digest in json.loads((ev/'pre-capture-freeze.json').read_text()).items():assert sha(ev/n)==digest
for n,digest in json.loads((ev/'capture-02-freeze.json').read_text()).items():assert sha(ev/n)==digest
assert not (base/'capture/project.semantic').exists() and not (base/'capture-02/project.semantic').exists()
out.mkdir()
for p in ev.iterdir():
 if p.is_file():shutil.copy2(p,out/p.name)
for n in ('capture','capture-02'):shutil.copytree(base/n,out/n)
g=json.loads((ev/'source-gold-reviewed.json').read_text());paths=set()
for t in g['tasks']:
 for a in t['atoms']:
  for e in a['evidence']:
   p=base/'source'/e['path'];assert sha(p)==e['raw_sha256'];assert '\n'.join(p.read_text(encoding='utf-8-sig').splitlines()[e['start_line']-1:e['end_line']])==e['excerpt'];paths.add(e['path'])
paths.update(('src/Quartz/Quartz.csproj','src/Quartz.Analyzers/Quartz.Analyzers.csproj','Directory.Build.props'))
for name in paths:
 p=out/'source'/name;p.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(base/'source'/name,p)
for name in ('internal/semanticrun/run.go','tools/semantic-dotnet/Worker.cs'):
 p=out/'implementation'/name;p.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(name,p)
for name in ('product-manifest.json','toolchain-manifest.json','source-manifest.json','dependency-manifest.json'):shutil.copy2(packet/name,out/name)
result=dict(classification='independent oracle review completed; compiler-backed holdout setup failed before solver assignment',recorded_utc=datetime.datetime.now(datetime.timezone.utc).isoformat(),corpus_commit=sm['commit'],oracle_review={'agent':'/root/quartz_oracle_review','tasks':6,'atoms':30,'accepted_unchanged':20,'accepted_amendments':10,'gold_sha256':sha(ev/'source-gold-reviewed.json'),'prompts_changed':False,'gold_source_files':len(paths)-3},capture_attempts=[{'attempt':1,'record':'capture/command.json','returncode':1,'classification':'coordinator launch configuration error before worker','failure':'Canonical checkout directory source did not match public repository Quartz.','seconds':json.loads((base/'capture/command.json').read_text())['seconds']},{'attempt':2,'record':'capture-02/command.json','returncode':1,'classification':'frozen product capture setup failure','failure':'Worker FileNotFoundException for source/artifacts/bin/Quartz.Analyzers/debug/Quartz.Analyzers.dll','seconds':json.loads((base/'capture-02/command.json').read_text())['seconds']}],setup_successor='Explicitly recorded canonical checkout correction; original attempt retained; source/product/rubric unchanged.',semantic_artifact_produced=False,snapshot_published=False,product_query_calls=0,assigned_solvers=0,answer_reviewers=0,task_scores=None,source_only_fallback=False,paired_codegraph_executed=False,gpu_used=False,subagents_used=1,maximum_concurrent_subagents=1,authorized_solver_phase='unstarted because compiler publication prerequisite failed',source_product_toolchain_dependencies_unchanged=True,prior_archives_verified=prior,diagnosis='Source inspection shows managed capture restores then opens MSBuildWorkspace and hashes analyzer files; Quartz references a project-built analyzer using UseArtifactsOutput. Missing binary is directly observed; no full worker stack or retained failed workspace is available. Upstream normal/offline builds passed separately. Do not infer full compiler compatibility from that build.',next_work='Handle project-built analyzer/generator outputs in isolated capture with configuration/framework-aware build preparation and provenance, plus actionable fail-closed missing-input diagnostics. Retire Quartz to development evidence if product changes are made; use another fresh holdout for a new untouched product test.')
result['files']=[dict(path=str(p.relative_to(out)),bytes=p.stat().st_size,sha256=sha(p)) for p in sorted(out.rglob('*')) if p.is_file()]
save(out/'result.json',result);print(json.dumps(dict(files=len(result['files']),bytes=sum(f['bytes'] for f in result['files']),sha256=sha(out/'result.json'),gold_source_files=result['oracle_review']['gold_source_files'])))
