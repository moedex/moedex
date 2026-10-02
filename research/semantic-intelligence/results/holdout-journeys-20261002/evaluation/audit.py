import collections,hashlib,json,sys
from pathlib import Path
base=Path(__file__).resolve().parent
sys.path.insert(0,str(Path('research/semantic-intelligence/agent-journeys').resolve()))
import browse
sha=lambda raw:hashlib.sha256(raw).hexdigest()
results=[]
for run in sorted((base/'solvers').iterdir()):
 if not (run/'answer.md').exists():continue
 state=json.loads((run/'state.json').read_text());events=[json.loads(l) for l in (run/'transcript.jsonl').read_text().splitlines()];requests=[e for e in events if e['event']=='request'];responses=[e for e in events if e['event']=='response'];tools=collections.Counter();errors=[];transport_errors=[]
 for e in events:
  if 'file' in e:assert sha((run/e['file']).read_bytes())==e['sha256']
 for e in requests:
  q=json.loads((run/e['file']).read_text());tools[q['params'].get('name',q['method'])]+=1
 for e in responses:
  raw=(run/e['file']).read_bytes();assert len(raw)==e['serialized_response_bytes'];d=json.loads(raw) if raw else {}
  if e['error'] or not e['response_complete']:transport_errors.append(e['ordinal'])
  if d.get('error') or d.get('result',{}).get('isError'):errors.append(e['ordinal'])
 assert len(requests)==len(responses)==state['calls'];assert sum(e['serialized_response_bytes'] for e in responses)==state['response_bytes'];assert state['pending'] is None
 views=[]
 for p in sorted((run/'views').glob('*.json')):
  v=json.loads(p.read_text());stdout=p.with_suffix('.stdout').read_bytes();stderr=p.with_suffix('.stderr').read_bytes()
  assert sha(stdout)==v['stdout_sha256'] and sha(stderr)==v['stderr_sha256'];assert len(stdout)==v['stdout_bytes']<=8192
  if stdout:
   d=json.loads(stdout);ctx=d['context'];raw=(run/'catalog.json').read_bytes() if 'catalog_tool' in ctx else (run/f"{ctx['ordinal']:03}.response.raw").read_bytes();assert sha(raw)==d['raw_sha256'] and len(raw)==d['raw_bytes']
   if 'catalog_tool' in ctx: value=browse.catalog_value(raw,ctx['catalog_tool'])
   else:
    value=json.loads(raw) if raw else {'unparsed_response':'','warning':'invalid JSON; raw retained, decoded display may be lossy'}
   value=browse.select(value,d['pointer']);canonical=json.dumps(value,ensure_ascii=True,separators=(',', ':'));end=d['offset']+len(d['data'])
   assert d['total_characters']==len(canonical) and canonical[d['offset']:end]==d['data']
   assert d['next_offset']==(end if end<len(canonical) else None)
   assert d['partial']==(d['offset']!=0 or end!=len(canonical))
  views.append({'ordinal':v['ordinal'],'argv':v['argv'],'returncode':v['returncode'],'stdout_bytes':len(stdout),'stderr_bytes':len(stderr)})
 results.append({'task':run.name,'calls':state['calls'],'response_bytes':state['response_bytes'],'native_errors':errors,'transport_errors':transport_errors,'tools':dict(tools),'views':views,'ledger_and_view_hashes_verified':True,'all_rpc_responses_complete':all(e['response_complete'] for e in responses),'transport_accounting_known':all(e['error'] is None and e['response_complete'] for e in responses),'local_validation_events':[e for e in events if e['event'] not in ('request','response')]})
(base/'accounting-audit.json').write_text(json.dumps(results,indent=2)+'\n');print(json.dumps([{k:v for k,v in r.items() if k not in ('views','local_validation_events')} for r in results],indent=2))
