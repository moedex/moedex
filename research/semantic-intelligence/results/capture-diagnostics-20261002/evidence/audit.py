import collections, hashlib, json
from pathlib import Path
r=Path('.local/workspace-diagnostics');sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
rows=[json.loads(x) for x in (r/'worker19-final.jsonl').read_bytes().splitlines()]
assert rows[-1]['record_type']=='stream_summary' and rows[-1]['compilation_status']=='incomplete'
assert rows[-1]['projects']==6 and not (r/'worker19-final.stderr').read_bytes()
errors=[x for x in rows if x['record_type']=='diagnostic' and x['severity']=='error']
ws=[x for x in errors if x['code']=='workspace'];assert all(x['message'].startswith('[Failure]') for x in ws)
assert not [x for x in errors if x['code'].startswith('CS')]
source=Path('.local/holdout-cleanarchitecture/offline-source')
manifest=json.loads(Path('research/semantic-intelligence/results/holdout-cleanarchitecture-20261002/source-manifest.json').read_text())
for f in manifest['files']:assert sha(source/f['path'])==f['sha256'],f['path']
index=Path('.local/holdout-cleanarchitecture/source-only/index')
index_manifest=json.loads(Path('.local/holdout-cleanarchitecture/evaluation/index-hashes.json').read_text())
for f in index_manifest:assert sha(index/f['path'])==f['sha256'],f['path']
prior=Path('research/semantic-intelligence/results/holdout-journeys-20261002')
assert sha(prior/'result.json')=='c0abef1b8b406ab92cf748342b26d1916cefbba5d9c2e4770ef69be3eb2cbf08'
old=json.loads((prior/'result.json').read_text())
for f in old['files']:assert sha(prior/f['path'])==f['sha256'],f['path']
for p in Path('tools/semantic-dotnet').glob('*.cs'):assert sha(p)==sha(r/'regression-final/worker'/p.name)
client=Path('research/semantic-intelligence/agent-journeys/client.py');assert sha(client)=='6e2934d7f16a8f87ba961d716899a8f298c8abf9feea367cce2b9e2fc00c36a1'
state=json.loads((r/'final-live-run/state.json').read_text());assert state['calls']==1 and state['response_bytes']==414 and state['stopped'] is None
wire=json.loads((r/'final-live-run/001.response.raw').read_text());assert not wire['result'].get('isError')
rawdiag=[json.loads(x) for x in (r/'probe2.stderr').read_text().splitlines() if x.startswith('{')]
assert json.loads((r/'bootstrap-smoke.json').read_text())['server_stopped']
assert 'FAIL' not in (r/'go-final.log').read_text()
assert not (r/'go-vet.log').read_bytes()
assert 'PASS:' in (r/'full-worker-final.log').read_text()
assert 'Ran 20 tests' in (r/'journey-tests.log').read_text() and 'OK' in (r/'journey-tests.log').read_text()
report={'classification':'worker19 development regressions and unscored MCP bootstrap; no new independent holdout/paired result',
 'compiler_capture_complete':False,'worker19_summary':rows[-1], 'workspace_events':len(ws)//6,'workspace_unique_messages':len({x['message'] for x in ws}),
 'workspace_reported_kinds':dict(collections.Counter(x['kind'] for x in rawdiag)),
 'error_records':dict(collections.Counter(x['code'] for x in errors)),
 'tuple_crash_fixed':True,'source_files_unchanged':len(manifest['files']),'index_files_unchanged':len(index_manifest),'previous_archive_files_unchanged':len(old['files']),
 'worker_source_files_verified':len(list(Path('tools/semantic-dotnet').glob('*.cs'))),'accounting_client_unchanged':True,
 'live_bootstrap':json.loads((r/'bootstrap-smoke.json').read_text()),'live_first_call':{'calls':state['calls'],'response_bytes':state['response_bytes'],'native_success':True},
 'validation':{'journey_unit_tests':20,'worker_regression':['tuple','warning','error'],'existing_worker_suite':'passed','go_test_all':'passed','go_vet_all':'passed'},
 'temporary_server_stopped':True,'gpu_used':False,'subagents_used':0,
 'worker19_jsonl_sha256':sha(r/'worker19-final.jsonl'),'worker19_jsonl_bytes':(r/'worker19-final.jsonl').stat().st_size}
(r/'report.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report,indent=2))
