#!/usr/bin/env python3
"""Offline regressions for tuple storage and fail-closed workspace diagnostics."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--dotnet', required=True)
p.add_argument('--sdk-path', required=True)
p.add_argument('--output-dir', required=True)
a = p.parse_args()
home = Path(a.output_dir).resolve()
home.mkdir(parents=True, exist_ok=False)
shutil.copytree(Path(__file__).parent, home/'worker',
                ignore=shutil.ignore_patterns('bin', 'obj', '__pycache__'))
env = dict(os.environ, DOTNET_CLI_HOME=str(home/'cli'), NUGET_PACKAGES=str(home/'packages'),
           DOTNET_PROCESSOR_COUNT='2', DOTNET_CLI_TELEMETRY_OPTOUT='1',
           MSBUILDDISABLENODEREUSE='1')
dotnet = str(Path(a.dotnet).resolve())
sdk = str(Path(a.sdk_path).resolve())


def run(name, args):
    result = subprocess.run([dotnet, *map(str, args)], env=env, capture_output=True, timeout=120)
    (home/(name+'.stdout')).write_bytes(result.stdout)
    (home/(name+'.stderr')).write_bytes(result.stderr)
    return result


build = run('build', ['build', home/'worker/Moedex.SemanticWorker.csproj', '-p:UseSharedCompilation=false'])
assert build.returncode == 0, build.stderr
dll = home/'worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll'
source = '''public class TupleProbe {
    public string Read((string Summary, string Description) value) => value.Summary + value.Item1 + value.Description;
    public int Long((int A, int B, int C, int D, int E, int F, int G, int H, int I) value) => value.H + value.Item8 + value.I;
    public int Plain { get; set; }
}
'''
results = []
for name, target, expected in [
    ('tuple', '', 0),
    ('warning', '<Warning Text="synthetic compatibility warning"/>', 2),
    ('error', '<Error Text="synthetic load failure"/>', 2),
]:
    root = home/name
    root.mkdir()
    shutil.copy(Path(__file__).parent/'NuGet.Config', root/'NuGet.Config')
    (root/'Probe.cs').write_text(source)
    project = root/'Probe.csproj'
    # Design-time-only target avoids confusing restore failure with extraction.
    project.write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework>'
                      '</PropertyGroup><Target Name="ProbeDiagnostic" BeforeTargets="CoreCompile" '
                      'Condition="\'$(DesignTimeBuild)\' == \'true\'">'+target+'</Target></Project>')
    assert run(name+'-restore', ['restore', project]).returncode == 0
    result = run(name+'-capture', [dll, '--repo', 'fixture', '--root', root, '--project', 'Probe.csproj',
                                  '--framework', 'net10.0', '--sdk-path', sdk])
    assert result.returncode == expected, (name, result.returncode, result.stderr)
    rows = [json.loads(line) for line in result.stdout.splitlines()]
    assert rows[-1]['record_type'] == 'stream_summary'
    assert rows[-1]['compilation_status'] == ('complete' if expected == 0 else 'incomplete')
    diagnostics = [r for r in rows if r['record_type'] == 'diagnostic']
    if target:
        assert any(r['code'] == 'workspace' and r['severity'] == 'error' and
                   '[Failure]' in r['message'] and 'synthetic' in r['message'] for r in diagnostics)
        assert not any(r.get('domain_facts') for r in rows)
    else:
        refs = [r for r in rows if r['record_type'] == 'reference' and r['source_path'] == 'Probe.cs']
        def symbol(text):
            return next(r['symbol'] for r in refs if r['source_text'] == text)
        for alias, canonical in [('Summary', 'Item1'), ('H', 'Item8')]:
            assert symbol(alias) == symbol(canonical), (alias, symbol(alias), symbol(canonical))
            assert symbol(alias)['namespace_kind'] == 'assembly'
            assert symbol(alias)['descriptor'].startswith('F:System.ValueTuple')
        assert symbol('Description')['descriptor'] != symbol('Summary')['descriptor']
        assert symbol('I')['descriptor'] != symbol('H')['descriptor']
    results.append({'fixture': name, 'returncode': result.returncode, 'summary': rows[-1],
                    'workspace_diagnostics': [r for r in diagnostics if r['code'] == 'workspace']})
(home/'result.json').write_text(json.dumps(results, indent=2)+'\n')
print('PASS: tuple aliases/long tuples, workspace warning and load error remain incomplete')
