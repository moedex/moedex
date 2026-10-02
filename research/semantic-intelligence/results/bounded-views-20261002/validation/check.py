import hashlib,json,sys
from pathlib import Path
sys.path.insert(0,'research/semantic-intelligence/agent-journeys')
import browse
root=Path('.local/journey-readiness/solvers')
checks=[]
for path in sorted(root.glob('*/*.response.raw')):
 raw=path.read_bytes();value=json.loads(raw);offset=0;parts=[];sizes=[]
 while True:
  wire=browse.page(raw,value,offset=offset);view=json.loads(wire)
  assert len(wire)<=8192
  sizes.append(len(wire));parts.append(view['data']);offset=view['next_offset']
  if offset is None: break
 assert json.loads(''.join(parts))==value
 assert path.read_bytes()==raw
 checks.append({'file':str(path.relative_to(root)),'sha256':hashlib.sha256(raw).hexdigest(),'raw_bytes':len(raw),'pages':len(parts),'largest_page_bytes':max(sizes)})
p=root/'impact-attendee/catalog.json';raw=p.read_bytes();v=browse.catalog_value(raw)
wire=browse.page(raw,v);assert json.loads(wire)['next_offset'] is None
names=v['tools'];descriptors=0
for name in names:
 value=browse.catalog_value(raw,name);offset=0;parts=[]
 while True:
  page=json.loads(browse.page(raw,value,offset=offset));parts.append(page['data']);offset=page['next_offset']
  if offset is None:break
 assert json.loads(''.join(parts))==value
 descriptors+=1
report={'classification':'scripted archived-response replay; not fresh agent quality','response_count':len(checks),'responses_raw_bytes':sum(x['raw_bytes'] for x in checks),'catalog_raw_bytes':len(raw),'catalog_names_view_bytes':len(wire),'complete_descriptors_reconstructed':descriptors,'responses':checks}
Path('.local/bounded-views/replay.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps({k:v for k,v in report.items() if k!='responses'},indent=2))
