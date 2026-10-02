import pathlib,json,hashlib,collections
base=pathlib.Path('.local/journey-readiness');out=[]
for p in sorted((base/'solvers').iterdir()):
 if not (p/'answer.md').exists():continue
 state=json.loads((p/'state.json').read_text());events=[json.loads(x) for x in (p/'transcript.jsonl').read_text().splitlines()];responses=[e for e in events if e['event']=='response'];requests=[e for e in events if e['event']=='request'];errors=[];tools=collections.Counter()
 for e in events:
  if 'file' in e:assert hashlib.sha256((p/e['file']).read_bytes()).hexdigest()==e['sha256'],(p,e)
 for e in requests:
  q=json.loads((p/e['file']).read_text());tools[q['params'].get('name',q['method'])]+=1
 for e in responses:
  raw=(p/e['file']).read_bytes();assert len(raw)==e['serialized_response_bytes'];assert e['response_complete'];d=json.loads(raw)
  if e['error'] or d.get('error') or d.get('result',{}).get('isError'):errors.append(e['ordinal'])
 assert len(requests)==state['calls']==len(responses);assert sum(e['serialized_response_bytes'] for e in responses)==state['response_bytes'];assert state['pending'] is None
 out.append({'task':p.name,'calls':state['calls'],'response_bytes':state['response_bytes'],'errors':errors,'tools':dict(tools),'ledger_hashes_verified':True,'all_responses_complete':True,'local_validation_events':[e for e in events if e['event'] not in ('request','response')]})
(base/'accounting-audit.json').write_text(json.dumps(out,indent=2)+'\n');print(json.dumps(out,indent=2))
