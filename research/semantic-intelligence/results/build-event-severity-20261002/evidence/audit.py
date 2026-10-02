import base64,collections,hashlib,json
from pathlib import Path
r=Path('.local/build-event-severity');sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
p=r/'accepted-capture/project.semantic';a=json.loads(base64.b64decode(json.loads(p.read_text())['payload']));assert sha(p)=='ec12ca74fd149a2926f1fa432cfdc1ea571671a4f9ccf67b908bb139c113dad4'
summary=json.loads((r/'accepted-capture/capture.stdout').read_text());root=Path(summary['workspace']);assert summary['complete'] and len(a['contexts'])==6
proofs=[]
for c in a['contexts']:
 proof=json.loads(c['capture'])['build_diagnostics'];assert c['status']=='complete' and c['extractor_version']=='20' and proof['verified'] and proof['policy']=='native-build-events-v1';assert proof['matched_warnings']==15 and not proof['errors'] and proof['reader_assembly']['sha256'];proofs.append({'project':c['project'],'context_id':c['id'],'proof':proof})
(r/'build-evidence.json').write_text(json.dumps(proofs,indent=2)+'\n')
manifest=json.loads(Path('research/semantic-intelligence/results/holdout-cleanarchitecture-20261002/source-manifest.json').read_text())
for f in manifest['files']:assert sha(root/f['path'])==f['sha256'],f['path']
gold=json.loads(Path('.local/holdout-cleanarchitecture/evaluation/source-gold-reviewed.json').read_text());expected={(c['path'],c['raw_sha256']) for t in gold['tasks'] for atom in t['atoms'] for c in atom['evidence']};assert expected<={(s['path'],s['raw_sha256']) for s in a['sources']}
for p in Path('tools/semantic-dotnet').glob('*.cs'):assert sha(p)==sha(r/'accepted-worker/worker'/p.name),p
for log in ['native-policy-suite.log','legacy-policy-suite.log','pinned-routing.log','accepted-worker.log']:assert 'PASS:' in (r/log).read_text(),log
assert 'FAIL' not in (r/'go-final.log').read_text() and not (r/'go-vet.log').read_bytes()
assert json.loads((r/'publication-accepted.json').read_text())['returncode']==0
smoke=json.loads((r/'mcp-smoke.json').read_text());assert smoke['server_stopped'] and smoke['server_returncode']==0 and len(smoke['calls'])==3
assert all(c['artifact_sha256']==sha(r/'accepted-capture/project.semantic') for c in smoke['calls'])
controls=json.loads((r/'import-controls.json').read_text());assert sum(c['artifact_published'] for c in controls)==3 and len(controls)==12
previous={}
for name,want in [('capture-diagnostics-20261002','e49433226271f91ef1f30b568da8ba6411bc518a415444c214265aba22bd9964'),('holdout-journeys-20261002','c0abef1b8b406ab92cf748342b26d1916cefbba5d9c2e4770ef69be3eb2cbf08')]:
 old=Path('research/semantic-intelligence/results')/name;assert sha(old/'result.json')==want
 data=json.loads((old/'result.json').read_text())
 for f in data['files']:assert sha(old/f['path'])==f['sha256']
 previous[name]={'manifest_sha256':want,'files_verified':len(data['files'])}
index=Path('.local/holdout-cleanarchitecture/source-only/index')
for f in json.loads(Path('.local/holdout-cleanarchitecture/evaluation/index-hashes.json').read_text()):assert sha(index/f['path'])==f['sha256']
report={'classification':'worker20 compiler readiness on development corpus; no new independent holdout or paired CodeGraph score','capture':summary,'artifact_sha256':sha(r/'accepted-capture/project.semantic'),'tracked_source_files_unchanged':len(manifest['files']),'prior_gold_source_hashes_present':len(expected),'native_build_events':15,'unique_workspace_warnings_per_context':9,'workspace_warning_facts':sum(d['code']=='workspace' and d['severity']=='warning' for d in a['diagnostics']),'mcp':smoke,'import_controls':controls,'prior_archives':previous,'prior_source_only_index_unchanged':True,'final_cli_sha256':sha(r/'moedex'),'worker_sha256':sha(r/'accepted-worker/worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll'),'full_go_tests':'pass','go_vet':'pass','default_sdk100_suite':'pass','native_sdk401_suite':'pass','pinned_roslyn411_net8':'pass','gpu_used':False,'subagents_used':0,'raw_binlogs_retained':False}
(r/'report.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps({k:report[k] for k in ['classification','artifact_sha256','tracked_source_files_unchanged','prior_gold_source_hashes_present','workspace_warning_facts']}))
