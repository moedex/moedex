import datetime,json,pathlib,sys,time
p=pathlib.Path('.local/journey-readiness/execution.json');d=json.loads(p.read_text());action,task=sys.argv[1:3]
if action=='start':
 assert not any(x['task']==task for x in d['runs'])
 d['runs'].append({'task':task,'assigned_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'assigned_monotonic':time.monotonic()})
elif action=='finish':
 r=next(x for x in d['runs'] if x['task']==task);r['completion_observed_utc']=datetime.datetime.now(datetime.timezone.utc).isoformat();r['completion_observed_monotonic']=time.monotonic();r['elapsed_upper_bound_seconds']=r['completion_observed_monotonic']-r['assigned_monotonic'];r['within_deadline']=r['elapsed_upper_bound_seconds']<=600
 state=json.loads((pathlib.Path('.local/journey-readiness/solvers')/task/'state.json').read_text());r['accounting']={k:state[k] for k in ('calls','response_bytes','stopped','pending')};print(json.dumps(r,indent=2))
p.write_text(json.dumps(d,indent=2)+'\n')
