#!/usr/bin/env python3
"""Typed response and if-absent DI facts against real pinned framework metadata."""
import argparse
import json
import os
from pathlib import Path
import subprocess

p = argparse.ArgumentParser(description=__doc__)
for key in ('dotnet', 'sdk', 'worker', 'packages', 'output'):
    p.add_argument('--' + key, required=True, type=Path)
p.add_argument('--expected-worker-version', choices=['10','11','12','13','14','15','16','17','18','19'], default='19')
a = p.parse_args()
a.output.mkdir(parents=True, exist_ok=False)
source = '''using System;
using System.Threading.Tasks;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.DependencyInjection.Extensions;
using MassTransit;
interface IService {}
class Service : IService {}
class Generic<T> : IService {}
interface Reply { string Value { get; } }
class ConcreteReply { public string Value { get; set; } }
class GenericReply<T> {}
class Fake {
 public Task RespondAsync<T>(object value) => Task.CompletedTask;
 public void TryAddSingleton<T,U>() {}
}
class Calls {
 async Task Run(ConsumeContext context, IServiceCollection services, bool enabled) {
  await context./*response-init*/RespondAsync<Reply>(new {Value="ready"});
  await context./*response-value*/RespondAsync(new ConcreteReply());
  await context./*response-pipe*/RespondAsync<Reply>(new {Value="ready"}, (IPipe<SendContext<Reply>>)null);
  await context./*response-untyped*/RespondAsync((object)new ConcreteReply());
  await context./*response-generic*/RespondAsync<GenericReply<int>>(new GenericReply<int>());
  await new Fake()./*response-fake*/RespondAsync<Reply>(new {});
  services./*singleton-two*/TryAddSingleton<IService,Service>();
  services./*scoped-two*/TryAddScoped<IService,Service>();
  services./*transient-two*/TryAddTransient<IService,Service>();
  services./*singleton-one*/TryAddSingleton<Service>();
  services./*scoped-one*/TryAddScoped<Service>();
  services./*transient-one*/TryAddTransient<Service>();
  ServiceCollectionDescriptorExtensions./*static*/TryAddSingleton<IService,Service>(services);
  if(enabled) services./*branch*/TryAddSingleton<IService,Service>();
  services./*unconditional*/AddSingleton<IService,Service>();
  services./*factory*/TryAddSingleton<IService>(sp => new Service());
  services./*instance*/TryAddSingleton<IService>(new Service());
  services./*runtime-type*/TryAddSingleton(typeof(IService), typeof(Service));
  services./*generic*/TryAddSingleton<IService,Generic<int>>();
  new Fake()./*di-fake*/TryAddSingleton<IService,Service>();
 }
 async Task Open<T>(ConsumeContext context, IServiceCollection services) where T:class,IService {
  await context./*response-open*/RespondAsync<T>(new {});
  services./*di-open*/TryAddSingleton<IService,T>();
 }
}
'''
(a.output/'Fixture.cs').write_text(source)
(a.output/'global.json').write_text(json.dumps({'sdk': {'version': a.sdk.name, 'rollForward': 'disable'}}))
(a.output/'Fixture.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><FrameworkReference Include="Microsoft.AspNetCore.App"/><PackageReference Include="MassTransit.Abstractions" Version="8.2.1"/></ItemGroup></Project>')
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
expected={}
for label,target in [('response-init','Reply'),('response-value','ConcreteReply')]:
    facts=at(label).get('domain_facts',[])
    assert len(facts)==1 and facts[0]['kind']=='message_response', (label,facts)
    assert facts[0]['targets'][0]['symbol']['descriptor']=='T:'+target
    expected[label]={'kind':'message_response','targets':[target]}
for label in ['singleton-two','scoped-two','transient-two','singleton-one','scoped-one','transient-one','static','branch']:
    f=at(label).get('domain_facts',[])
    lifetime=label.split('-')[0] if '-' in label else 'singleton'
    targets=['Service','Service'] if label.endswith('-one') else ['IService','Service']
    assert len(f)==1 and f[0]['kind']=='di_registration_if_absent' and f[0]['lifetime']==lifetime,(label,f)
    assert [t['symbol']['descriptor'] for t in f[0]['targets']]==['T:'+t for t in targets]
    expected[label]={'kind':'di_registration_if_absent','targets':targets,'lifetime':lifetime}
for label in expected:
    f=at(label)['domain_facts'][0]
    assert f['rule']=='csharp-framework-v5' and f['evidence_scope']=='compile_time'
assert at('unconditional')['domain_facts'][0]['kind']=='di_registration'
negatives=['response-pipe','response-untyped','response-generic','response-fake','response-open','factory','instance','runtime-type','generic','di-fake','di-open']
for label in negatives: assert not at(label).get('domain_facts'),(label,at(label))
(a.output/'expected.json').write_text(json.dumps({'positive':expected,'negative':negatives},indent=2)+'\n')
(a.output/'Fixture.cs').write_text(source+'\nclass Broken { MissingType value; }')
broken=subprocess.run(command,env=env,capture_output=True,timeout=120)
(a.output/'incomplete.jsonl').write_bytes(broken.stdout)
assert broken.returncode==2
assert not any(r.get('domain_facts') or r.get('implementation_facts') for r in map(json.loads,broken.stdout.splitlines()))
(a.output/'Fixture.cs').write_text(source)
print('PASS: 10 exact new facts, 11 exclusions, unconditional registration distinction, incomplete suppression')
