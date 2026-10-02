import json,sys,hashlib
from pathlib import Path
sys.path.insert(0,str(Path('research/semantic-intelligence/agent-journeys').resolve()))
import client
base=Path('.local/masstransit-scan');dest=base/'navigation';dest.mkdir()
r=json.loads((base/'ForkJoint-public/report.json').read_text());cases={x['id']:x for x in r['expectations']};calls=[]
for label,kind,path,descriptor in [
 ('consumer-scan','consumer_namespace_scan_configuration','src/ForkJoint.Api/Components/Consumers/CookOnionRingsConsumer.cs','T:ForkJoint.Api.Components.Consumers.CookOnionRingsConsumer'),
 ('activity-scan','activity_namespace_scan_configuration','src/ForkJoint.Api/Components/Activities/GrillBurgerActivity.cs','T:ForkJoint.Api.Components.Activities.GrillBurgerActivity')]:
 row=cases[label]['bindings'][0];f=row['domain_facts'][0];assert f['kind']==kind and f['rule']=='csharp-masstransit-scan-v1'
 assert len(f['targets'])==1 and f['targets'][0]['role']=='namespace_marker'
 symbol=f['targets'][0]['symbol'];assert symbol['namespace']=='Sample-ForkJoint/src/ForkJoint.Api/ForkJoint.Api.csproj' and symbol['descriptor']==descriptor
 for name,args in [('compiler_definitions',{'symbol_id':symbol['id'],'repo':'Sample-ForkJoint'}),('compiler_contract_impact',{'symbol_id':symbol['id'],'context_ids':[row['context_id']]}),('compiler_contract_context',{'symbol_id':symbol['id'],'context_ids':[row['context_id']]})]:
  q={'jsonrpc':'2.0','id':len(calls)+1,'method':'tools/call','params':{'name':name,'arguments':args}};i=len(calls)+1
  (dest/f'{i:03d}-request.json').write_text(json.dumps(q,indent=2)+'\n');raw,status,_=client.http_exchange('http://127.0.0.1:19438/mcp',json.dumps(q).encode(),60);(dest/f'{i:03d}-response.json').write_bytes(raw)
  assert status==200;result=json.loads(raw)['result'];assert not result.get('isError');data=result['structuredContent'];assert data['status']=='ok' and data['artifact_sha256']==r['identity'][1]
  if name=='compiler_definitions':
   assert len(data['results'])==1;hit=data['results'][0]
   assert hit['symbol']==symbol and hit['path']==path and hit['raw_sha256']==hashlib.sha256((Path('.local/fresh-paths/corpus/Sample-ForkJoint')/path).read_bytes()).hexdigest()
  else:assert kind in json.dumps(data) and row['path'] in json.dumps(data)
  calls.append({'anchor':label,'tool':name,'passed':True})
(dest/'report.json').write_text(json.dumps({'classification':'declared_namespace_scopes_not_discovered_registrations','calls':calls},indent=2)+'\n')
print('PASS: both scan markers, source definitions and reverse namespace-scan context verified through public MCP')
