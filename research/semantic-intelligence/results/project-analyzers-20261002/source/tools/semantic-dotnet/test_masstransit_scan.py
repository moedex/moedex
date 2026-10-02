#!/usr/bin/env python3
"""Bounded MassTransit namespace scans with constant-null predicates."""
import argparse
import json
import os
from pathlib import Path
import subprocess

p = argparse.ArgumentParser(description=__doc__)
for key in ('dotnet', 'sdk', 'worker', 'packages', 'output'):
    p.add_argument('--' + key, required=True, type=Path)
p.add_argument('--expected-worker-version', choices=['17','18','19','20','21'], default='21')
a = p.parse_args()
a.output.mkdir(parents=True, exist_ok=False)
source = '''using System;
using MassTransit;
class GlobalMarker {}
namespace Models.One {class Marker {}}
namespace Models.Two {class Marker {}}
namespace Models {
 class Outer {public class Inner {}}
 class Generic<T> {}
 class GenericOuter<T> {public class Inner {}}
 class Fake {
  public void AddConsumersFromNamespaceContaining<T>() {}
  public void AddActivitiesFromNamespaceContaining<T>() {}
 }
 class Calls {
  static bool Filter(Type t)=>true;
  static Func<Type,bool> Factory()=>Filter;
  void Run(IRegistrationConfigurator cfg,bool enabled,Func<Type,bool> predicate) {
   cfg./*consumer-default*/AddConsumersFromNamespaceContaining<Models.One.Marker>();
   cfg./*consumer-null*/AddConsumersFromNamespaceContaining<Models.One.Marker>(null);
   cfg./*consumer-named*/AddConsumersFromNamespaceContaining<Models.One.Marker>(filter:null);
   RegistrationExtensions./*consumer-static*/AddConsumersFromNamespaceContaining<Models.One.Marker>(cfg);
   if(enabled) cfg./*consumer-branch*/AddConsumersFromNamespaceContaining<Models.One.Marker>();
   cfg./*consumer-metadata*/AddConsumersFromNamespaceContaining<string>();
   cfg./*consumer-nested-simple*/AddConsumersFromNamespaceContaining<Outer.Inner>();
   cfg./*consumer-lambda*/AddConsumersFromNamespaceContaining<Models.One.Marker>(t=>true);
   cfg./*consumer-variable*/AddConsumersFromNamespaceContaining<Models.One.Marker>(predicate);
   cfg./*consumer-method*/AddConsumersFromNamespaceContaining<Models.One.Marker>(Filter);
   cfg./*consumer-factory*/AddConsumersFromNamespaceContaining<Models.One.Marker>(Factory());
   cfg./*consumer-conditional*/AddConsumersFromNamespaceContaining<Models.One.Marker>(enabled?null:predicate);
   cfg./*consumer-generic*/AddConsumersFromNamespaceContaining<Generic<int>>();
   cfg./*consumer-generic-owner*/AddConsumersFromNamespaceContaining<GenericOuter<int>.Inner>();
   cfg./*consumer-array*/AddConsumersFromNamespaceContaining<Models.One.Marker[]>();
   cfg./*consumer-global*/AddConsumersFromNamespaceContaining<GlobalMarker>();
   cfg./*consumer-type-overload*/AddConsumersFromNamespaceContaining(typeof(Models.One.Marker));
   new Fake()./*consumer-fake*/AddConsumersFromNamespaceContaining<Models.One.Marker>();
   cfg./*activity-default*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>();
   cfg./*activity-null*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>(null);
   cfg./*activity-named*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>(filter:null);
   RegistrationExtensions./*activity-static*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>(cfg);
   if(enabled) cfg./*activity-branch*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>();
   cfg./*activity-metadata*/AddActivitiesFromNamespaceContaining<string>();
   cfg./*activity-nested-simple*/AddActivitiesFromNamespaceContaining<Outer.Inner>();
   cfg./*activity-lambda*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>(t=>true);
   cfg./*activity-variable*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>(predicate);
   cfg./*activity-method*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>(Filter);
   cfg./*activity-factory*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>(Factory());
   cfg./*activity-conditional*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>(enabled?null:predicate);
   cfg./*activity-generic*/AddActivitiesFromNamespaceContaining<Generic<int>>();
   cfg./*activity-generic-owner*/AddActivitiesFromNamespaceContaining<GenericOuter<int>.Inner>();
   cfg./*activity-array*/AddActivitiesFromNamespaceContaining<Models.One.Marker[]>();
   cfg./*activity-global*/AddActivitiesFromNamespaceContaining<GlobalMarker>();
   cfg./*activity-type-overload*/AddActivitiesFromNamespaceContaining(typeof(Models.Two.Marker));
   new Fake()./*activity-fake*/AddActivitiesFromNamespaceContaining<Models.Two.Marker>();
  }
  void Open<T>(IRegistrationConfigurator cfg) where T:class {
   cfg./*consumer-open*/AddConsumersFromNamespaceContaining<T>();
   cfg./*activity-open*/AddActivitiesFromNamespaceContaining<T>();
  }
 }
}
'''
(a.output/'Fixture.cs').write_text(source)
(a.output/'global.json').write_text(json.dumps({'sdk': {'version': a.sdk.name, 'rollForward': 'disable'}}))
(a.output/'Fixture.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><FrameworkReference Include="Microsoft.AspNetCore.App"/><PackageReference Include="MassTransit" Version="8.2.1"/></ItemGroup></Project>')
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
expected = {'consumer-default': {'kind': 'consumer_namespace_scan_configuration', 'marker': 'Models.One.Marker'}, 'consumer-null': {'kind': 'consumer_namespace_scan_configuration', 'marker': 'Models.One.Marker'}, 'consumer-named': {'kind': 'consumer_namespace_scan_configuration', 'marker': 'Models.One.Marker'}, 'consumer-static': {'kind': 'consumer_namespace_scan_configuration', 'marker': 'Models.One.Marker'}, 'consumer-branch': {'kind': 'consumer_namespace_scan_configuration', 'marker': 'Models.One.Marker'}, 'consumer-metadata': {'kind': 'consumer_namespace_scan_configuration', 'marker': 'System.String'}, 'consumer-nested-simple': {'kind': 'consumer_namespace_scan_configuration', 'marker': 'Models.Outer.Inner'}, 'activity-default': {'kind': 'activity_namespace_scan_configuration', 'marker': 'Models.Two.Marker'}, 'activity-null': {'kind': 'activity_namespace_scan_configuration', 'marker': 'Models.Two.Marker'}, 'activity-named': {'kind': 'activity_namespace_scan_configuration', 'marker': 'Models.Two.Marker'}, 'activity-static': {'kind': 'activity_namespace_scan_configuration', 'marker': 'Models.Two.Marker'}, 'activity-branch': {'kind': 'activity_namespace_scan_configuration', 'marker': 'Models.Two.Marker'}, 'activity-metadata': {'kind': 'activity_namespace_scan_configuration', 'marker': 'System.String'}, 'activity-nested-simple': {'kind': 'activity_namespace_scan_configuration', 'marker': 'Models.Outer.Inner'}}
negatives = ['consumer-lambda', 'consumer-variable', 'consumer-method', 'consumer-factory', 'consumer-conditional', 'consumer-generic', 'consumer-generic-owner', 'consumer-array', 'consumer-global', 'consumer-type-overload', 'consumer-fake', 'activity-lambda', 'activity-variable', 'activity-method', 'activity-factory', 'activity-conditional', 'activity-generic', 'activity-generic-owner', 'activity-array', 'activity-global', 'activity-type-overload', 'activity-fake', 'consumer-open', 'activity-open']
for label,value in expected.items():
    facts=at(label).get('domain_facts',[]);assert len(facts)==1,(label,facts)
    f=facts[0];assert f['kind']==value['kind'] and f['rule']=='csharp-masstransit-scan-v1' and f['evidence_scope']=='compile_time'
    assert len(f['targets'])==1 and f['targets'][0]['role']=='namespace_marker'
    key=f['targets'][0]['symbol'];assert key['descriptor']=='T:'+value['marker'] and key['descriptor_kind']=='documentation_comment_id'
    if label.endswith('metadata'):assert key['namespace_kind']=='assembly' and key['namespace'].startswith('System.Runtime,')
    else:assert key['namespace_kind']=='project' and key['namespace']=='fixture/Fixture.csproj'
for label in negatives:assert not at(label).get('domain_facts'),(label,at(label))
(a.output/'expected.json').write_text(json.dumps({'positive':expected,'negative':negatives},indent=2)+'\n')
(a.output/'Fixture.cs').write_text(source+'\nclass Broken { MissingType value; }')
broken=subprocess.run(command,env=env,capture_output=True,timeout=120)
(a.output/'incomplete.jsonl').write_bytes(broken.stdout)
assert broken.returncode==2
assert not any(r.get('domain_facts') or r.get('implementation_facts') for r in map(json.loads,broken.stdout.splitlines()))
(a.output/'Fixture.cs').write_text(source)
print('PASS: fourteen namespace scans, twenty-four exclusions, incomplete suppression')
