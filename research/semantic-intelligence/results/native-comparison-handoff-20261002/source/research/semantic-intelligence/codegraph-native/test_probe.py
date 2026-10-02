#!/usr/bin/env python3
"""Offline integration controls for the metadata-only provenance probe."""
import argparse,json,shutil,subprocess
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--dotnet',required=True);p.add_argument('--probe',required=True);p.add_argument('--output',required=True);a=p.parse_args()
r=Path(a.output).resolve();r.mkdir(parents=True,exist_ok=False);src=r/'src/Fixture';src.mkdir(parents=True)
(src/'Fixture.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework><AssemblyName>TC.CodeGraphFixture</AssemblyName></PropertyGroup></Project>')
source=src/'Class.cs';source.write_text('public class Fixture { public int Value => 42; }\n')
config=r/'NuGet.Config';config.write_text('<configuration><packageSources><clear /></packageSources></configuration>')
with (r/'build.log').open('wb') as log:subprocess.run([a.dotnet,'build',str(src/'Fixture.csproj'),'--configfile',str(config)],stdout=log,stderr=subprocess.STDOUT,check=True)
bin=src/'bin/Debug/net10.0'
def probe(name):
 raw=subprocess.check_output([a.dotnet,a.probe,str(r),str(bin)]);(r/(name+'.json')).write_bytes(raw);return json.loads(raw)['assemblies'][0]
def doc(report):return next(d for d in report['documents'] if d.get('relative')=='src/Fixture/Class.cs')
first=probe('matched');assert first['pdb_linked'] and doc(first)['status']=='match'
source.write_text('public class Fixture { public int Value => 43; }\n');assert doc(probe('changed-source'))['status']=='mismatch'
source.unlink();assert doc(probe('missing-source'))['status']=='missing'
pdb=bin/'TC.CodeGraphFixture.pdb';original=pdb.read_bytes();shutil.copyfile(Path(a.probe).with_suffix('.pdb'),pdb);assert not probe('unrelated-pdb')['pdb_linked']
pdb.unlink();assert probe('missing-pdb')['status']=='missing-pdb'
pdb.write_bytes(original)
print('PASS: matching source, modified source, missing source, unrelated PDB, missing PDB')
