#!/usr/bin/env python3
"""Freeze/verify reference source identity without copying source or secrets."""
import argparse,hashlib,json,os
from pathlib import Path

EXCLUDE={'bin','obj','.git','.tmp','node_modules','__pycache__'}
ROOT_FILES={'global.json','nuget.config','nuget.docker.config','Directory.Build.props','Directory.Build.targets','Directory.Packages.props'}

def roster(root):
 root=Path(root).resolve();paths=[]
 for folder in ('src','sql'):
  start=root/folder
  if start.is_symlink():raise ValueError('symlink source root')
  if not start.is_dir():continue
  for parent,dirs,files in os.walk(start,followlinks=False):
   for name in dirs:
    if name not in EXCLUDE and (Path(parent)/name).is_symlink():raise ValueError('symlink source directory')
   dirs[:]=sorted(d for d in dirs if d not in EXCLUDE)
   paths.extend(Path(parent)/f for f in sorted(files))
 paths.extend(p for p in root.iterdir() if p.is_file() and (p.name in ROOT_FILES or p.suffix=='.sln'))
 rows=[]
 for p in sorted(paths):
  if p.is_symlink() or not p.is_file():raise ValueError('nonregular source input')
  raw=p.read_bytes();rows.append({'path':p.relative_to(root).as_posix(),'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()})
 if not rows:raise ValueError('empty source roster')
 return {'schema':'codegraph-source-fingerprint-v1','scope':'src and sql excluding generated/cache directories; root solution and named build/package configuration files; not exhaustive external build inputs','files':rows}

def verify(expected,actual):
 if expected['schema']!=actual['schema'] or expected['scope']!=actual['scope']:raise ValueError('fingerprint scope mismatch')
 def mapping(rows):
  result={}
  for r in rows:
   if r['path'] in result:raise ValueError('duplicate source path')
   result[r['path']]=r
  return result
 left,right=mapping(expected['files']),mapping(actual['files'])
 changed=sorted(p for p in left.keys()&right.keys() if left[p]!=right[p])
 missing=sorted(left.keys()-right.keys());added=sorted(right.keys()-left.keys())
 return {'matches':not(changed or missing or added),'changed':changed,'missing':missing,'added':added,'expected_files':len(left),'actual_files':len(right)}

if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--checkout',required=True);g=p.add_mutually_exclusive_group(required=True);g.add_argument('--write');g.add_argument('--verify');a=p.parse_args()
 actual=roster(a.checkout)
 if a.write:
  with open(a.write,'x') as f:json.dump(actual,f,indent=2);f.write('\n')
 else:
  report=verify(json.loads(Path(a.verify).read_text()),actual);print(json.dumps(report,indent=2));raise SystemExit(0 if report['matches'] else 1)
