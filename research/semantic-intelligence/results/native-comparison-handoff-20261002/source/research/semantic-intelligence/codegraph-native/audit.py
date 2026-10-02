#!/usr/bin/env python3
"""Offline, metadata-only CodeGraph prebuilt/source audit. Does not start hosts."""
import argparse,collections,hashlib,json,subprocess
from pathlib import Path

def sha(path):return hashlib.sha256(path.read_bytes()).hexdigest()
def audit(root,dotnet,probe,out):
 root=Path(root).resolve();out=Path(out);out.mkdir(parents=True,exist_ok=False)
 report={'schema':'codegraph-native-preflight-v1','ready':False,'classification':'read-only provenance audit; native setup not executed','arms':{}}
 for arm,project in [('api','TC.CodeGraphApi'),('indexer','TC.CodeGraphApi.Indexer.Host')]:
  bin=root/'src'/project/'bin/Debug/net10.0'
  argv=[str(dotnet),str(probe),str(root),str(bin)]
  result=subprocess.run(argv,capture_output=True,timeout=60)
  (out/(arm+'.json')).write_bytes(result.stdout);(out/(arm+'.stderr')).write_bytes(result.stderr)
  if result.returncode:raise RuntimeError(f'{arm} metadata probe failed: exit {result.returncode}')
  parsed=json.loads(result.stdout)
  assets=root/'src'/project/'obj/project.assets.json';a=json.loads(assets.read_text());roots=list(a['packageFolders']);packages=[]
  for name,v in a['libraries'].items():
   if v['type']!='package':continue
   packages.append({'identity':name,'private_tc':name.lower().startswith('tc.'),'recorded_cache_present':any((Path(r)/v['path']).is_dir() for r in roots),'default_cache_present':(Path.home()/'.nuget/packages'/v['path']).is_dir()})
  files=[{'path':str(p.relative_to(root)),'sha256':sha(p),'bytes':p.stat().st_size} for p in sorted(bin.iterdir()) if p.is_file() and p.suffix in ('.dll','.pdb','.json') and not p.name.startswith('appsettings')]
  docs=[d for assembly in parsed['assemblies'] for d in assembly.get('documents',[])];source=[d for d in docs if not d.get('generated')]
  report['arms'][arm]={'project':project,'probe_argv':argv,'assets_sha256':sha(assets),'packages':packages,'package_counts':{'all':len(packages),'private_tc':sum(p['private_tc'] for p in packages),'missing_recorded':sum(not p['recorded_cache_present'] for p in packages),'missing_default':sum(not p['default_cache_present'] for p in packages)},'binary_files':files,'assemblies':len(parsed['assemblies']),'pdb_linked_assemblies':sum(a.get('pdb_linked',False) for a in parsed['assemblies']),'source_documents':dict(collections.Counter(d['status'] for d in source)),'generated_documents':dict(collections.Counter(d['status'] for d in docs if d.get('generated'))),'source_mismatches':[d['relative'] for d in source if d['status']=='mismatch'],'source_missing':[d['relative'] for d in source if d['status']=='missing']}
 maps=[{Path(f['path']).name:f['sha256'] for f in report['arms'][a]['binary_files'] if f['path'].endswith('.dll')} for a in ('api','indexer')];left,right=maps
 report['shared_dlls']={'count':len(left.keys()&right.keys()),'different':[n for n in sorted(left.keys()&right.keys()) if left[n]!=right[n]]}
 report['probe_sha256']=sha(Path(probe))
 report['unexecuted_gates']=['Trusted reproducible build with complete dependency provenance.','Isolated native service configuration, schema and authentication.','Native initialization, catalog and indexing smoke on a frozen corpus.']
 report['limitations']=['PDB links and checksums establish consistency, not trusted build attestation.','Missing recorded/default package caches do not rule out alternative caches or an authorized feed.','Different DLL hashes alone do not establish incompatibility.','A clean metadata audit alone never sets ready=true.']
 (out/'setup.json').write_text(json.dumps(report,indent=2)+'\n');return report
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--checkout',required=True);p.add_argument('--dotnet',required=True);p.add_argument('--probe',required=True);p.add_argument('--output',required=True);a=p.parse_args()
 r=audit(a.checkout,a.dotnet,a.probe,a.output);print(json.dumps({'ready':r['ready'],'setup':str(Path(a.output)/'setup.json')}))
