#!/usr/bin/env python3
"""Exact MassTransit8 state-machine configuration APIs, offline."""
import argparse
import json
import os
from pathlib import Path
import subprocess

p = argparse.ArgumentParser()
for key in ('dotnet', 'sdk', 'worker', 'packages', 'output'):
    p.add_argument('--' + key, required=True, type=Path)
p.add_argument('--expected-worker-version', default='5')
a = p.parse_args()
a.output.mkdir(parents=True, exist_ok=False)
source = '''using System;
using System.Threading.Tasks;
using MassTransit;
class Input { public Guid CorrelationId { get; set; } }
class Output {}
class GenericMessage<T> {}
class Saga : SagaStateMachineInstance { public Guid CorrelationId { get; set; } }
class Fake { public void Publish(Func<object, Output> factory) {} public void Event(Action configure) {} }
class Machine : MassTransitStateMachine<Saga> {
 public Event<Input> Started { get; private set; } = null!;
 public Event<Input> Other { get; private set; } = null!;
 public Event<GenericMessage<int>> GenericEvent { get; private set; } = null!;
 public Machine() {
  /*event*/Event(() => Started, x => x.CorrelateById(c => c.Message.CorrelationId));
  /*event-null*/Event(() => Other, null);
  /*event-single*/Event(() => Other);
  /*generic-event*/Event(() => GenericEvent, x => {});
  var binder = When(Started);
  _ = binder./*publish*/Publish(c => new Output());
  _ = binder./*callback*/Publish(c => new Output(), c => {});
  _ = MassTransit.PublishExtensions./*static*/Publish<Saga, Input, Output>(binder, c => new Output());
  _ = binder./*generic-publish*/Publish(c => new GenericMessage<int>());
  _ = binder./*instance*/Publish(new Output());
  _ = binder./*async*/PublishAsync(c => Task.FromResult(new Output()));
  _ = When(Started).Then(c => {})./*chain*/Publish(c => new Output(), null);
  var fake = new Fake();
  fake./*fake-publish*/Publish(c => new Output());
  fake./*fake-event*/Event(() => {});
 }
 void Generic<T>(EventActivityBinder<Saga, Input> binder) where T : class, new() {
  _ = binder./*open*/Publish(c => new T());
 }
}
'''
(a.output / 'Fixture.cs').write_text(source)
(a.output / 'Fixture.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><PackageReference Include="MassTransit" Version="8.2.4" /></ItemGroup></Project>')
(a.output / 'NuGet.Config').write_text('<configuration><packageSources><clear /></packageSources></configuration>')
env = dict(os.environ, NUGET_PACKAGES=str(a.packages.resolve()), DOTNET_NOLOGO='1', DOTNET_CLI_TELEMETRY_OPTOUT='1')
subprocess.run([str(a.dotnet), 'restore', str(a.output / 'Fixture.csproj')], env=env, check=True, timeout=120)
result = subprocess.run([str(a.dotnet), str(a.worker), '--repo', 'fixture', '--root', str(a.output), '--project', 'Fixture.csproj', '--framework', 'net8.0', '--sdk-path', str(a.sdk)], env=env, check=True, capture_output=True, timeout=120)
(a.output / 'capture.jsonl').write_bytes(result.stdout)
rows = [json.loads(line) for line in result.stdout.splitlines()]
expected = {'event': ('message_event_configuration', 'T:Input'), 'event-null': ('message_event_configuration', 'T:Input'), 'publish': ('message_publish_configuration', 'T:Output'), 'callback': ('message_publish_configuration', 'T:Output'), 'static': ('message_publish_configuration', 'T:Output'), 'chain': ('message_publish_configuration', 'T:Output')}
negatives = ['event-single', 'generic-event', 'generic-publish', 'instance', 'async', 'fake-publish', 'fake-event', 'open']
apis = {
 'message_publish_configuration': 'M:MassTransit.PublishExtensions.Publish``3(MassTransit.EventActivityBinder{``0,``1},MassTransit.EventMessageFactory{``0,``1,``2},System.Action{MassTransit.PublishContext{``2}})',
 'message_event_configuration': 'M:MassTransit.MassTransitStateMachine`1.Event``1(System.Linq.Expressions.Expression{System.Func{MassTransit.Event{``0}}},System.Action{MassTransit.IEventCorrelationConfigurator{`0,``0}})'
}
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
        kind, target = expected[label]
        assert fact['kind'] == kind and fact['rule'] == 'csharp-framework-v4'
        assert fact['evidence_scope'] == 'compile_time'
        assert [(t['role'], t['symbol']['descriptor']) for t in fact['targets']] == [('message', target)]
        assert not any(key in fact for key in ['lifetime', 'table', 'schema'])
        assert hits[0]['symbol']['descriptor'] == apis[kind], hits[0]
assert sum(len(r.get('domain_facts') or []) for r in rows) == len(expected)
# A second capture introduces genuinely unresolved invocations. Keep its
# stream separate so unresolved evidence cannot masquerade as a complete case.
broken = source + "\nclass Broken { void Check() { missing./*unresolved-publish*/Publish(c => new Output()); missing./*unresolved-event*/Event(() => {}); } }\n"
(a.output / 'Fixture.cs').write_text(broken)
result = subprocess.run([str(a.dotnet), str(a.worker), '--repo', 'fixture', '--root', str(a.output), '--project', 'Fixture.csproj', '--framework', 'net8.0', '--sdk-path', str(a.sdk)], env=env, check=False, capture_output=True, timeout=120)
assert result.returncode == 2, (result.returncode, result.stderr)
(a.output / 'unresolved.jsonl').write_bytes(result.stdout)
unresolved = [json.loads(line) for line in result.stdout.splitlines()]
for label in ['unresolved-publish', 'unresolved-event']:
    offset = broken.index('/*' + label + '*/') + len(label) + 4
    hits = [r for r in unresolved if r['record_type'] == 'reference' and r['source_path'] == 'Fixture.cs' and r['span']['byte_offset'] == offset]
    assert len(hits) == 1 and hits[0]['binding_status'] != 'resolved' and not hits[0].get('domain_facts'), (label, hits)
print('PASS: 6 exact MassTransit configuration positives, 8 overload/async/generic/lookalike exclusions, 2 unresolved exclusions')
