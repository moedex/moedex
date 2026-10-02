import base64, datetime, hashlib, json, pathlib, shutil
root=pathlib.Path.cwd(); local=root/'.local/journey-readiness'; out=root/'research/semantic-intelligence/results/journey-readiness-20261001'; old=root/'research/semantic-intelligence/results/agent-journeys-20261001'; cap=local/'capture-sdk10'
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def write(p,d):p.parent.mkdir(parents=True,exist_ok=True);p.write_text(json.dumps(d,indent=2)+'\n')
def copy(p,d):d.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(p,d)
out.mkdir(exist_ok=False)
rubric=json.loads((old/'source-rubric.json').read_text()); inputs=json.loads((cap/'inputs.json').read_text());expected={x['path']:x['raw_sha256'] for x in rubric['source_roster']};assert expected==inputs['source_sha256'];assert inputs['source_unchanged'];assert all(sha(root/'.local/application-impact/Sample-Outbox'/p)==h for p,h in expected.items())
artifact=json.loads(base64.b64decode(json.loads((cap/'composed.semantic').read_text())['payload']));assert len(artifact['contexts'])==4;assert all(x['status']=='complete' and x['extractor_version']=='18' for x in artifact['contexts']);assert {json.loads(x['capture'])['compiler_version'] for x in artifact['contexts']}=={'5.0.0.0'}
for name in ('catalog-request.json','catalog-response.json','catalog.json','harness-tests.log','server.stdout','server.stderr'):
 copy(local/name,out/name)
for p in cap.iterdir():
 if p.is_file() and p.suffix in ('.json','.stdout','.stderr'):copy(p,out/'capture'/p.name)
for p in (local/'capture').iterdir():
 if p.is_file() and p.suffix in ('.json','.stdout','.stderr'):copy(p,out/'failed-pinned-sdk10'/p.name)
for p in (local/'smoke-validation').iterdir():
 if p.is_file():copy(p,out/'smoke-validation'/p.name)
for name in ('client.py','check_discovery_workflow.py','test_client.py','test_discovery_workflow.py'):
 copy(root/'research/semantic-intelligence/agent-journeys'/name,out/'harness'/name)
copy(root/'research/semantic-intelligence/check-application-capture.py',out/'harness/check-application-capture.py')
copy(local/'freeze.py',out/'harness/freeze.py')
protocol=json.loads((old/'protocol-v2.json').read_text());protocol.update(status='frozen_launch_packet_awaiting_delegation_authorization',classification='single_product_development_agent_journey_current_build',frozen_at_utc=datetime.datetime.now(datetime.timezone.utc).isoformat(),arms=['Moedex production HTTP MCP, CPU only, worker18 / SDK10 Roslyn5 Outbox snapshot'],successor_of_sha256=sha(old/'protocol-v2.json'))
protocol.pop('infrastructure_amendment',None)
protocol['execution_plan']={'tasks':12,'fresh_solver_per_task':True,'fork_turns':'none','maximum_concurrent_children':1,'task_order':[x['id'] for x in protocol['tasks']],'repeat_old_three':True,'pool_prior_scores':False,'launch_authorized':False,'reason':'User requested single threaded, no more subagents for now; fresh independent solvers and reviewers require an explicit exception.','review':'Root scores each task against frozen rubric and actual logged evidence; a fresh independent reviewer checks each task sequentially. Resolve and retain disagreements.','timing':'Record assignment and final-answer UTC timestamps and coordinator monotonic deadline; interrupt at 600 seconds. Preserve incomplete answers, errors and failed attempts. Never silently rerun an evidence-exposed task.','activity_audit':'Record observable agent tool activity and isolation attestations. If full activity cannot be inspected, mark audit incomplete and do not claim verified isolation.'}
protocol['isolation']+=' Root already knows gold/source and must not substitute its own answers as independent solvers. Shared workspace access remains technically possible.'
protocol['server']={'endpoint':'http://127.0.0.1:19457/mcp','snapshot':'application-real','index':str(cap/'index'),'embeddings':'none','graph':'absent; immutable source and compiler evidence available','mcp_max_concurrency':1,'native_semantic_artifact_sha256':sha(cap/'composed.semantic'),'worker_version':'18','compiler_version':'5.0.0.0','sdk':'10.0.100','owner':'coordinator must start and retain ownership during execution'}
protocol['prior_baseline']={'result_sha256':sha(old/'result.json'),'executed_tasks':3,'planned_tasks':12,'strict_successes':2,'correctness':'14/14','evidence':'13.5/14','current_results':'none; smoke is not scored'}
protocol['frozen_inputs']={}
for p in (old/'source-rubric.json',old/'rubric-review.json',root/'research/semantic-intelligence/agent-journeys/client.py',root/'.local/routing-slip/moedex',root/'.local/routing-slip/worker-default/bin/Debug/net10.0/Moedex.SemanticWorker.dll',cap/'composed.semantic',local/'catalog.json',root/'.local/application-impact/bundle/manifest.json'):
 protocol['frozen_inputs'][str(p.relative_to(root))]={'sha256':sha(p),'bytes':p.stat().st_size}
for task in protocol['tasks']:
 p=out/'prompts'/(task['id']+'.txt');p.parent.mkdir(exist_ok=True);p.write_text(task['prompt']+'\n')
copy(old/'solver-instructions-v2.txt',out/'solver-instructions.txt')
protocol['frozen_inputs'][str((out/'solver-instructions.txt').relative_to(root))]={'sha256':sha(out/'solver-instructions.txt'),'bytes':(out/'solver-instructions.txt').stat().st_size}
write(out/'protocol.json',protocol)
write(out/'capture-summary.json',{'source_files_verified':len(expected),'source_unchanged':True,'complete_contexts':len(artifact['contexts']),'worker_version':'18','compiler_version':'5.0.0.0','composed_sha256':sha(cap/'composed.semantic'),'cli_sha256':sha(root/'.local/routing-slip/moedex'),'worker_sha256':sha(root/'.local/routing-slip/worker-default/bin/Debug/net10.0/Moedex.SemanticWorker.dll'),'failed_attempt':'Pinned Roslyn4.11 worker with SDK10 failed MissingMethodException in Microsoft.NET.StringTools; failure retained. Successful capture uses source-identical SDK-matched default worker.','source_verification':'All 40 tracked hashes exactly match the frozen independent rubric, and capture left source unchanged.'})
write(out/'result.json',{'status':'ready_for_authorized_sequential_independent_execution','independent_tasks_executed':0,'independent_score':None,'classification':'coordinator_readiness_not_independent_solver_evaluation','harness_tests_passed':12,'public_catalog_tools':22,'smoke':{'calls':11,'response_bytes':45931,'passed':True},'source_files_verified':40,'prior_archive_verified':'routing-slip-20261001 all 355 file hashes; 78 current archived source files; default worker C# source parity','files':[{'path':str(p.relative_to(out)),'sha256':sha(p),'bytes':p.stat().st_size} for p in sorted(out.rglob('*')) if p.is_file()]})
print(out)
