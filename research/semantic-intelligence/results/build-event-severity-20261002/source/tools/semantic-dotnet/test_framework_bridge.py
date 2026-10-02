#!/usr/bin/env python3
"""Closed interface identity, default body and source-derived keyed DI fixtures."""
import argparse
import json
import os
from pathlib import Path
import subprocess

p=argparse.ArgumentParser()
for key in ('dotnet','sdk','worker','packages','output'):
    p.add_argument('--'+key,required=True,type=Path)
p.add_argument('--expected-worker-version', choices=['7', '8', '9', '10','11','12','13','14','15','16','17','18','19','20'], default='20')
a=p.parse_args(); a.output.mkdir(parents=True,exist_ok=False)
source='''using System;
using System.Threading.Tasks;
using Microsoft.Extensions.DependencyInjection;
namespace One { class Message {} }
namespace Two { class Message {} }
interface IBridge { Task Handle(object value); }
interface IHandler<in T> : IBridge {
 Task Handle(T value);
 Task IBridge./*default-bridge*/Handle(object value) => Handle((T)value);
}
class First : IHandler<One.Message> { public Task /*first*/Handle(One.Message value) => Task.CompletedTask; }
class Second : IHandler<Two.Message> { public Task /*second*/Handle(Two.Message value) => Task.CompletedTask; }
class Generic<T> : IHandler<T> { public Task /*open*/Handle(T value) => Task.CompletedTask; }
class ArrayHandler : IHandler<int[]> { public Task /*array*/Handle(int[] value) => Task.CompletedTask; }
class Shape { public Task /*shape*/Handle(One.Message value) => Task.CompletedTask; }
class Fake { public void AddKeyedTransient<T,H>(object key) {} }
static class Helpers {
 public static void Register<T,H>(IServiceCollection services) where H:class,IBridge {
  services.AddKeyedTransient<IBridge,H>(typeof(T));
 }
 public static void Conditional<T,H>(IServiceCollection services,bool enabled) where H:class,IBridge {
  if(enabled) services.AddKeyedTransient<IBridge,H>(typeof(T));
 }
 public static void Factory<T,H>(IServiceCollection services) where H:class,IBridge,new() {
  services.AddKeyedTransient<IBridge,H>(typeof(T),(sp,key)=>new H());
 }
 public static void Nested<T,H>(IServiceCollection services) where H:class,IBridge { Register<T,H>(services); }
 public static void DifferentName<T,H>(IServiceCollection services) where H:class,IBridge {
  services.AddKeyedSingleton<IBridge,H>(typeof(T));
 }
}
class Calls {
 void Run(IServiceCollection services, IHandler<One.Message> one, IHandler<Two.Message> two) {
  services./*direct*/AddKeyedScoped<IBridge,First>(typeof(One.Message));
  Helpers./*helper-one*/Register<One.Message,First>(services);
  Helpers./*helper-two*/Register<Two.Message,Second>(services);
  Helpers./*renamed*/DifferentName<One.Message,First>(services);
  Helpers./*conditional*/Conditional<One.Message,First>(services,true);
  Helpers./*factory*/Factory<One.Message,First>(services);
  Helpers./*nested*/Nested<One.Message,First>(services);
  services./*string-key*/AddKeyedTransient<IBridge,First>("key");
  new Fake()./*lookalike*/AddKeyedTransient<IBridge,First>(typeof(One.Message));
  one./*call-one*/Handle(new One.Message()); two./*call-two*/Handle(new Two.Message());
 }
 void Open<T>(IServiceCollection services) { Helpers./*open-helper*/Register<T,First>(services); }
}
'''
(a.output/'global.json').write_text(json.dumps({'sdk': {'version': a.sdk.name, 'rollForward': 'disable'}}))
(a.output/'Fixture.cs').write_text(source)
(a.output/'Fixture.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><FrameworkReference Include="Microsoft.AspNetCore.App" /></ItemGroup></Project>')
(a.output/'NuGet.Config').write_text('<configuration><packageSources><clear /></packageSources></configuration>')
env=dict(os.environ,NUGET_PACKAGES=str(a.packages.resolve()),DOTNET_NOLOGO='1',DOTNET_CLI_TELEMETRY_OPTOUT='1',DOTNET_PROCESSOR_COUNT='2')
subprocess.run([str(a.dotnet),'restore',str(a.output/'Fixture.csproj')],env=env,cwd=a.output,check=True,timeout=120)
command=[str(a.dotnet),str(a.worker),'--repo','fixture','--root',str(a.output),'--project','Fixture.csproj','--framework','net8.0','--sdk-path',str(a.sdk)]
result=subprocess.run(command,env=env,capture_output=True,timeout=120)
(a.output/'capture.jsonl').write_bytes(result.stdout);(a.output/'capture.stderr').write_bytes(result.stderr)
assert result.returncode==0,(result.returncode,result.stderr)
rows=[json.loads(line) for line in result.stdout.splitlines()]
assert all(r['extractor_version']==a.expected_worker_version and r['compilation_status']=='complete' for r in rows if r['record_type']=='project')
def at(label):
 offset=source.index('/*'+label+'*/')+len(label)+4
 hits=[r for r in rows if r.get('source_path')=='Fixture.cs' and r.get('span',{}).get('byte_offset')==offset and r['record_type'] in ('declaration','reference')]
 assert len(hits)==1,(label,hits)
 return hits[0]
for label,typ in [('first','One.Message'),('second','Two.Message')]:
 f=at(label)['implementation_facts']; assert len(f)==1 and f[0]['rule']=='csharp-interface-closed-v1',f
 key=f[0]['interface_symbol'];assert key['descriptor_kind']=='constructed_interface_method_v1'
 desc=json.loads(key['descriptor']);assert desc['arguments'][0]['descriptor']=='T:'+typ,desc
 call=at('call-one' if label=='first' else 'call-two')['symbol'];assert call==key,(call,key)
assert at('first')['implementation_facts'][0]['interface_symbol']!=at('second')['implementation_facts'][0]['interface_symbol']
bridge=at('default-bridge')['implementation_facts'];assert len(bridge)==1 and bridge[0]['rule']=='csharp-interface-default-v1' and bridge[0]['interface_symbol']['descriptor']=='M:IBridge.Handle(System.Object)',bridge
for label in ['open','array','shape']:assert not at(label).get('implementation_facts'),label
for label,lifetime,key in [('direct','scoped','One.Message'),('helper-one','transient','One.Message'),('helper-two','transient','Two.Message'),('renamed','singleton','One.Message')]:
 facts=at(label).get('domain_facts');assert len(facts or [])==1,(label,facts)
 f=facts[0];assert f['rule']=='csharp-keyed-di-v1' and f['lifetime']==lifetime and f['kind']==('di_keyed_registration' if label=='direct' else 'di_keyed_registration_configuration'),f
 assert [t['role'] for t in f['targets']]==['service','implementation','key_type','registration_api']
 assert f['targets'][2]['symbol']['descriptor']=='T:'+key
 assert f['targets'][3]['symbol']['namespace_kind']=='assembly'
for label in ['conditional','factory','nested','string-key','lookalike','open-helper']:assert not at(label).get('domain_facts'),(label,at(label))
# Incomplete compilation suppresses all interpreted facts.
(a.output/'Fixture.cs').write_text(source+'\nclass Broken { MissingType value; }')
broken=subprocess.run(command,env=env,capture_output=True,timeout=120)
(a.output/'incomplete.jsonl').write_bytes(broken.stdout)
assert broken.returncode==2
assert not any(r.get('domain_facts') or r.get('implementation_facts') for r in map(json.loads,broken.stdout.splitlines()))
(a.output/'Fixture.cs').write_text(source)
print('PASS: two distinct closed handlers/calls, one default template, four keyed configurations, nine exclusions, incomplete suppression')
