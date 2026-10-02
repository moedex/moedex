import hashlib,json,shutil,subprocess
from pathlib import Path
r=Path('.references/CodeGraph');out=Path('.local/holdout-cleanarchitecture/readiness')
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
rows=[]
for host in ['TC.CodeGraphApi','TC.CodeGraphApi.Indexer.Host']:
 p=r/'src'/host/'obj/project.assets.json';d=json.loads(p.read_text());roots=[Path(x) for x in d.get('packageFolders',{})]
 packages=[(name,v) for name,v in d['libraries'].items() if v.get('type')=='package']
 missing=[name for name,v in packages if not any((root/v['path']).exists() for root in roots)]
 binpath=r/'src'/host/'bin/Debug/net10.0'
 rows.append({'host':host,'assets_sha256':sha(p),'recorded_package_roots':[str(p) for p in roots],'package_count':len(packages),'missing_at_recorded_roots':missing,'private_package_count':sum(n.lower().startswith('tc.') for n,v in packages),'prebuilt_dlls':[{'name':f.name,'sha256':sha(f)} for f in sorted(binpath.glob('*.dll'))],'prebuilt_provenance':'Source-to-binary match not verified; copied build outputs are not a reproducible build'})
listeners={}
for port in [3306,8500,5037,5042]:
 p=subprocess.run(['lsof','-nP',f'-iTCP:{port}','-sTCP:LISTEN'],capture_output=True,text=True)
 listeners[str(port)]={'listener_visible':p.returncode==0,'probe_exitcode':p.returncode}
files=[]
for p in sorted(r.rglob('*')):
 if p.is_file() and p.suffix in ('.cs','.csproj') and not any(x in ('bin','obj','.git','node_modules') for x in p.relative_to(r).parts):files.append({'path':str(p.relative_to(r)),'sha256':sha(p)})
report={'classification':'Read-only local setup inventory, not an execution failure or product comparison','hosts':rows,'commands_available':{x:shutil.which(x) for x in ['mysql','mysqld','consul','docker']},'visible_listeners':listeners,'current_user_private_package_directories':len(list((Path.home()/'.nuget/packages').glob('tc.*'))),'source_files':files,'requirements':[{'path':'src/TC.CodeGraphApi/Program.cs','facts':['Removes appsettings JSON configuration sources','Startup uses TcServiceStartup','Hardcoded IPAddress.Any port 5037','MCP endpoint requires PAT authorization','Background missing-MCP-doc generation starts on boot']},{'path':'README.md','facts':['Consul configuration and MySQL storage required','Database migrations are explicit','Indexer separate host on port 5042']}],'status':'paired arm not ready: restored dependency paths unavailable; reproducible build, isolated service configuration, database schema, authentication and binary provenance not established','not_attempted':['No service start','No database connections or mutations','No credentials read or private feeds accessed','No holdout source indexed or queried','No substitute component host counted as product']}
(out/'codegraph-readiness.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps({'hosts':[{'host':x['host'],'missing_packages':len(x['missing_at_recorded_roots']),'private_packages':x['private_package_count'],'prebuilt_dlls':len(x['prebuilt_dlls'])} for x in rows],'commands_available':report['commands_available'],'visible_listeners':listeners,'reference_source_files_hashed':len(files)},indent=2))
