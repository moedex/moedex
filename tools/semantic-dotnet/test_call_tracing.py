#!/usr/bin/env python3
"""Capture a real-SDK call fixture offline and verify native compiler tracing."""
import argparse
import json
import os
from pathlib import Path
import subprocess


SOURCE = '''using System;
public interface IService { void Send(); }
public class One : IService {
    public void Send() { Step(); }
    public void Step() { Send(); }
    public void Send(int value) { }
}
public class Two { public void Send() { } }
public class Controller {
    public void Entry(IService service) { service.Send(); }
    public void Other(Two service) { service.Send(); }
    public void References(One service) {
        Action group = service.Send;
        Action lambda = () => service.Send();
        var made = new Two();
    }
}
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('moedex', 'dotnet', 'sdk-path', 'worker', 'output-dir'):
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    base = Path(args.output_dir).resolve()
    base.mkdir(parents=True, exist_ok=False)
    repo = base / 'Fixture'
    repo.mkdir()
    (repo / 'App.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup>'
                                  '<TargetFramework>net10.0</TargetFramework>'
                                  '</PropertyGroup></Project>')
    (repo / 'Flow.cs').write_text(SOURCE)

    def git(*argv):
        return subprocess.check_output(['git', '-C', str(repo), *argv],
                                       stderr=subprocess.STDOUT).decode().strip()

    git('init', '-q')
    git('config', 'user.name', 'Call Fixture')
    git('config', 'user.email', 'fixture@example.invalid')
    git('remote', 'add', 'origin', 'https://example.com/Fixture.git')
    git('add', '.')
    git('commit', '-qm', 'call fixture')
    artifact = base / 'artifact.json'
    captured = subprocess.run([str(Path(args.moedex).resolve()), 'semantic', 'capture',
                              '--checkout', str(repo), '--repo', 'Fixture',
                              '--commit', git('rev-parse', 'HEAD'),
                              '--origin', 'https://example.com/Fixture.git',
                              '--project', 'App.csproj', '--framework', 'net10.0',
                              '--dotnet', str(Path(args.dotnet).resolve()),
                              '--sdk-path', str(Path(args.sdk_path).resolve()),
                              '--worker', str(Path(args.worker).resolve()), '--restore-offline',
                              '--workspace', str(base / 'workspace'), '--output', str(artifact),
                              '--timeout', '2m', '--json'], capture_output=True, timeout=180)
    (base / 'capture.stdout').write_bytes(captured.stdout)
    (base / 'capture.stderr').write_bytes(captured.stderr)
    assert captured.returncode == 0, captured.stderr.decode()
    env = dict(os.environ, MOEDEX_CALL_ARTIFACT=str(artifact))
    tested = subprocess.run(['go', 'test', './internal/semanticimport', '-run',
                             '^TestPublicCompilerCallTracing$', '-count=1', '-v'],
                            env=env, capture_output=True, timeout=180)
    (base / 'test.stdout').write_bytes(tested.stdout)
    (base / 'test.stderr').write_bytes(tested.stderr)
    assert tested.returncode == 0, tested.stdout.decode() + tested.stderr.decode()
    assert 'SKIP' not in tested.stdout.decode()
    (base / 'result.json').write_text(json.dumps({'passed': True, 'commit': git('rev-parse', 'HEAD'),
                                                'capture': json.loads(captured.stdout)}, indent=2) + '\n')
    print('PASS: exact interface targets, qualified homonyms, overloads, cycles and reference exclusions')


if __name__ == '__main__':
    main()
