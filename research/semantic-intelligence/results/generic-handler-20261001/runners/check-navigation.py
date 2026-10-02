import json,sys
from pathlib import Path
sys.path.insert(0,str(Path('research/semantic-intelligence/agent-journeys').resolve()))
import client
base=Path('.local/generic-handler');dest=base/'navigation';dest.mkdir()
r=json.loads((base/'eShopOnWeb-public/report.json').read_text());cases={x['id']:x for x in r['expectations']};calls=[]
row=cases['orders-handler']['bindings'][0];fact=row['implementation_facts'][0]
iface=next(s for s in row['implementation_symbols'] if s['id']==fact['interface_symbol_id'])
value=json.loads(iface['descriptor'])
assert iface['descriptor_kind']=='constructed_interface_method_v2'
assert value['definition']=='M:MediatR.IRequestHandler`2.Handle(`0,System.Threading.CancellationToken)'
dispatch=cases['mediator-orders']['bindings'][0]['domain_facts'][0]
for argument,target in zip(value['arguments'],dispatch['targets']):
 assert argument=={k:v for k,v in target['symbol'].items() if k!='id'}
for name,args in [('compiler_implementations',{'symbol_id':iface['id']}),('compiler_implementations',{'symbol_id':iface['id'],'context_ids':[row['context_id']]}),('compiler_definitions',{'symbol_id':row['symbol']['id'],'repo':'eShopOnWeb'})]:
 q={'jsonrpc':'2.0','id':len(calls)+1,'method':'tools/call','params':{'name':name,'arguments':args}};i=len(calls)+1
 (dest/f'{i:03d}-request.json').write_text(json.dumps(q,indent=2)+'\n');raw,status,_=client.http_exchange('http://127.0.0.1:19407/mcp',json.dumps(q).encode(),60);(dest/f'{i:03d}-response.json').write_bytes(raw)
 assert status==200;result=json.loads(raw)['result'];assert not result.get('isError');data=result['structuredContent'];assert data['artifact_sha256']==r['identity'][1]
 if i==1:assert data['status']=='context_required' and len(data['contexts'])==1
 elif i==2:
  assert data['status']=='ok' and len(data['matches'])==1
  match=data['matches'][0]
  assert row['symbol']['id'] in json.dumps(match) and row['path'] in json.dumps(match)
 else:
  assert data['status']=='ok' and len(data['results'])==1
  hit=data['results'][0];assert hit['symbol']==row['symbol'] and hit['path']==row['path'] and hit['byte_offset']==624 and hit['raw_sha256']==row['raw_sha256']
 calls.append({'tool':name,'passed':True})
(dest/'report.json').write_text(json.dumps({'classification':'compiler_correspondence_not_runtime_handler_selection','dispatch_argument_keys_equal':True,'interface_symbol':iface,'calls':calls},indent=2)+'\n')
print('PASS: dispatch arguments equal closed handler arguments; reverse correspondence and exact source definition verified')
