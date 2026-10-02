import json,sys,hashlib
from pathlib import Path
sys.path.insert(0,str(Path('research/semantic-intelligence/agent-journeys').resolve()))
import client
base=Path('.local/open-generic-di');dest=base/'navigation';dest.mkdir()
r=json.loads((base/'eShopOnWeb-public/report.json').read_text());row=next(x for x in r['expectations'] if x['id']=='open-repository')['bindings'][0]
f=row['domain_facts'][0];assert f['kind']=='di_open_generic_registration_configuration' and f['lifetime']=='scoped'
expected=[('service_template','src/ApplicationCore/Interfaces/IReadRepository.cs','eShopOnWeb/src/ApplicationCore/ApplicationCore.csproj','T:Microsoft.eShopWeb.ApplicationCore.Interfaces.IReadRepository`1'),('implementation_template','src/Infrastructure/Data/EfRepository.cs','eShopOnWeb/src/Infrastructure/Infrastructure.csproj','T:Microsoft.eShopWeb.Infrastructure.Data.EfRepository`1')]
calls=[]
for target,(role,path,namespace,descriptor) in zip(f['targets'],expected):
 symbol=target['symbol'];assert target['role']==role and symbol['namespace']==namespace and symbol['descriptor']==descriptor
 for name,args in [('compiler_definitions',{'symbol_id':symbol['id'],'repo':'eShopOnWeb'}),('compiler_contract_impact',{'symbol_id':symbol['id'],'context_ids':[row['context_id']]}),('compiler_contract_context',{'symbol_id':symbol['id'],'context_ids':[row['context_id']]})]:
  q={'jsonrpc':'2.0','id':len(calls)+1,'method':'tools/call','params':{'name':name,'arguments':args}};i=len(calls)+1
  (dest/f'{i:03d}-request.json').write_text(json.dumps(q,indent=2)+'\n');raw,status,_=client.http_exchange('http://127.0.0.1:19417/mcp',json.dumps(q).encode(),60);(dest/f'{i:03d}-response.json').write_bytes(raw)
  assert status==200;result=json.loads(raw)['result'];assert not result.get('isError');data=result['structuredContent'];assert data['status']=='ok' and data['artifact_sha256']==r['identity'][1]
  if name=='compiler_definitions':
   assert len(data['results'])==1;hit=data['results'][0]
   assert hit['symbol']==symbol and hit['path']==path and hit['raw_sha256']==hashlib.sha256((Path('.local/fresh-paths/corpus/eShopOnWeb')/path).read_bytes()).hexdigest()
  else:
   assert 'di_open_generic_registration_configuration' in json.dumps(data) and row['path'] in json.dumps(data)
  calls.append({'role':role,'tool':name,'passed':True})
(dest/'report.json').write_text(json.dumps({'classification':'positional_open_registration_template_not_closed_runtime_resolution','calls':calls},indent=2)+'\n')
print('PASS: both qualified generic definitions and reverse registration context verified through public MCP')
