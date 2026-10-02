import json,sys,subprocess
from pathlib import Path
sys.path.insert(0,'research/semantic-intelligence/agent-journeys')
import client
out=Path('.local/bounded-views'); run=out/'live-run'
raw,status,_=client.http_exchange('http://127.0.0.1:19460/mcp',json.dumps({'jsonrpc':'2.0','id':0,'method':'tools/list','params':{}}).encode(),30)
assert status==200
(out/'catalog.json').write_bytes(raw);(out/'prompt.txt').write_text('Scripted browser integration; not an agent task.\n')
client.initialize(run,'http://127.0.0.1:19460/mcp',out/'prompt.txt',out/'catalog.json')
base=[sys.executable,'-B','research/semantic-intelligence/agent-journeys/browse.py','--run-dir',str(run)]
views=[]
def invoke(args):
 p=subprocess.run(base+args,capture_output=True,check=True); assert len(p.stdout)<=8192 and not p.stderr
 v=json.loads(p.stdout);views.append(v);return v
c=invoke(['catalog']);assert c['next_offset'] is None
first=invoke(['call','--name','compiler_symbols','--arguments',json.dumps({'repo':'Sample-Outbox','path':'src/Sample.Components/Services/IRegistrationValidationService.cs'})])
assert first['partial'];parts=[first['data']];offset=first['next_offset']
while offset is not None:
 v=invoke(['--offset',str(offset),'response','--ordinal','1']);parts.append(v['data']);offset=v['next_offset']
raw=(run/'001.response.raw').read_bytes(); assert json.loads(''.join(parts))==json.loads(raw)
s=json.loads((run/'state.json').read_bytes());assert s['calls']==1 and s['response_bytes']==len(raw)
v=invoke(['--pointer','/result/structuredContent/status','response','--ordinal','1']);assert json.loads(v['data'])=='ok'
assert json.loads((run/'state.json').read_bytes())==s
(out/'live-views.json').write_text(json.dumps(views,indent=2)+'\n')
(out/'live-report.json').write_text(json.dumps({'passed':True,'calls':s['calls'],'response_bytes':len(raw),'views':len(views),'full_response_reconstructed':True,'selected_field_correct':True,'local_views_do_not_charge_or_mutate_state':True},indent=2)+'\n')
print((out/'live-report.json').read_text())
