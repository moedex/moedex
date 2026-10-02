import json,sys,hashlib
from pathlib import Path
sys.path.insert(0,str(Path('research/semantic-intelligence/agent-journeys').resolve()))
import client
base=Path('.local/routing-slip');dest=base/'navigation';dest.mkdir()
r=json.loads((base/'ForkJoint-public/report.json').read_text());row=next(x for x in r['expectations'] if x['id']=='routing-slip')['bindings'][0];f=row['domain_facts'][0];calls=[]
assert f['kind']=='routing_slip_activity_configuration' and f['rule']=='csharp-routing-slip-v1'
def call(name,args):
 q={'jsonrpc':'2.0','id':len(calls)+1,'method':'tools/call','params':{'name':name,'arguments':args}};i=len(calls)+1
 (dest/f'{i:03d}-request.json').write_text(json.dumps(q,indent=2)+'\n');raw,status,_=client.http_exchange('http://127.0.0.1:19448/mcp',json.dumps(q).encode(),60);(dest/f'{i:03d}-response.json').write_bytes(raw)
 assert status==200;result=json.loads(raw)['result'];assert not result.get('isError');data=result['structuredContent'];assert data['status']=='ok' and data['artifact_sha256']==r['identity'][1]
 calls.append({'tool':name,'passed':True});return data
expected=[('activity','T:ForkJoint.Api.Components.Activities.GrillBurgerActivity','src/ForkJoint.Api/Components/Activities/GrillBurgerActivity.cs'),('arguments','T:ForkJoint.Api.Components.Activities.GrillBurgerArguments','src/ForkJoint.Api/Components/Activities/GrillBurgerArguments.cs'),('address_field','F:ForkJoint.Api.Components.ItineraryPlanners.BurgerItineraryPlanner._grillAddress',row['path']),('formatter_api','M:MassTransit.IEndpointNameFormatter.ExecuteActivity``2',None)]
assert len(f['targets'])==4
for target,(role,descriptor,path) in zip(f['targets'],expected):
 symbol=target['symbol'];assert target['role']==role and symbol['descriptor']==descriptor
 if path:
  data=call('compiler_definitions',{'symbol_id':symbol['id'],'repo':'Sample-ForkJoint'});assert len(data['results'])==1;hit=data['results'][0]
  assert hit['symbol']==symbol and hit['path']==path and hit['raw_sha256']==hashlib.sha256((Path('.local/fresh-paths/corpus/Sample-ForkJoint')/path).read_bytes()).hexdigest()
 for name in ['compiler_contract_impact','compiler_contract_context']:
  data=call(name,{'symbol_id':symbol['id'],'context_ids':[row['context_id']]});assert f['kind'] in json.dumps(data) and row['path'] in json.dumps(data)
source=(Path('.local/fresh-paths/corpus/Sample-ForkJoint')/row['path']).read_bytes();offset=source.rindex(b'builder.AddActivity')+len(b'builder.')
other=call('compiler_binding_at',{'repo':'Sample-ForkJoint','path':row['path'],'raw_sha256':row['raw_sha256'],'byte_offset':offset,'context_id':row['context_id']})['results'][0]['domain_facts'][0]
assert other['kind']==f['kind']
assert other['targets'][0]['symbol']['descriptor']=='T:ForkJoint.Api.Components.Activities.DressBurgerActivity'
assert other['targets'][1]['symbol']['descriptor']=='T:ForkJoint.Api.Components.Activities.DressBurgerArguments'
assert other['targets'][2]['symbol']['descriptor'].endswith('._dressAddress')
assert other['targets'][3]['symbol']==f['targets'][3]['symbol']
(dest/'report.json').write_text(json.dumps({'classification':'bounded_constructor_witness_not_runtime_route_execution','sibling_activity_distinct':True,'calls':calls},indent=2)+'\n')
print('PASS: exact routing targets, source definitions, reverse context, and distinct sibling activity verified')
