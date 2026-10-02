import hashlib,json
from pathlib import Path
base=Path(__file__).resolve().parent;source=base.parent/'capture-input/CleanArchitecture';checked=[];identities=set()
def verify(repo,path,start,end,content,label):
 assert repo=='CleanArchitecture'
 raw=(source/path).read_bytes();text=raw.decode('utf-8-sig');lines=text.split('\n');want='\n'.join(lines[start-1:end])
 # Search excerpts and read_source differ only in whether final LF is retained.
 assert content.replace('\r\n','\n').rstrip('\r\n')==want.replace('\r\n','\n').rstrip('\r\n'),(label,path,start,end)
 checked.append({'response':label,'path':path,'start_line':start,'end_line':end,'source_sha256':hashlib.sha256(raw).hexdigest()})
for run in sorted((base/'solvers').iterdir()):
 for path in sorted(run.glob('*.response.raw')):
  raw=path.read_bytes()
  if not raw:continue
  d=json.loads(raw).get('result',{});meta=d.get('_meta',{}).get('dev.moedex/snapshot',{})
  if meta.get('corpus_fingerprint'):identities.add(meta['corpus_fingerprint'])
  s=d.get('structuredContent',{});label=str(path.relative_to(base))
  if all(k in s for k in ('repo','path','start_line','end_line','content')):verify(s['repo'],s['path'],s['start_line'],s['end_line'],s['content'],label)
  for b in s.get('blocks',[]):
   # Search block schema is verified against the actual public return shape.
   assert all(k in b for k in ('repo','rel_path','start_line','end_line','text')),label
   verify(b['repo'],b['rel_path'],b['start_line'],b['end_line'],b['text'],label)
assert len(identities)<=1
report={'normalization':'UTF-8 BOM removed, CRLF normalized, terminal line separators ignored; raw file hashes retained','source_windows_verified':len(checked),'fingerprints':sorted(identities),'windows':checked}
(base/'source-audit.json').write_text(json.dumps(report,indent=2)+'\n');print('source windows verified',len(checked),'fingerprints',len(identities))
