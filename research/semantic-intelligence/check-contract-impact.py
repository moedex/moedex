#!/usr/bin/env python3
"""Real multi-project contract fixture capture; no runtime messaging/storage claims."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess

p=argparse.ArgumentParser()
p.add_argument('--dotnet',required=True);p.add_argument('--sdk-path',required=True)
p.add_argument('--worker',required=True);p.add_argument('--packages',required=True)
p.add_argument('--output-dir',required=True)
a=p.parse_args()
base=Path(a.output_dir).resolve();base.mkdir(parents=True,exist_ok=True)
repo=Path(__file__).resolve().parents[2];root=base/'fixture'
if root.exists(): raise SystemExit('use fresh output directory')
shutil.copytree(repo/'internal/eval/testdata/semantic-intelligence/contract-impact-v1',root)
env=dict(os.environ,DOTNET_ROOT=str(Path(a.dotnet).parent),DOTNET_CLI_HOME=str(base/'home'),NUGET_PACKAGES=a.packages,DOTNET_NOLOGO='1',DOTNET_CLI_TELEMETRY_OPTOUT='1',DOTNET_SKIP_FIRST_TIME_EXPERIENCE='1',MSBUILDDISABLENODEREUSE='1')
def run(label,args):
 r=subprocess.run([a.dotnet,*args],env=env,capture_output=True,timeout=240)
 (base/(label+'.stdout')).write_bytes(r.stdout);(base/(label+'.stderr')).write_bytes(r.stderr)
 assert r.returncode==0,(label,r.returncode,r.stdout[-3000:],r.stderr[-3000:])
 return r.stdout
run('restore',['restore',str(root/'Aggregate/Aggregate.csproj'),'--nologo'])
expected={'Publisher/Producer.cs':('message_publish','contract-gold/Contracts/Contracts.csproj'), 'Consumer/Consumer.cs':('message_consumer','contract-gold/Contracts/Contracts.csproj'), 'Storage/Store.cs':('storage_entity','contract-gold/Contracts/Contracts.csproj'), 'Distractor/Distractor.cs':('message_publish','contract-gold/Distractor/Distractor.csproj')}
reports={}
for config in ['Debug','Release']:
 output=run(config,[a.worker,'--repo','contract-gold','--root',str(root),'--project','Aggregate/Aggregate.csproj','--framework','net10.0','--sdk-path',a.sdk_path,'--configuration',config])
 (base/(config+'.jsonl')).write_bytes(output)
 rows=[json.loads(x) for x in output.splitlines()]
 assert rows[-1]['record_type']=='stream_summary' and rows[-1]['compilation_status']=='complete'
 assert rows[-1]['projects']==6
 facts=[(r,f) for r in rows for f in (r.get('domain_facts') or [])]
 assert len(facts)==5,(config,facts)
 for path,(kind,namespace) in expected.items():
  matching=[(r,f) for r,f in facts if r['source_path']==path and f['kind']==kind]
  assert len(matching)==1,(path,matching)
  row,fact=matching[0];target=fact['targets'][0]['symbol']
  assert target['descriptor']=='T:Shared.Notice' and target['namespace']==namespace,target
  content=(root/path).read_bytes();assert row['source_sha256']==hashlib.sha256(content).hexdigest()
  marker='publish-'+config.lower() if path.startswith('Publisher') else {'Consumer/Consumer.cs':'consumer','Storage/Store.cs':'entity','Distractor/Distractor.cs':'distractor'}[path]
  token=('/*gold:'+marker+'*/').encode();assert row['span']['byte_offset']==content.index(token)+len(token)
 table=[f for _,f in facts if f['kind']=='storage_table'];assert len(table)==1 and table[0]['table']=='notices' and table[0]['schema']=='app'
 reports[config]={'contexts':{r['project']:r['build_context'] for r in rows if r['record_type']=='project'},'facts':len(facts),'stream_sha256':hashlib.sha256(output).hexdigest()}
assert reports['Debug']['contexts']['Publisher/Publisher.csproj']!=reports['Release']['contexts']['Publisher/Publisher.csproj']
result={'status':'passed','worker_sha256':hashlib.sha256(Path(a.worker).read_bytes()).hexdigest(),'reports':reports}
(base/'capture-gold.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result,indent=2))
print(f'MOEDEX_CONTRACT_CAPTURE_DIR={base} go test ./internal/semanticimport -run TestPublicContractImpact -count=1 -v')
