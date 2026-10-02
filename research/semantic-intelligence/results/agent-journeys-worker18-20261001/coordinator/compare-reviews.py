import pathlib,json,hashlib
b=pathlib.Path('.local/journey-readiness');rows=[]
for p in sorted((b/'reviews').glob('*.independent.json')):
 d=json.loads(p.read_text());r=json.loads(p.with_name(p.name.replace('.independent.json','.root.json')).read_text());assert d['answer_sha256']==r['answer_sha256']==hashlib.sha256((b/'solvers'/d['task']/'answer.md').read_bytes()).hexdigest();a={x['id']:x for x in d['atoms']};assert set(a)=={x['id'] for x in r['atoms']}
 diffs=[]
 for x in r['atoms']:
  y=a[x['id']];assert y['correctness'] in (0,0.5,1) and y['evidence'] in (0,0.5,1)
  if (x['correctness'],x['evidence'])!=(y['correctness'],y['evidence']):diffs.append({'id':x['id'],'root':x,'independent':y})
 rows.append({'task':d['task'],'correctness':sum(x['correctness'] for x in a.values()),'evidence':sum(x['evidence'] for x in a.values()),'strict_success':d['strict_success'],'differences':diffs,'unsupported_claims':d['unsupported_claims']})
(b/'review-comparison.json').write_text(json.dumps(rows,indent=2)+'\n');print(json.dumps(rows,indent=2))
