import collections,json
from pathlib import Path
b=Path(__file__).resolve().parent;report={}
for run in sorted((b/'solvers').iterdir()):
 groups=collections.defaultdict(list)
 for p in sorted((run/'views').glob('*.stdout')):
  raw=p.read_bytes()
  if not raw:continue
  d=json.loads(raw);c=d['context']
  if 'catalog_tool' in c:continue
  groups[(c['ordinal'],d['pointer'],d['total_characters'])].append((d['offset'],d['offset']+len(d['data']),p.name))
 rows=[]
 for (ordinal,pointer,total),spans in sorted(groups.items()):
  end=0;complete=True
  for start,stop,_ in sorted(spans):
   if start>end:complete=False
   end=max(end,stop)
  rows.append({'response_ordinal':ordinal,'pointer':pointer,'selected_value_fully_displayed':complete and end==total,'total_characters':total,'pages':[{'start':start,'end_exclusive':stop,'view':view} for start,stop,view in spans]})
 report[run.name]=rows
(b/'view-coverage.json').write_text(json.dumps({'scope':'Recorded output coverage, not proof of model attention. Complete selected value does not imply whole response coverage.','tasks':report},indent=2)+'\n')
print('coverage task records',len(report))
