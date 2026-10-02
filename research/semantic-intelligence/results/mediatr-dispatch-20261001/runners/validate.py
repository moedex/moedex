from pathlib import Path
import os,json,subprocess
root=Path.cwd();base=root/'.local/mediatr-dispatch';env=dict(os.environ);commands=[]
for prefix,folder in [('FORWARD','forwarding'),('DEFAULT','selection'),('BRIDGE','bridge'),('RESPONSE_DI','response-di'),('REQUEST_RESPONSE','request-response'),('ENDPOINT','endpoint'),('MEDIATR','native')]:
 env['MOEDEX_'+prefix+'_STREAM']=str(base/folder/'capture.jsonl');env['MOEDEX_'+prefix+'_ROOT']=str(base/folder)
for label,args in [('full-test',['go','test','./...']),('vet',['go','vet','./...']),('race',['go','test','-race','./internal/semantic','./internal/semanticindex','./internal/semanticimport','./internal/mcp'])]:
 with (base/(label+'.log')).open('w') as f:r=subprocess.run(args,env=env,stdout=f,stderr=subprocess.STDOUT)
 commands.append({'label':label,'argv':args,'native_environment':{k:v for k,v in env.items() if k.startswith('MOEDEX_') and (k.endswith('_STREAM') or k.endswith('_ROOT'))},'returncode':r.returncode});(base/'validation-commands.json').write_text(json.dumps(commands,indent=2)+'\n');print(label,r.returncode,flush=True);r.check_returncode()
