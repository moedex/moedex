#!/usr/bin/env python3
"""Exact EF8 metadata API positives and overload/lookalike negatives, offline."""
import argparse
import json
import os
from pathlib import Path
import subprocess

p = argparse.ArgumentParser()
for key in ('dotnet', 'sdk', 'worker', 'packages', 'output'):
    p.add_argument('--' + key, required=True, type=Path)
a = p.parse_args()
a.output.mkdir(parents=True, exist_ok=False)
source = '''using Microsoft.EntityFrameworkCore;
class Entity {}
class Box<T> {}
class Fake { public object Set<T>() => null!; public object Entity<T>() => null!; }
class C : DbContext {
 void Check(ModelBuilder model, Fake fake) {
  _ = this./*use*/Set<Entity>();
  _ = model./*mapping*/Entity<Entity>();
  _ = this./*named*/Set<Entity>("named");
  _ = model./*callback*/Entity<Entity>(b => {});
  _ = model./*type*/Entity(typeof(Entity));
  _ = this./*generic*/Set<Box<Entity>>();
  _ = fake./*fake-set*/Set<Entity>();
  _ = fake./*fake-model*/Entity<Entity>();
  dynamic unknown = fake;
  _ = unknown./*dynamic*/Set<Entity>();
 }
 void Generic<T>() where T : class { _ = this./*open*/Set<T>(); }
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
for label, kind in [('use', 'storage_entity_use'), ('mapping', 'storage_entity_mapping'), ('named', None), ('callback', None), ('type', None), ('generic', None), ('fake-set', None), ('fake-model', None), ('dynamic', None), ('open', None)]:
    offset = source.index('/*' + label + '*/') + len(label) + 4
    hits = [r for r in rows if r['record_type'] == 'reference' and r['source_path'] == 'Fixture.cs' and r['span']['byte_offset'] == offset]
    assert len(hits) == 1, (label, hits)
    facts = hits[0].get('domain_facts') or []
    assert len(facts) == int(kind is not None), (label, facts)
    if kind:
        assert facts[0]['kind'] == kind and facts[0]['rule'] == 'csharp-framework-v2'
        assert facts[0]['targets'][0]['symbol']['descriptor'] == 'T:Entity'
        assert facts[0]['evidence_scope'] == 'compile_time'
assert sum(len(r.get('domain_facts') or []) for r in rows) == 2
print('PASS: 2 exact API positives, 8 overload/generic/lookalike/dynamic negatives')
