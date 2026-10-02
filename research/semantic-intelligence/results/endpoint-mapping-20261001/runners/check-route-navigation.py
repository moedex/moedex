import json,sys
from pathlib import Path
sys.path.insert(0,str(Path('research/semantic-intelligence/agent-journeys').resolve()))
import client
base=Path('.local/endpoint-mapping');dest=base/'route-navigation';dest.mkdir()
r=json.loads((base/'ForkJoint-public/report.json').read_text());cases={x['id']:x for x in r['expectations']}
route=cases['route-post']['bindings'][0];fact=route['domain_facts'][0];handler=fact['targets'][0]['symbol']
request=cases['request-response']['bindings'][0]
assert request['enclosing_symbol_id']==handler['id'] and request['context_id']==route['context_id']
assert fact['route_pattern']=='/order' and fact['kind']=='endpoint_post_configuration'
for i,(tool,args) in enumerate([('compiler_definitions',{'symbol_id':handler['id'],'repo':'Sample-ForkJoint'}),('compiler_contract_context',{'symbol_id':handler['id'],'context_ids':[route['context_id']]})],1):
 q={'jsonrpc':'2.0','id':i,'method':'tools/call','params':{'name':tool,'arguments':args}}
 (dest/f'{i:03d}-request.json').write_text(json.dumps(q,indent=2)+'\n')
 raw,status,_=client.http_exchange('http://127.0.0.1:19395/mcp',json.dumps(q).encode(),60)
 (dest/f'{i:03d}-response.json').write_bytes(raw);assert status==200
 result=json.loads(raw)['result'];assert not result.get('isError');data=result['structuredContent'];assert data['status']=='ok' and data['artifact_sha256']==r['identity'][1]
 if tool=='compiler_definitions':
  assert len(data['results'])==1 and data['results'][0]['symbol']==handler and data['results'][0]['path']==route['path'] and data['results'][0]['raw_sha256']==route['raw_sha256']
 else:assert b'"route_pattern":"/order"' in raw
(dest/'report.json').write_text(json.dumps({'classification':'source_configurations_and_enclosing_method_identity_not_runtime_route','handler':handler,'route_pattern':'/order','same_context':route['context_id'],'request_fact':request['domain_facts'][0],'definition_navigation_passed':True,'contract_context_passed':True},indent=2)+'\n')
print('PASS: endpoint target definition and request occurrence share the exact handler and compiler context')
