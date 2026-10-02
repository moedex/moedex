from pathlib import Path
import subprocess,json
root=Path.cwd();base=root/'.local/endpoint-mapping';rows=[]
for script,name in [('test_default_forwarding.py','forwarding'),('test_default_selection.py','selection'),('test_framework_bridge.py','bridge'),('test_response_di.py','response-di'),('test_request_response.py','request-response')]:
 argv=['python3',str(root/'tools/semantic-dotnet'/script),'--dotnet',str(root/'.local/application-impact/independent/dotnet8/dotnet'),'--sdk',str(root/'.local/application-impact/independent/dotnet8/sdk/8.0.400'),'--worker',str(base/'worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll'),'--packages',str(root/'.local/fresh-paths/ForkJoint-complete-bundle/packages'),'--output',str(base/name)]
 with (base/(name+'.log')).open('w') as f:r=subprocess.run(argv,stdout=f,stderr=subprocess.STDOUT,timeout=360)
 rows.append({'argv':argv,'returncode':r.returncode});(base/'native-commands.json').write_text(json.dumps(rows,indent=2)+'\n');print(name,r.returncode,flush=True);r.check_returncode()
