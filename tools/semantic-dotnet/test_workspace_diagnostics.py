#!/usr/bin/env python3
"""Offline regressions for tuple storage and native build-event severity."""
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
    ('warning', '<Warning Code="MOE001" Text="identical diagnostic text"/>', 0),
    ('duplicate_warning', '<Warning Code="MOE001" Text="identical diagnostic text"/><Warning Code="MOE001" Text="identical diagnostic text"/>', 0),
    ('error', '<Error Code="MOE001" Text="identical diagnostic text"/>', 2),
    ('warning_and_error', '<Warning Code="MOE001" Text="identical diagnostic text"/><Error Code="MOE001" Text="identical diagnostic text"/>', 2),
    ('compiler_warning_as_error', '', 2),
    ('missing_reference', '', 1),
]:
    root = home/name
    root.mkdir()
    shutil.copy(Path(__file__).parent/'NuGet.Config', root/'NuGet.Config')
    (root/'Probe.cs').write_text(('#warning promoted warning\n' if name == 'compiler_warning_as_error' else '') + source)
    project = root/'Probe.csproj'
    # Design-time-only target avoids confusing restore failure with extraction.
    project.write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework>' +
                      ('<TreatWarningsAsErrors>true</TreatWarningsAsErrors>' if name == 'compiler_warning_as_error' else '') +
                      '</PropertyGroup>' + ('<ItemGroup><ProjectReference Include="Missing.csproj"/></ItemGroup>' if name == 'missing_reference' else '') + '<Target Name="ProbeDiagnostic" BeforeTargets="CoreCompile" '
                      'Condition="\'$(DesignTimeBuild)\' == \'true\'">'+target+'</Target></Project>')
    assert run(name+'-restore', ['restore', project]).returncode == 0
    result = run(name+'-capture', [dll, '--repo', 'fixture', '--root', root, '--project', 'Probe.csproj',
                                  '--framework', 'net10.0', '--sdk-path', sdk])
    assert result.returncode == expected, (name, result.returncode, result.stderr)
    rows = [json.loads(line) for line in result.stdout.splitlines()]
    if name == 'missing_reference':
        assert b'FileNotFoundException' in result.stderr
        assert not any(r['record_type'] == 'stream_summary' for r in rows)
        results.append({'fixture': name, 'returncode': result.returncode, 'complete': False})
        continue
    assert rows[-1]['record_type'] == 'stream_summary'
    assert rows[-1]['compilation_status'] == ('complete' if expected == 0 else 'incomplete')
    diagnostics = [r for r in rows if r['record_type'] == 'diagnostic']
    proof = json.loads(next(r['capture_json'] for r in rows if r['record_type'] == 'project'))['build_diagnostics']
    if name == 'compiler_warning_as_error':
        assert any(r['code'] == 'CS1030' and r['severity'] == 'error' for r in diagnostics)
    elif name == 'missing_reference':
        assert not proof['verified']
    elif target:
        assert any(r['code'] == 'workspace' and r['severity'] == ('error' if expected else 'warning') and
                   '[Failure]' in r['message'] and 'identical diagnostic text' in r['message'] for r in diagnostics)
        assert proof['verified'] == (expected == 0)
        if name == 'duplicate_warning':
            assert proof['matched_warnings'] == 2
            assert len([r for r in diagnostics if r['code'] == 'workspace']) == 1
        if expected:
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
                    'build_diagnostics': proof, 'workspace_diagnostics': [r for r in diagnostics if r['code'] == 'workspace']})
# Fault injection exists only in this disposable copy, never the production CLI.
# Exercise the proof boundary using real logs from the actual workspace load.
fault_worker = home/'fault-worker'
shutil.copytree(home/'worker', fault_worker, ignore=shutil.ignore_patterns('bin', 'obj', '__pycache__'))
diagnostics_file = fault_worker/'BuildDiagnostics.cs'
diagnostics_file.write_text(diagnostics_file.read_text().replace('public BinaryLogger Logger',
                          'public string TestLogDirectory => directory;\n        public BinaryLogger Logger'))
worker_file = fault_worker/'Worker.cs'
worker_file.write_text(worker_file.read_text().replace(
    '        buildDiagnostics.Verify(States.Select(s => s.Project.FilePath!), workspaceIssues);', '''
        var fault = Path.GetFileName(Root);
        if (fault == "duplicate_diagnostic") workspaceIssues = [.. workspaceIssues, workspaceIssues[0]];
        if (fault == "missing_diagnostic") workspaceIssues = [];
        if (fault == "unmatched_diagnostic") workspaceIssues = [new WorkspaceDiagnostic(WorkspaceDiagnosticKind.Failure, "unmatched")];
        if (fault == "missing_log") foreach (var log in Directory.GetFiles(buildDiagnostics.TestLogDirectory)) File.Delete(log);
        if (fault == "truncated_log") foreach (var log in Directory.GetFiles(buildDiagnostics.TestLogDirectory)) {
            using var stream = File.OpenWrite(log); stream.SetLength(16);
        }
        buildDiagnostics.Verify(States.Select(s => s.Project.FilePath!), workspaceIssues);'''))
assert run('fault-build', ['build', fault_worker/'Moedex.SemanticWorker.csproj', '-p:UseSharedCompilation=false']).returncode == 0
for fault in ['duplicate_diagnostic', 'missing_diagnostic', 'unmatched_diagnostic', 'missing_log', 'truncated_log']:
    root = home/fault
    shutil.copytree(home/'warning', root, ignore=shutil.ignore_patterns('bin', 'obj'))
    assert run(fault+'-restore', ['restore', root/'Probe.csproj']).returncode == 0
    result = run(fault+'-capture', [fault_worker/'bin/Debug/net10.0/Moedex.SemanticWorker.dll',
                                  '--repo', 'fixture', '--root', root, '--project', 'Probe.csproj',
                                  '--framework', 'net10.0', '--sdk-path', sdk])
    assert result.returncode in (1, 2), (fault, result.returncode)
    rows = [json.loads(line) for line in result.stdout.splitlines()]
    assert not any(r['record_type'] == 'stream_summary' and r['compilation_status'] == 'complete' for r in rows)
    results.append({'fixture': fault, 'returncode': result.returncode, 'complete': False})
(home/'result.json').write_text(json.dumps(results, indent=2)+'\n')
print('PASS: tuple aliases/long tuples, verified warnings, identical-message errors, promoted compiler warning, missing reference, five proof fault controls')
