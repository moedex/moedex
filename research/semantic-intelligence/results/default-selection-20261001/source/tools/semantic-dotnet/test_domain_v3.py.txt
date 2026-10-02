#!/usr/bin/env python3
"""Exact EF8 context registrations and context lifetime constants, offline."""
import argparse
import json
import os
from pathlib import Path
import subprocess

p = argparse.ArgumentParser()
for key in ('dotnet', 'sdk', 'worker', 'packages', 'output'):
    p.add_argument('--' + key, required=True, type=Path)
p.add_argument('--expected-worker-version', default='4')
a = p.parse_args()
a.output.mkdir(parents=True, exist_ok=False)
source = '''using System;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
class Context : DbContext {}
class GenericContext<T> : DbContext {}
class Fake { public void AddDbContext<T>(Action<DbContextOptionsBuilder> action) {} }
class C {
 void Check(IServiceCollection services, Fake fake, ServiceLifetime runtime) {
  services./*default*/AddDbContext<Context>(b => {});
  services./*singleton*/AddDbContext<Context>(b => {}, ServiceLifetime.Singleton);
  services./*transient*/AddDbContext<Context>(optionsLifetime: ServiceLifetime.Scoped, contextLifetime: ServiceLifetime.Transient, optionsAction: b => {});
  const ServiceLifetime constant = ServiceLifetime.Scoped;
  services./*constant*/AddDbContext<Context>(b => {}, constant);
  services./*options-only*/AddDbContext<Context>(b => {}, optionsLifetime: runtime);
  EntityFrameworkServiceCollectionExtensions./*static*/AddDbContext<Context>(services, b => {});
  services./*runtime*/AddDbContext<Context>(b => {}, runtime);
  services./*invalid*/AddDbContext<Context>(b => {}, (ServiceLifetime)77);
  services./*empty*/AddDbContext<Context>();
  services./*no-callback*/AddDbContext<Context>(ServiceLifetime.Scoped);
  services./*provider*/AddDbContext<Context>((provider, b) => {});
  services./*two-types*/AddDbContext<Context, Context>(b => {});
  services./*pool*/AddDbContextPool<Context>(b => {});
  services./*factory*/AddDbContextFactory<Context>(b => {});
  services./*generic*/AddDbContext<GenericContext<int>>(b => {});
  fake./*fake*/AddDbContext<Context>(b => {});
 }
 void Generic<T>(IServiceCollection services) where T : DbContext {
  services./*open*/AddDbContext<T>(b => {});
 }
}
'''
(a.output / 'Fixture.cs').write_text(source)
(a.output / 'Fixture.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><PackageReference Include="Microsoft.EntityFrameworkCore" Version="8.0.4" /></ItemGroup></Project>')
(a.output / 'NuGet.Config').write_text('<configuration><packageSources><clear /></packageSources></configuration>')
env = dict(os.environ, NUGET_PACKAGES=str(a.packages.resolve()), DOTNET_NOLOGO='1', DOTNET_CLI_TELEMETRY_OPTOUT='1')
subprocess.run([str(a.dotnet), 'restore', str(a.output / 'Fixture.csproj')], env=env, check=True, timeout=120)
result = subprocess.run([str(a.dotnet), str(a.worker), '--repo', 'fixture', '--root', str(a.output), '--project', 'Fixture.csproj', '--framework', 'net8.0', '--sdk-path', str(a.sdk)], env=env, check=True, capture_output=True, timeout=120)
(a.output / 'capture.jsonl').write_bytes(result.stdout)
rows = [json.loads(line) for line in result.stdout.splitlines()]
expected = {'empty': 'scoped', 'default': 'scoped', 'singleton': 'singleton', 'transient': 'transient', 'constant': 'scoped', 'options-only': 'scoped', 'static': 'scoped'}
negatives = ['runtime', 'invalid', 'no-callback', 'provider', 'two-types', 'pool', 'factory', 'generic', 'fake', 'open']
api = 'M:Microsoft.Extensions.DependencyInjection.EntityFrameworkServiceCollectionExtensions.AddDbContext``1(Microsoft.Extensions.DependencyInjection.IServiceCollection,System.Action{Microsoft.EntityFrameworkCore.DbContextOptionsBuilder},Microsoft.Extensions.DependencyInjection.ServiceLifetime,Microsoft.Extensions.DependencyInjection.ServiceLifetime)'
projects = [r for r in rows if r['record_type'] == 'project']
assert projects and all(r['compilation_status'] == 'complete' and r['extractor_version'] == a.expected_worker_version for r in projects), projects
for label in [*expected, *negatives]:
    offset = source.index('/*' + label + '*/') + len(label) + 4
    hits = [r for r in rows if r['record_type'] == 'reference' and r['source_path'] == 'Fixture.cs' and r['span']['byte_offset'] == offset]
    assert len(hits) == 1, (label, hits)
    facts = hits[0].get('domain_facts') or []
    assert len(facts) == int(label in expected), (label, facts)
    if label in expected:
        fact = facts[0]
        assert fact['kind'] == 'storage_context_registration' and fact['rule'] == 'csharp-framework-v3'
        assert fact['evidence_scope'] == 'compile_time' and fact['lifetime'] == expected[label]
        assert [(t['role'], t['symbol']['descriptor']) for t in fact['targets']] == [('service', 'T:Context'), ('implementation', 'T:Context')]
        assert 'table' not in fact and 'schema' not in fact
        assert hits[0]['symbol']['descriptor'] == api, hits[0]
assert sum(len(r.get('domain_facts') or []) for r in rows) == len(expected)
print('PASS: 7 exact AddDbContext registrations/lifetimes, 10 nonconstant/overload/generic/lookalike exclusions')
