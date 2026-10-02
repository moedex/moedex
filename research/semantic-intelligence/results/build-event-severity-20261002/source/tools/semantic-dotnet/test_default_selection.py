#!/usr/bin/env python3
"""Compiler-selected closed defaults, including project references and negatives."""
import argparse
import json
import os
from pathlib import Path
import subprocess

p = argparse.ArgumentParser()
for key in ('dotnet', 'sdk', 'worker', 'packages', 'output'):
    p.add_argument('--' + key, required=True, type=Path)
p.add_argument('--expected-worker-version', choices=['8','9','10','11','12','13','14','15','16','17','18','19','20'], default='20')
a = p.parse_args()
a.output.mkdir(parents=True, exist_ok=False)
contracts = '''namespace Contracts;
public interface IBase { void Run(object value); }
public interface IHandler<T> : IBase {
 void Run(T value);
 void IBase.Run(object value) => Run((T)value);
}
public interface IMore<T> : IHandler<T> {
 void IBase.Run(object value) => Run((T)value);
}
public interface ILeft<T> : IHandler<T> {}
public interface IRight<T> : IHandler<T> {}
public interface IReabstract<T> : IHandler<T> { abstract void IBase.Run(object value); }
public interface IPlain : IBase { void IBase.Run(object value) {} }
public interface IBody<T> : IBase { void IBase.Run(object value) { System.Console.WriteLine(value); } }
'''
source = '''using Contracts;
namespace One { public class Message {} }
namespace Two { public class Message {} }
class First : IHandler<One.Message> { public void Run(One.Message value) {} }
class Second : IHandler<Two.Message> { public void Run(Two.Message value) {} }
class MostSpecific : IMore<One.Message> { public void Run(One.Message value) {} }
class Diamond : ILeft<One.Message>, IRight<One.Message> { public void Run(One.Message value) {} }
class Inherited : First {}
class ArbitraryBody : IBody<One.Message> {}
class Override : IHandler<One.Message> {
 public void Run(One.Message value) {} public void Run(object value) {}
}
class ExplicitOverride : IHandler<One.Message> {
 public void Run(One.Message value) {} void IBase.Run(object value) {}
}
class Reabstract : IReabstract<One.Message> {
 public void Run(One.Message value) {} public void Run(object value) {}
}
abstract class Abstract : IHandler<One.Message> { public abstract void Run(One.Message value); }
class Open<T> : IHandler<T> { public void Run(T value) {} }
class Array : IHandler<int[]> { public void Run(int[] value) {} }
class Plain : IPlain {}
struct Value : IHandler<One.Message> { public void Run(One.Message value) {} }
'''
# More than 32 selections suppress the entire class payload.
for i in range(33):
    contracts += f"public interface IBase{i} {{ void Run{i}(object value); }}\npublic interface IDefault{i}<T> : IBase{i} {{ void IBase{i}.Run{i}(object value) {{}} }}\n"
source += 'class Overflow : ' + ', '.join(f'IDefault{i}<One.Message>' for i in range(33)) + ' {}\n'
(a.output / 'global.json').write_text(json.dumps({'sdk': {'version': a.sdk.name, 'rollForward': 'disable'}}))
(a.output / 'NuGet.Config').write_text('<configuration><packageSources><clear /></packageSources></configuration>')
for directory, text in [('Contracts', contracts), ('App', source)]:
    d = a.output / directory
    d.mkdir()
    (d / 'Source.cs').write_text(text)
    reference = '<ItemGroup><ProjectReference Include="../Contracts/Contracts.csproj" /></ItemGroup>' if directory == 'App' else ''
    (d / (directory + '.csproj')).write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>' + reference + '</Project>')
env = dict(os.environ, NUGET_PACKAGES=str(a.packages.resolve()), DOTNET_NOLOGO='1', DOTNET_CLI_TELEMETRY_OPTOUT='1', DOTNET_PROCESSOR_COUNT='2')
subprocess.run([str(a.dotnet), 'restore', str(a.output / 'App/App.csproj')], env=env, cwd=a.output, check=True, timeout=120)
command = [str(a.dotnet), str(a.worker), '--repo', 'fixture', '--root', str(a.output), '--project', 'App/App.csproj', '--framework', 'net8.0', '--sdk-path', str(a.sdk)]

def capture(name, expected):
    result = subprocess.run(command, env=env, capture_output=True, timeout=120)
    (a.output / (name + '.jsonl')).write_bytes(result.stdout)
    (a.output / (name + '.stderr')).write_bytes(result.stderr)
    assert result.returncode == expected, (name, result.returncode, result.stderr)
    return [json.loads(line) for line in result.stdout.splitlines()]

rows = capture('capture', 0)
assert all(r['extractor_version'] == a.expected_worker_version and r['compilation_status'] == 'complete' for r in rows if r['record_type'] == 'project')
selections = {r['symbol']['descriptor']: r for r in rows if any(f['kind'] == 'interface_default_selection' for f in r.get('implementation_facts', []))}
expected = {'First': ('IHandler', 'One'), 'Second': ('IHandler', 'Two'), 'MostSpecific': ('IMore', 'One'), 'Diamond': ('IHandler', 'One'), 'Inherited': ('IHandler', 'One'), 'ArbitraryBody': ('IBody', 'One')}
assert set(selections) == {'T:' + c for c in expected}, selections.keys()
for cls, (owner, ns) in expected.items():
    row = selections['T:' + cls]
    assert row['record_type'] == 'declaration' and row['source_path'] == 'App/Source.cs'
    fact, = row['implementation_facts']
    assert fact['rule'] == 'csharp-interface-selection-v1' and fact['evidence_scope'] == 'compile_time'
    assert fact['implementing_type'] == row['symbol']
    assert fact['interface_symbol']['descriptor'] == 'M:Contracts.IBase.Run(System.Object)'
    selected = fact['selected_default_symbol']
    template = fact['default_template_symbol']
    assert selected['descriptor_kind'] == 'constructed_interface_method_v1'
    desc = json.loads(selected['descriptor'])
    assert desc['definition'] == template['descriptor'] == 'M:Contracts.' + owner + '`1.Contracts#IBase#Run(System.Object)'
    assert selected['namespace'] == template['namespace'] == 'fixture/Contracts/Contracts.csproj'
    assert desc['arguments'][0]['namespace'] == 'fixture/App/App.csproj'
    assert desc['arguments'][0]['descriptor'] == 'T:' + ns + '.Message'

if a.expected_worker_version in ('9', '10', '11', '12', '13', '14', '15', '16', '17', '18', '19', '20'):
    forwarded = {name: row['implementation_facts'][0]['forwarding'] for name, row in selections.items() if row['implementation_facts'][0].get('forwarding')}
    assert set(forwarded) == {'T:First', 'T:Second', 'T:Diamond'}, forwarded
    for name, f in forwarded.items():
        assert f['rule'] == 'csharp-default-forward-v1' and f['receiver'] == 'this' and f['conversion'] == 'explicit_type_parameter_cast'
        assert f['implementation_symbol']['descriptor'].startswith('M:' + name[2:] + '.Run(')
        assert f['call_project'] == 'Contracts/Contracts.csproj' and f['call_path'] == 'Contracts/Source.cs'

# An unresolved diamond is a compiler error: suppress interpreted facts for that project.
ambiguous = '''
interface IOther<T> : Contracts.IHandler<T> { void Contracts.IBase.Run(object value) {} }
class Ambiguous : Contracts.IMore<One.Message>, IOther<One.Message> { public void Run(One.Message value) {} }
'''
(a.output / 'App/Source.cs').write_text(source + ambiguous)
bad = capture('ambiguous', 2)
assert not any(r.get('implementation_facts') or r.get('domain_facts') for r in bad if r.get('project') == 'App/App.csproj')
assert any(r.get('record_type') == 'diagnostic' and 'CS8705' in str(r) for r in bad)
(a.output / 'App/Source.cs').write_text(source)
# A metadata-only default body cannot provide a captured source-template witness.
subprocess.run([str(a.dotnet), 'build', str(a.output / 'Contracts/Contracts.csproj'), '--no-restore'], env=env, cwd=a.output, check=True, timeout=120)
meta = a.output / 'Metadata'
meta.mkdir()
(meta / 'Metadata.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><Compile Include="../App/Source.cs"/><Reference Include="Contracts"><HintPath>../Contracts/bin/Debug/net8.0/Contracts.dll</HintPath></Reference></ItemGroup></Project>')
subprocess.run([str(a.dotnet), 'restore', str(meta / 'Metadata.csproj')], env=env, cwd=a.output, check=True, timeout=120)
command[command.index('App/App.csproj')] = 'Metadata/Metadata.csproj'
metadata = capture('metadata', 0)
assert not any(f['kind'] == 'interface_default_selection' for r in metadata for f in r.get('implementation_facts', []))
print('PASS: six class selections, distinct closed arguments, most-specific override, diamond, inherited default, arbitrary body; eight excluded classes/structs; 33-fact overflow; metadata-only defaults excluded; ambiguous compilation suppressed')
