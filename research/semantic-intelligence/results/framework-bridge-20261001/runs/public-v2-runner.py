#!/usr/bin/env python3
"""Source-authored eShop discovery regression; no independent agent score."""
import argparse
import json
from pathlib import Path
import client
from check_discovery_workflow import raw_position

p=argparse.ArgumentParser()
p.add_argument('--endpoint',required=True)
p.add_argument('--output',type=Path,required=True)
a=p.parse_args();a.output.mkdir(parents=True,exist_ok=False)
request={'jsonrpc':'2.0','id':1,'method':'tools/list','params':{}}
raw,status,_=client.http_exchange(a.endpoint,json.dumps(request).encode(),30)
assert status==200
(a.output/'catalog.raw').write_bytes(raw)
catalog=a.output/'catalog.json';catalog.write_text(json.dumps(json.loads(raw)['result']['tools'],indent=2))
reports=[]
for event in ['OrderStatusChangedToPaidIntegrationEvent','OrderStatusChangedToShippedIntegrationEvent','ProductPriceChangedIntegrationEvent','default-template']:
 prompt=a.output/(event+'.txt');prompt.write_text('Inspect the recorded handler correspondence and keyed configuration for '+event)
 directory=a.output/event;client.initialize(directory,a.endpoint,prompt,catalog)
 identities=set()
 def call(name,args):
  raw,accounting=client.request(directory,'tools/call',{'name':name,'arguments':args})
  assert accounting['response_complete'] and accounting['http_status']==200
  wire=json.loads(raw);assert 'error' not in wire and not wire['result'].get('isError'),wire
  result=wire['result']['structuredContent'];assert not result.get('truncated'),result
  if name.startswith('compiler_'):identities.add((result['snapshot_id'],result['artifact_sha256']))
  return result
 def find(subject):
  result=call('search_context',{'repo':'eShop','query':subject,'top_k':5,'token_budget':2200})
  return result['blocks']
 if event!='default-template':
  handler=event+'Handler';blocks=find(handler)
  paths=sorted({b['rel_path'] for b in blocks if b['rel_path'].endswith('/'+handler+'.cs')})
  alternatives=[]
  for path in paths:
   symbols=call('compiler_symbols',{'repo':'eShop','path':path,'query':'.Handle('})
   alternatives.extend(symbols['results'])
  assert len(alternatives)==1,alternatives
  decl=alternatives[0];facts=decl['implementation_facts'];assert len(facts)==1 and facts[0]['rule']=='csharp-interface-closed-v1',decl
  iface=next(s for s in decl['implementation_symbols'] if s['id']==facts[0]['interface_symbol_id'])
  constructed=json.loads(iface['descriptor']);assert constructed['arguments'][0]['descriptor']=='T:Webhooks.API.IntegrationEvents.'+event
  choices=call('compiler_implementations',{'symbol_id':iface['id']})
  assert len(choices['contexts'])==1,choices
  reverse=call('compiler_implementations',{'symbol_id':iface['id'],'context_ids':[choices['contexts'][0]['context_id']]})
  assert len(reverse['matches'])==1 and reverse['matches'][0]['source']['symbol']==decl['symbol'],reverse
  blocks=find('AddSubscription '+handler)
  paths=sorted({b['rel_path'] for b in blocks if 'AddSubscription' in b['text'] and handler in b['text']})
  alternatives=[]
  for path in paths:
   contexts=call('compiler_binding_at',{'repo':'eShop','path':path,'byte_offset':0})
   alternatives.extend((path,c) for c in contexts['contexts'])
  assert len(alternatives)==1,alternatives
  config_path,context=alternatives[0]
  source=call('read_source',{'repo':'eShop','path':config_path})
  assert source['start_line']==1 and source['end_line']==source['lines']
  offset,bom=raw_position(source['content'],context['raw_sha256'],'AddSubscription<'+event+', '+handler+'>','AddSubscription')
  binding=call('compiler_binding_at',{'repo':'eShop','path':config_path,'byte_offset':offset,'context_id':context['context_id'],'raw_sha256':context['raw_sha256']})
  assert len(binding['results'])==1,binding
  config=binding['results'][0];fact=config['domain_facts'][0]
  assert fact['kind']=='di_keyed_registration_configuration' and fact['lifetime']=='transient',fact
  targets={t['role']:t['symbol'] for t in fact['targets']}
  assert targets['key_type']['descriptor']=='T:Webhooks.API.IntegrationEvents.'+event
  assert targets['implementation']['descriptor']=='T:Webhooks.API.IntegrationEvents.'+handler
  assert targets['service']['descriptor']=='T:eShop.EventBus.Abstractions.IIntegrationEventHandler'
  assert targets['registration_api']['namespace_kind']=='assembly'
  definition=call('compiler_definitions',{'symbol_id':config['symbol']['id']})
  assert len(definition['results'])==1,definition
  body=call('read_source',{'repo':'eShop','path':definition['results'][0]['path']})
  assert 'AddKeyedTransient<IIntegrationEventHandler, TH>(typeof(T))' in body['content']
  impact=call('compiler_contract_impact',{'symbol_id':targets['key_type']['id']})
  selected=call('compiler_contract_impact',{'symbol_id':targets['key_type']['id'],'context_ids':[c['context_id'] for c in impact['contexts']]})
  assert any(p['fact']['kind']=='di_keyed_registration_configuration' and 'key_type' in p['matched_roles'] for p in selected['paths']),selected
  evidence={'interface_symbol':iface,'implementation':decl['symbol'],'configuration':fact,'helper_definition':definition['results'][0],'configuration_offset':offset,'verified_bom_bytes':bom}
 else:
  blocks=find('IIntegrationEventHandler')
  paths={b['rel_path'] for b in blocks if b['rel_path'].endswith('/IIntegrationEventHandler.cs')};assert len(paths)==1,paths
  path=paths.pop();source=call('read_source',{'repo':'eShop','path':path})
  symbols=call('compiler_symbols',{'repo':'eShop','path':path,'query':'Handle'})
  defaults=[r for r in symbols['results'] if any(f['rule']=='csharp-interface-default-v1' for f in r.get('implementation_facts',[]))]
  assert len(defaults)==1,defaults
  decl=defaults[0];fact=decl['implementation_facts'][0]
  choices=call('compiler_implementations',{'symbol_id':fact['interface_symbol_id']})
  selected=call('compiler_implementations',{'symbol_id':fact['interface_symbol_id'],'context_ids':[c['context_id'] for c in choices['contexts']]})
  assert len(selected['matches'])==1,selected
  assert 'Handle((TIntegrationEvent)@event)' in source['content']
  offset,bom=raw_position(source['content'],decl['raw_sha256'],'Handle((TIntegrationEvent)@event)','Handle')
  forward=call('compiler_binding_at',{'repo':'eShop','path':path,'context_id':decl['context_id'],'raw_sha256':decl['raw_sha256'],'byte_offset':offset})
  assert forward['results'][0]['symbol']['descriptor']=='M:eShop.EventBus.Abstractions.IIntegrationEventHandler`1.Handle(`0)',forward
  evidence={'default_declaration':decl,'forwarding_call':forward['results'][0],'boundary':'Open generic default template and recorded cast/call, not closed class dispatch or proof of execution.'}
 assert len(identities)==1
 state=json.loads((directory/'state.json').read_text());assert not state['stopped'],state
 report={'case':event,'classification':'source_authored_public_workflow_not_independent_agent_score','passed':True,'calls':state['calls'],'response_bytes':state['response_bytes'],'compiler_identity':list(next(iter(identities))),'evidence':evidence}
 client.atomic_json(directory/'report.json',report);reports.append(report);print(event,report['calls'],report['response_bytes'],flush=True)
client.atomic_json(a.output/'report.json',reports)
