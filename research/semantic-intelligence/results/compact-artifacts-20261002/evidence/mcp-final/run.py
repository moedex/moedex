import hashlib,json,subprocess,sys,time
from pathlib import Path
r=Path('.local/compact-artifacts/mcp-final').resolve();sys.path.insert(0,str(Path('research/semantic-intelligence/agent-journeys').resolve()));import client,prepare
(r/'smoke-prompt.txt').write_text('Unscored development smoke: verify compiler discovery and definitions.\n')
logs=[(r/'server.stdout').open('wb'),(r/'server.stderr').open('wb')]
p=subprocess.Popen([str(r.parent/'moedex-final'),'serve','--index-dir',str(r.parent/'quartz/index'),'--embed','none','--mcp-http','127.0.0.1:19469'],stdout=logs[0],stderr=logs[1]);report={'classification':'unscored native compiler serving smoke','server_pid':p.pid}
try:
 deadline=time.monotonic()+15
 while '"msg":"listening"' not in (r/'server.stderr').read_text():
  if p.poll() is not None or time.monotonic()>deadline:raise RuntimeError('server startup failed')
  time.sleep(.05)
 run=r/'mcp-run';setup=r/'mcp-setup';prepare.prepare(setup,run,'http://127.0.0.1:19469/mcp',r/'smoke-prompt.txt')
 calls=[]
 def call(name,args):
  raw,event=client.request(run,'tools/call',{'name':name,'arguments':args});data=json.loads(raw);assert event['response_complete'] and event['http_status']==200 and not data.get('error') and not data['result'].get('isError'),data
  out=data['result']['structuredContent'];assert out['status']=='ok',out;calls.append({'tool':name,'bytes':len(raw),'snapshot_id':out['snapshot_id'],'artifact_sha256':out['artifact_sha256']});return out
 path='src/Quartz/Impl/StdScheduler.cs'
 symbols=call('compiler_symbols',{'repo':'Quartz','path':path,'query':'Start','limit':5})
 assert symbols['results'] and all(row['extractor_version']=='21' for row in symbols['results'])
 evidence=symbols['results'][0]
 definitions=call('compiler_definitions',{'symbol_id':evidence['symbol']['id'],'context_id':evidence['context_id'],'repo':'Quartz'})
 assert any(row['occurrence_id']==evidence['occurrence_id'] for row in definitions['results'])
 assert len({(x['snapshot_id'],x['artifact_sha256']) for x in calls})==1
 assert evidence['raw_sha256']==hashlib.sha256((Path('.local/holdout-quartz/corpus/Quartz')/path).read_bytes()).hexdigest()
 report.update(calls=calls,discovered_symbols=len(symbols['results']),definitions=len(definitions['results']),source_hash_verified=True,worker_version='21')
finally:
 p.terminate()
 try:code=p.wait(timeout=10)
 except subprocess.TimeoutExpired:p.kill();code=p.wait(timeout=5)
 for f in logs:f.close()
 report.update(server_returncode=code,server_stopped=p.poll() is not None);(r/'mcp-smoke.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps(report))
