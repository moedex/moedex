import pathlib,json,hashlib
base=pathlib.Path('.local/journey-readiness');source=pathlib.Path('.local/application-impact/Sample-Outbox');report=[]
for run in sorted((base/'solvers').iterdir()):
 if not (run/'answer.md').exists():continue
 spans=[]
 for p in sorted(run.glob('*.response.raw')):
  s=json.loads(p.read_text()).get('result',{}).get('structuredContent',{})
  blocks=s.get('blocks',[])
  if 'path' in s and 'content' in s:blocks=blocks+[{'rel_path':s['path'],'start_line':s['start_line'],'end_line':s['end_line'],'text':s['content']}]
  for b in blocks:
   f=source/b['rel_path'];actual=f.read_text(encoding='utf-8-sig').splitlines();expected='\n'.join(actual[b['start_line']-1:b['end_line']]);assert expected.rstrip('\n')==b['text'].rstrip('\n'),(run,p,b['rel_path'],b['start_line']);spans.append({'response':p.name,'path':b['rel_path'],'start_line':b['start_line'],'end_line':b['end_line']})
 report.append({'task':run.name,'source_blocks_verified':len(spans),'spans':spans})
(base/'returned-source-audit.json').write_text(json.dumps(report,indent=2)+'\n');print('Verified',sum(x['source_blocks_verified'] for x in report),'returned source blocks against frozen checkout across',len(report),'completed tasks.')
