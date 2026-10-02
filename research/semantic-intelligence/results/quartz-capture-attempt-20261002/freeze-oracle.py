import datetime,hashlib,json,shutil
from pathlib import Path
base=Path('.local/holdout-quartz');ev=base/'evaluation';packet=base/'packet';source=base/'source'
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def save(n,v):(ev/n).write_text(json.dumps(v,indent=2)+'\n')
r=json.loads((base/'oracle-review.json').read_text());g=json.loads((packet/'source-gold.json').read_text());lookup={a['id']:a for t in r['tasks'] for a in t['atoms']};decisions=[]
for t in g['tasks']:
 for a in t['atoms']:
  review=lookup[a['id']]
  if review['verdict']=='accept':continue
  before=dict(a);a['requirement']=review['proposed_requirement'];a['evidence']=[]
  for e in review['proposed_evidence']:
   p=source/e['path'];assert sha(p)==e['raw_sha256'];a['evidence'].append(dict(e,excerpt='\n'.join(p.read_text(encoding='utf-8-sig').splitlines()[e['start_line']-1:e['end_line']])))
  decisions.append(dict(atom=a['id'],decision='accept',reason=review['finding'],before=before,after=a))
g.update(status='independently reviewed source oracle; coordinator accepted 10 amendments before capture and queries',scoring={'correctness':r['scoring_review']['proposed_correctness'],'evidence':r['scoring_review']['proposed_evidence'],'strict':'Per-task: every scored atom correctness=1 and evidence=1, no material unsupported claim, static/runtime distinction preserved, budgets respected. Corpus strict success requires all six tasks strict. Documentation alone does not establish implementation or execution.'},scope_guards=r['scope_review']['notes'])
p=json.loads((packet/'protocol.json').read_text());p.update(version=2,status='reviewed oracle frozen before capture',execution='User explicitly authorized one oracle reviewer, six fresh solvers and six fresh answer reviewers, sequentially with only one agent active at a time. The coordinator is not a blind solver.',reviewed_gold_sha256=None)
save('source-gold-reviewed.json',g);p['reviewed_gold_sha256']=sha(ev/'source-gold-reviewed.json');save('protocol-reviewed.json',p);save('oracle-amendments.json',dict(timestamp_utc=datetime.datetime.now(datetime.timezone.utc).isoformat(),product_queries=0,capture_attempted=False,decisions=decisions,scoring_decision='Accept explicit partial-credit and normative-claim rules; clarify strict scoring per task and corpus. Prompts unchanged.'))
shutil.copy2(base/'oracle-review.json',ev/'oracle-review.json')
save('pre-capture-freeze.json',{n:sha(ev/n) for n in ('source-gold-reviewed.json','protocol-reviewed.json','oracle-amendments.json','oracle-review.json','authorization.json','capture.py','manage.py')})
print(json.dumps(dict(amendments=len(decisions),gold_sha256=sha(ev/'source-gold-reviewed.json'))))
