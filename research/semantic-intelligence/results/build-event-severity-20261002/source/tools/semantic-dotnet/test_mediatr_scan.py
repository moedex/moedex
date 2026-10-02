#!/usr/bin/env python3
"""Exact MediatR assembly-scan configuration with a direct typeof marker."""
import argparse
import json
import os
from pathlib import Path
import subprocess

p = argparse.ArgumentParser(description=__doc__)
for key in ('dotnet', 'sdk', 'worker', 'packages', 'output'):
    p.add_argument('--' + key, required=True, type=Path)
p.add_argument('--expected-worker-version', choices=['16','17','18','19','20'], default='20')
a = p.parse_args()
a.output.mkdir(parents=True, exist_ok=False)
source = '''using System;
using System.Reflection;
using System.Collections.Generic;
using MediatR;
using Microsoft.Extensions.DependencyInjection;
using Alias = Marker;
class Marker {}
class Other {}
class Generic<T> {}
class Outer { public class Inner {} }
class GenericOuter<T> { public class Inner {} }
class Fake { public void RegisterServicesFromAssembly(Assembly assembly) {} }
class Holder { public Assembly Assembly => typeof(Marker).Assembly; }
class Calls {
 static Assembly Factory()=>typeof(Marker).Assembly;
 void Run(MediatRServiceConfiguration cfg,IServiceCollection services,bool enabled) {
  cfg./*direct*/RegisterServicesFromAssembly(typeof(Marker).Assembly);
  cfg./*named*/RegisterServicesFromAssembly(assembly:typeof(Other).Assembly);
  if(enabled) cfg./*branch*/RegisterServicesFromAssembly(typeof(Marker).Assembly);
  services.AddMediatR(c=>c./*callback*/RegisterServicesFromAssembly(typeof(Marker).Assembly));
  new MediatRServiceConfiguration()./*detached*/RegisterServicesFromAssembly(typeof(Marker).Assembly);
  cfg./*metadata*/RegisterServicesFromAssembly(typeof(string).Assembly);
  cfg./*nested-simple*/RegisterServicesFromAssembly(typeof(Outer.Inner).Assembly);
  cfg./*alias*/RegisterServicesFromAssembly(typeof(Alias).Assembly);
  Assembly variable=typeof(Marker).Assembly;cfg./*variable*/RegisterServicesFromAssembly(variable);
  cfg./*factory*/RegisterServicesFromAssembly(Factory());
  cfg./*executing*/RegisterServicesFromAssembly(Assembly.GetExecutingAssembly());
  cfg./*calling*/RegisterServicesFromAssembly(Assembly.GetCallingAssembly());
  cfg./*entry*/RegisterServicesFromAssembly(Assembly.GetEntryAssembly());
  cfg./*gettype*/RegisterServicesFromAssembly(new Marker().GetType().Assembly);
  cfg./*cast-assembly*/RegisterServicesFromAssembly((Assembly)typeof(Marker).Assembly);
  cfg./*cast-type*/RegisterServicesFromAssembly(((Type)typeof(Marker)).Assembly);
  cfg./*type-lookup*/RegisterServicesFromAssembly(Type.GetType("Marker").Assembly);
  cfg./*generic-closed*/RegisterServicesFromAssembly(typeof(Generic<int>).Assembly);
  cfg./*generic-open*/RegisterServicesFromAssembly(typeof(Generic<>).Assembly);
  cfg./*generic-owner*/RegisterServicesFromAssembly(typeof(GenericOuter<int>.Inner).Assembly);
  cfg./*array*/RegisterServicesFromAssembly(typeof(Marker[]).Assembly);
  cfg./*tuple*/RegisterServicesFromAssembly(typeof((int,string)).Assembly);
  cfg./*convenience*/RegisterServicesFromAssemblyContaining<Marker>();
  cfg./*plural*/RegisterServicesFromAssemblies(typeof(Marker).Assembly,typeof(Other).Assembly);
  cfg./*property-lookalike*/RegisterServicesFromAssembly(new Holder().Assembly);
  new Fake()./*api-lookalike*/RegisterServicesFromAssembly(typeof(Marker).Assembly);
  cfg./*typeinfo*/RegisterServicesFromAssembly(typeof(Marker).GetTypeInfo().Assembly);
  cfg./*conditional*/RegisterServicesFromAssembly(enabled?typeof(Marker).Assembly:variable);
 }
 void Open<T>(MediatRServiceConfiguration cfg) {cfg./*type-parameter*/RegisterServicesFromAssembly(typeof(T).Assembly);}
}
'''
(a.output/'Fixture.cs').write_text(source)
(a.output/'global.json').write_text(json.dumps({'sdk': {'version': a.sdk.name, 'rollForward': 'disable'}}))
(a.output/'Fixture.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><FrameworkReference Include="Microsoft.AspNetCore.App"/><PackageReference Include="MediatR" Version="12.0.1"/></ItemGroup></Project>')
(a.output/'NuGet.Config').write_text('<configuration><packageSources><clear /></packageSources></configuration>')
env = dict(os.environ, NUGET_PACKAGES=str(a.packages.resolve()), DOTNET_NOLOGO='1', DOTNET_CLI_TELEMETRY_OPTOUT='1', DOTNET_PROCESSOR_COUNT='2')
subprocess.run([str(a.dotnet),str(a.sdk/'MSBuild.dll'),'-target:Restore',str(a.output/'Fixture.csproj'),'-p:NuGetAudit=false'], env=env, cwd=a.output, check=True, timeout=120)
command = [str(a.dotnet),str(a.worker),'--repo','fixture','--root',str(a.output),'--project','Fixture.csproj','--framework','net8.0','--sdk-path',str(a.sdk)]
result = subprocess.run(command,env=env,capture_output=True,timeout=120)
(a.output/'capture.jsonl').write_bytes(result.stdout)
(a.output/'capture.stderr').write_bytes(result.stderr)
assert result.returncode == 0, (result.returncode,result.stderr)
rows = list(map(json.loads,result.stdout.splitlines()))
assert all(r['extractor_version']==a.expected_worker_version and r['compilation_status']=='complete' for r in rows if r['record_type']=='project')
def at(label):
    offset=source.index('/*'+label+'*/')+len(label)+4
    hits=[r for r in rows if r.get('source_path')=='Fixture.cs' and r.get('span',{}).get('byte_offset')==offset and r['record_type']=='reference']
    assert len(hits)==1,(label,hits)
    return hits[0]
expected={'direct':'Marker','named':'Other','branch':'Marker','callback':'Marker','detached':'Marker','metadata':'System.String','nested-simple':'Outer.Inner','alias':'Marker'}
for label,marker in expected.items():
    facts=at(label).get('domain_facts',[]);assert len(facts)==1,(label,facts)
    f=facts[0];assert f['kind']=='mediator_assembly_scan_configuration' and f['rule']=='csharp-mediatr-scan-v1' and f['evidence_scope']=='compile_time'
    assert len(f['targets'])==1 and f['targets'][0]['role']=='assembly_marker'
    key=f['targets'][0]['symbol'];assert key['descriptor']=='T:'+marker and key['descriptor_kind']=='documentation_comment_id'
    if label=='metadata':assert key['namespace_kind']=='assembly' and key['namespace'].startswith('System.Runtime,')
    else:assert key['namespace_kind']=='project' and key['namespace']=='fixture/Fixture.csproj'
negatives=['variable','factory','executing','calling','entry','gettype','cast-assembly','cast-type','type-lookup','generic-closed','generic-open','generic-owner','array','tuple','convenience','plural','property-lookalike','api-lookalike','typeinfo','conditional','type-parameter']
for label in negatives:assert not at(label).get('domain_facts'),(label,at(label))
(a.output/'expected.json').write_text(json.dumps({'positive':expected,'negative':negatives},indent=2)+'\n')
(a.output/'Fixture.cs').write_text(source+'\nclass Broken { MissingType value; }')
broken=subprocess.run(command,env=env,capture_output=True,timeout=120)
(a.output/'incomplete.jsonl').write_bytes(broken.stdout)
assert broken.returncode==2
assert not any(r.get('domain_facts') or r.get('implementation_facts') for r in map(json.loads,broken.stdout.splitlines()))
(a.output/'Fixture.cs').write_text(source)
print('PASS: eight scan configurations, twenty-one exclusions, incomplete suppression')
