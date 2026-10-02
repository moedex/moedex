#!/usr/bin/env python3
"""Bounded forwarding operation controls; negatives must retain selection only."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

p = argparse.ArgumentParser()
for key in ('dotnet', 'sdk', 'worker', 'packages', 'output'):
    p.add_argument('--' + key, required=True, type=Path)
a = p.parse_args()
a.output.mkdir(parents=True, exist_ok=False)
source = '''using System.Threading.Tasks;
interface IBase { void Run(object value); }
class Message {}
'''
cases = {
    'Direct': ('void IBase.Run(object value) => Accept((T)value);', '', 'public void Accept(Message value) {}', True),
    'This': ('void IBase.Run(object value) => this.Accept((T)value);', '', 'public void Accept(Message value) {}', True),
    'Block': ('void IBase.Run(object value) { Accept((T)value); }', '', 'public void Accept(Message value) {}', True),
    'Conditional': ('void IBase.Run(object value) { if(value != null) Accept((T)value); }', '', 'public void Accept(Message value) {}', False),
    'Other': ('void IBase.Run(object value) => Receiver.Accept((T)value);', 'IOther<T> Receiver {get;}', 'public IOther<Message> Receiver => this; public void Accept(Message value) {}', False),
    'NewArgument': ('void IBase.Run(object value) => Accept((T)new object());', '', 'public void Accept(Message value) {}', False),
    'TwoCalls': ('void IBase.Run(object value) { Accept((T)value); Accept((T)value); }', '', 'public void Accept(Message value) {}', False),
    'NestedCast': ('void IBase.Run(object value) => Accept((T)(object)value);', '', 'public void Accept(Message value) {}', False),
    'AsCast': ('void IBase.Run(object value) => Accept(value as T);', '', 'public void Accept(Message value) {}', False),
}
for name, (body, extra, implementation, _) in cases.items():
    source += f'interface I{name}<T> : IBase where T:class {{ void Accept(T value); {extra} {body} }}\n'
    source += f'class {name} : I{name}<Message> {{ {implementation} }}\n'
source += '''
interface IAsyncBase { Task Run(object value); }
interface ITask<T> : IAsyncBase { Task Accept(T value); Task IAsyncBase.Run(object value) { return Accept((T)value); } }
class TaskReturn : ITask<Message> { public Task Accept(Message value) => Task.CompletedTask; }
interface IAwait<T> : IAsyncBase { Task Accept(T value); async Task IAsyncBase.Run(object value) { await Accept((T)value); } }
class AsyncBody : IAwait<Message> { public Task Accept(Message value) => Task.CompletedTask; }
interface IOrdinal<A,B> : IBase { void Accept(B value); void IBase.Run(object value) => Accept((B)value); }
class Ordinal : IOrdinal<string,Message> { public void Accept(Message value) {} }
interface IGenericCall<T> : IBase { void Accept<U>(U value); void IBase.Run(object value) => Accept<T>((T)value); }
class GenericCall : IGenericCall<Message> { public void Accept<U>(U value) {} }
interface IMultiArgument<T> : IBase { void Accept(T value, int n); void IBase.Run(object value) => Accept((T)value, 1); }
class MultiArgument : IMultiArgument<Message> { public void Accept(Message value, int n) {} }
interface INoCast<T> : IBase { void Accept(object value); void IBase.Run(object value) => Accept(value); }
class NoCast : INoCast<Message> { public void Accept(object value) {} }
'''
(a.output / 'global.json').write_text(json.dumps({'sdk': {'version': a.sdk.name, 'rollForward': 'disable'}}))
(a.output / 'Fixture.cs').write_text(source, encoding='utf-8-sig')
(a.output / 'Fixture.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>')
(a.output / 'NuGet.Config').write_text('<configuration><packageSources><clear /></packageSources></configuration>')
env = dict(os.environ, NUGET_PACKAGES=str(a.packages.resolve()), DOTNET_NOLOGO='1', DOTNET_CLI_TELEMETRY_OPTOUT='1', DOTNET_PROCESSOR_COUNT='2')
subprocess.run([str(a.dotnet), 'restore', str(a.output / 'Fixture.csproj')], env=env, cwd=a.output, check=True, timeout=120)
command = [str(a.dotnet), str(a.worker), '--repo', 'fixture', '--root', str(a.output), '--project', 'Fixture.csproj', '--framework', 'net8.0', '--sdk-path', str(a.sdk)]
r = subprocess.run(command, env=env, capture_output=True, timeout=120)
(a.output / 'capture.jsonl').write_bytes(r.stdout)
(a.output / 'capture.stderr').write_bytes(r.stderr)
assert r.returncode == 0, (r.returncode, r.stderr)
rows = [json.loads(line) for line in r.stdout.splitlines()]
selections = {row['symbol']['descriptor']: row['implementation_facts'][0] for row in rows if any(f['kind'] == 'interface_default_selection' for f in row.get('implementation_facts', []))}
expected = {'Direct', 'This', 'Block', 'TaskReturn', 'Ordinal'}
assert len(selections) == 15, selections.keys()
assert {name[2:] for name, f in selections.items() if f.get('forwarding')} == expected
raw = (a.output / 'Fixture.cs').read_bytes()
for name in expected:
    fact = selections['T:' + name]
    f = fact['forwarding']
    assert f['receiver'] == 'this' and f['conversion'] == 'explicit_type_parameter_cast'
    assert f['type_parameter_ordinal'] == (1 if name == 'Ordinal' else 0)
    assert f['cast_type']['descriptor'] == 'T:Message'
    assert f['implementation_symbol']['descriptor'] == 'M:' + name + '.Accept(Message)'
    assert f['call_sha256'] == hashlib.sha256(raw).hexdigest()
    offset, length = f['call_span']['byte_offset'], f['call_span']['byte_length']
    assert raw[offset:offset + length] == b'Accept'
    witnesses = [r for r in rows if r.get('source_path') == 'Fixture.cs' and r.get('span') == f['call_span'] and r['record_type'] == 'reference']
    witness, = witnesses
    assert witness['enclosing_symbol']['descriptor'] == fact['default_template_symbol']['descriptor']
    assert witness['symbol']['descriptor'] == json.loads(f['interface_symbol']['descriptor'])['definition']
(a.output / 'Fixture.cs').write_bytes(raw + b'\nclass Broken { MissingType value; }')
bad = subprocess.run(command, env=env, capture_output=True, timeout=120)
(a.output / 'incomplete.jsonl').write_bytes(bad.stdout)
assert bad.returncode == 2
assert not any(r.get('implementation_facts') for r in map(json.loads, bad.stdout.splitlines()))
(a.output / 'Fixture.cs').write_bytes(raw)
print('PASS: five forwarding forms, ten selection-only controls, qualified substitution including ordinal one, exact BOM call witnesses, incomplete suppression')
