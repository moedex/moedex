import json,os,shutil,subprocess
from pathlib import Path
root=Path.cwd();local=root/'.local/fresh-paths'
for repo in ['eShopOnWeb','Sample-ForkJoint']:
 paths=set(json.loads((local/(repo+'-packages.json')).read_text()))
 for p in (local/'provision'/repo).rglob('project.assets.json'):
  a=json.loads(p.read_text())
  for f in a['project']['frameworks'].values():
   for d in f.get('downloadDependencies',[]):paths.add(d['name'].lower()+'/'+d['version'].strip('[]').split(',')[0].strip())
 dest=local/(repo+'-packages');dest.mkdir(exist_ok=False)
 for p in sorted(paths): shutil.copytree(local/'packages'/p,dest/p,copy_function=os.link)
 with (local/(repo+'-pack.log')).open('w') as f:
  r=subprocess.run([str(root/'.local/default-forwarding/moedex-final'),'semantic','dependencies','pack','--packages',str(dest),'--output',str(local/(repo+'-bundle'))],stdout=f,stderr=subprocess.STDOUT)
 print(repo,r.returncode,flush=True)
