import json,sys
from pathlib import Path
sys.path.insert(0,str(Path('research/semantic-intelligence/agent-journeys').resolve()))
import client
base=Path('.local/mediatr-dispatch');dest=base/'navigation';dest.mkdir()
r=json.loads((base/'eShopOnWeb-public/report.json').read_text());cases={x['id']:x for x in r['expectations']};calls=[]
for label in ['mediator-orders','mediator-details']:
 row=cases[label]['bindings'][0];f=row['domain_facts'][0];request,response=[t['symbol'] for t in f['targets']]
 for name,args in [('compiler_definitions',{'symbol_id':request['id'],'repo':'eShopOnWeb'}),('compiler_contract_context',{'symbol_id':response['id'],'context_ids':[row['context_id']]})]:
  q={'jsonrpc':'2.0','id':len(calls)+1,'method':'tools/call','params':{'name':name,'arguments':args}};i=len(calls)+1
  (dest/f'{i:03d}-request.json').write_text(json.dumps(q,indent=2)+'\n');raw,status,_=client.http_exchange('http://127.0.0.1:19397/mcp',json.dumps(q).encode(),60);(dest/f'{i:03d}-response.json').write_bytes(raw)
  assert status==200;result=json.loads(raw)['result'];assert not result.get('isError');data=result['structuredContent'];assert data['status']=='ok' and data['artifact_sha256']==r['identity'][1]
  if name=='compiler_definitions':assert len(data['results'])==1 and data['results'][0]['symbol']==request and data['results'][0]['path'].endswith('/'+request['descriptor'].split('.')[-1]+'.cs')
  else:assert any(s['id']==response['id'] and s['descriptor']==response['descriptor'] for s in data['symbols'])
  calls.append({'anchor':label,'tool':name,'passed':True})
(dest/'report.json').write_text(json.dumps({'classification':'direct_request_construction_and_declared_response_not_runtime_handler_selection','calls':calls},indent=2)+'\n')
print('PASS: two request definitions and both exact response identities through public context navigation')
