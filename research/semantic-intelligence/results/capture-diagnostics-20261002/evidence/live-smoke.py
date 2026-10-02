import json, os, subprocess, sys, time
from pathlib import Path
r=Path('.local/workspace-diagnostics').resolve();sys.path.insert(0,str(Path('research/semantic-intelligence/agent-journeys').resolve()));import prepare,client
(r/'smoke-prompt.txt').write_text('Unscored setup smoke: inspect native instructions, then list repositories.\n')
logs=[(r/'final-server.stdout').open('wb'),(r/'final-server.stderr').open('wb')]
p=subprocess.Popen([str(r/'moedex'),'serve','--index-dir',str(Path('.local/holdout-cleanarchitecture/source-only/index').resolve()),'--embed','none','--mcp-http','127.0.0.1:19463'],stdout=logs[0],stderr=logs[1])
report={'classification':'unscored local MCP bootstrap smoke','server_pid':p.pid}
try:
 deadline=time.monotonic()+15
 while '"msg":"listening"' not in (r/'final-server.stderr').read_text():
  if p.poll() is not None or time.monotonic()>deadline:raise RuntimeError('server not ready')
  time.sleep(.05)
 run=r/'final-live-run';setup=r/'final-live-setup';setup_report=prepare.prepare(setup,run,'http://127.0.0.1:19463/mcp',r/'smoke-prompt.txt')
 parts=[];offset=0;n=0
 while True:
  view=subprocess.run([sys.executable,'research/semantic-intelligence/agent-journeys/browse.py','--run-dir',str(run),'--pointer','/instructions','--offset',str(offset),'instructions'],capture_output=True,check=True)
  n+=1;(r/f'final-instructions-view-{n:02d}.json').write_bytes(view.stdout);assert len(view.stdout)<=8192
  page=json.loads(view.stdout);parts.append(page['data']);offset=page['next_offset']
  if offset is None:break
 instructions=json.loads((run/'initialize.json').read_text())['result']['instructions'];assert json.loads(''.join(parts))==instructions
 state=json.loads((run/'state.json').read_text());assert state['calls']==0 and state['response_bytes']==0 and state['started_monotonic'] is None
 report.update(instruction_views=n,instruction_bytes=len(instructions.encode()),tool_count=setup_report['tool_count'],task_calls_before_smoke=0,task_bytes_before_smoke=0,task_clock_started=False)
 view=subprocess.run([sys.executable,'research/semantic-intelligence/agent-journeys/browse.py','--run-dir',str(run),'call','--name','list_repos','--arguments','{}'],capture_output=True,check=True);(r/'final-live-call-view.json').write_bytes(view.stdout)
 state=json.loads((run/'state.json').read_text());assert state['calls']==1 and state['response_bytes']==414 and state['stopped'] is None
 assert not json.loads((run/'001.response.raw').read_text())['result'].get('isError')
 report['first_call']={'calls':state['calls'],'response_bytes':state['response_bytes'],'native_success':True}
finally:
 p.terminate()
 try:code=p.wait(timeout=10)
 except subprocess.TimeoutExpired:p.kill();code=p.wait(timeout=5)
 for f in logs:f.close()
 report.update(server_returncode=code,server_stopped=p.poll() is not None)
 (r/'bootstrap-smoke.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps(report))
