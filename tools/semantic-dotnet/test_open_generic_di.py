#!/usr/bin/env python3
"""Bounded positional open-generic registration templates against real DI metadata."""
import argparse
import json
import os
from pathlib import Path
import subprocess

p = argparse.ArgumentParser(description=__doc__)
for key in ('dotnet', 'sdk', 'worker', 'packages', 'output'):
    p.add_argument('--' + key, required=True, type=Path)
p.add_argument('--expected-worker-version', choices=['15','16','17','18','19','20','21'], default='21')
a = p.parse_args()
a.output.mkdir(parents=True, exist_ok=False)
source = '''using System;
using System.Collections.Generic;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.DependencyInjection.Extensions;
interface IRepo<T> {}
interface IPair<A,B> {}
class Repo<T> : IRepo<T> where T:class {}
class Child<T> : Repo<T> where T:class {}
class Pair<T,U> : IPair<T,U> {}
class Swap<T,U> : IPair<U,T> {}
class Repeat<T,U> : IPair<T,T> {}
class Nested<T> : IRepo<List<T>> {}
class Unrelated<T> {}
abstract class Abstract<T> : IRepo<T> {}
class Private<T> : IRepo<T> { private Private() {} }
interface IWide<A,B,C,D,E,F,G,H,I> {}
class Wide<A,B,C,D,E,F,G,H,I> : IWide<A,B,C,D,E,F,G,H,I> {}
class Outer { public class Inner<T> : IRepo<T> {} }
class Fake {public void AddScoped(Type serviceType,Type implementationType) {}}
class Calls {
 void Run(IServiceCollection services,bool enabled) {
  services./*singleton*/AddSingleton(typeof(IRepo<>),typeof(Repo<>));
  services./*scoped*/AddScoped(typeof(IRepo<>),typeof(Repo<>));
  services./*transient*/AddTransient(typeof(IRepo<>),typeof(Repo<>));
  services./*pair*/AddScoped(typeof(IPair<,>),typeof(Pair<,>));
  services./*inherited*/AddScoped(typeof(IRepo<>),typeof(Child<>));
  services./*named*/AddScoped(implementationType:typeof(Repo<>),serviceType:typeof(IRepo<>));
  ServiceCollectionServiceExtensions./*static*/AddScoped(services,typeof(IRepo<>),typeof(Repo<>));
  if(enabled) services./*branch*/AddScoped(typeof(IRepo<>),typeof(Repo<>));
  services./*private-ctor*/AddScoped(typeof(IRepo<>),typeof(Private<>));
  services./*swapped*/AddScoped(typeof(IPair<,>),typeof(Swap<,>));
  services./*repeated*/AddScoped(typeof(IPair<,>),typeof(Repeat<,>));
  services./*nested-argument*/AddScoped(typeof(IRepo<>),typeof(Nested<>));
  services./*unrelated*/AddScoped(typeof(IRepo<>),typeof(Unrelated<>));
  services./*abstract*/AddScoped(typeof(IRepo<>),typeof(Abstract<>));
  services./*implementation-interface*/AddScoped(typeof(IRepo<>),typeof(IRepo<>));
  services./*class-service*/AddScoped(typeof(Repo<>),typeof(Child<>));
  services./*arity*/AddScoped(typeof(IRepo<>),typeof(Pair<,>));
  services./*wide*/AddScoped(typeof(IWide<,,,,,,,,>),typeof(Wide<,,,,,,,,>));
  services./*nested-owner*/AddScoped(typeof(IRepo<>),typeof(Outer.Inner<>));
  Type variable=typeof(IRepo<>);services./*variable*/AddScoped(variable,typeof(Repo<>));
  services./*conditional*/AddScoped(enabled?typeof(IRepo<>):variable,typeof(Repo<>));
  services./*closed*/AddScoped(typeof(IRepo<string>),typeof(Repo<string>));
  services./*cast*/AddScoped((Type)typeof(IRepo<>),typeof(Repo<>));
  services./*if-absent*/TryAddScoped(typeof(IRepo<>),typeof(Repo<>));
  services./*factory*/AddScoped(typeof(IRepo<>),sp=>new Repo<string>());
  new Fake()./*fake*/AddScoped(typeof(IRepo<>),typeof(Repo<>));
 }
 void Open<T>(IServiceCollection services) {services./*type-parameter*/AddScoped(typeof(T),typeof(Repo<>));}
}
'''
(a.output/'Fixture.cs').write_text(source)
(a.output/'global.json').write_text(json.dumps({'sdk': {'version': a.sdk.name, 'rollForward': 'disable'}}))
(a.output/'Fixture.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><FrameworkReference Include="Microsoft.AspNetCore.App"/></ItemGroup></Project>')
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
expected={label:{'service':'IRepo`1','implementation':'Repo`1','lifetime':label if label in ('singleton','scoped','transient') else 'scoped'} for label in ['singleton','scoped','transient','named','static','branch']}
expected['pair']={'service':'IPair`2','implementation':'Pair`2','lifetime':'scoped'}
expected['inherited']={'service':'IRepo`1','implementation':'Child`1','lifetime':'scoped'}
expected['private-ctor']={'service':'IRepo`1','implementation':'Private`1','lifetime':'scoped'}
for label,values in expected.items():
    facts=at(label).get('domain_facts',[]);assert len(facts)==1,(label,facts)
    f=facts[0];assert f['kind']=='di_open_generic_registration_configuration' and f['rule']=='csharp-open-di-v1' and f['evidence_scope']=='compile_time' and f['lifetime']==values['lifetime']
    assert [t['role'] for t in f['targets']]==['service_template','implementation_template']
    for target,name in zip(f['targets'],[values['service'],values['implementation']]):
        key=target['symbol'];assert key['descriptor']=='T:'+name and key['descriptor_kind']=='documentation_comment_id' and key['namespace_kind']=='project' and key['namespace']=='fixture/Fixture.csproj'
negatives=['swapped','repeated','nested-argument','unrelated','abstract','implementation-interface','class-service','arity','wide','nested-owner','variable','conditional','closed','cast','if-absent','factory','fake','type-parameter']
for label in negatives:assert not at(label).get('domain_facts'),(label,at(label))
(a.output/'expected.json').write_text(json.dumps({'positive':expected,'negative':negatives},indent=2)+'\n')
(a.output/'Fixture.cs').write_text(source+'\nclass Broken { MissingType value; }')
broken=subprocess.run(command,env=env,capture_output=True,timeout=120)
(a.output/'incomplete.jsonl').write_bytes(broken.stdout)
assert broken.returncode==2
assert not any(r.get('domain_facts') or r.get('implementation_facts') for r in map(json.loads,broken.stdout.splitlines()))
(a.output/'Fixture.cs').write_text(source)
print('PASS: nine positional registration templates, eighteen exclusions, incomplete suppression')
