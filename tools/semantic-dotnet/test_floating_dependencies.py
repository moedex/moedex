#!/usr/bin/env python3
"""Real-SDK offline capture: floating versions resolve only from a verified bundle."""
import argparse
import base64
import gzip
import hashlib
import json
import subprocess
import zipfile
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('moedex', 'dotnet', 'sdk-path', 'worker', 'output-dir'):
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    base = Path(args.output_dir).resolve()
    base.mkdir(parents=True, exist_ok=False)
    repo = base / 'corpus/Fixture'
    repo.mkdir(parents=True)
    moe = str(Path(args.moedex).resolve())
    dotnet = str(Path(args.dotnet).resolve())
    sdk = Path(args.sdk_path).resolve()
    feed = base / 'feed'
    feed.mkdir()
    # A content-only package needs no compiler or external dependencies.
    with zipfile.ZipFile(feed / 'offline.fixture.1.2.3.nupkg', 'w') as package:
        package.writestr('Offline.Fixture.nuspec', '''<?xml version="1.0"?>
<package><metadata><id>Offline.Fixture</id><version>1.2.3</version>
<authors>Fixture</authors><description>Offline fixture</description>
</metadata></package>''')
    config = base / 'NuGet.Config'
    config.write_text('<configuration><packageSources><clear /></packageSources>'
                      '<fallbackPackageFolders><clear /></fallbackPackageFolders></configuration>')
    project = repo / 'App.csproj'

    def set_version(version):
        project.write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup>'
                           '<TargetFramework>net10.0</TargetFramework></PropertyGroup>'
                           '<ItemGroup><PackageReference Include="Offline.Fixture" Version="' +
                           version + '" /></ItemGroup></Project>')

    def run(name, argv):
        result = subprocess.run(list(map(str, argv)), capture_output=True, timeout=180)
        (base / (name + '.stdout')).write_bytes(result.stdout)
        (base / (name + '.stderr')).write_bytes(result.stderr)
        return result

    def git(*argv):
        return subprocess.check_output(['git', '-C', str(repo), *argv],
                                       stderr=subprocess.STDOUT).decode().strip()

    set_version('1.*')
    (repo / 'Api.cs').write_text('public static class Api { public static int Read() => 42; }')
    provision = run('provision', [dotnet, sdk / 'MSBuild.dll', '-target:Restore', project,
                                 '-p:RestoreConfigFile=' + str(config),
                                 '-p:RestoreSources=' + str(feed),
                                 '-p:RestorePackagesPath=' + str(base / 'packages'),
                                 '-p:NuGetAudit=false', '-nologo'])
    assert provision.returncode == 0, provision.stdout.decode() + provision.stderr.decode()
    packed = run('pack', [moe, 'semantic', 'dependencies', 'pack', '--packages',
                          base / 'packages', '--output', base / 'bundle'])
    assert packed.returncode == 0, packed.stderr.decode()
    git('init', '-q')
    git('config', 'user.name', 'Capture Fixture')
    git('config', 'user.email', 'fixture@example.invalid')
    git('remote', 'add', 'origin', 'https://example.com/Fixture.git')
    reports = []
    for name, version, bundled, success in [
        ('floating-present', '1.*', True, True),
        ('floating-absent', '2.*', True, False),
        ('no-bundle', '1.*', False, False),
    ]:
        set_version(version)
        git('add', 'App.csproj', 'Api.cs')
        git('commit', '--allow-empty', '-qm', name)
        output = base / (name + '.semantic')
        argv = [moe, 'semantic', 'capture', '--checkout', repo, '--repo', 'Fixture',
                '--commit', git('rev-parse', 'HEAD'), '--origin', 'https://example.com/Fixture.git',
                '--project', 'App.csproj', '--framework', 'net10.0', '--dotnet', dotnet,
                '--sdk-path', sdk, '--worker', Path(args.worker).resolve(), '--restore-offline',
                '--workspace', base / (name + '-workspace'), '--output', output, '--timeout', '2m']
        if bundled:
            argv += ['--dependency-bundle', base / 'bundle']
        result = run(name, argv)
        assert (result.returncode == 0) == success, result.stderr.decode()
        report = dict(name=name, version=version, bundled=bundled, returncode=result.returncode)
        if success:
            envelope = json.loads(output.read_text())
            payload = base64.b64decode(envelope['payload'], validate=True)
            if envelope['version'] == 2:
                payload = gzip.decompress(payload)
                assert len(payload) == envelope['payload_bytes']
            assert hashlib.sha256(payload).hexdigest() == envelope['sha256']
            artifact = json.loads(payload)
            assert len(artifact['contexts']) == 1
            bundle_sha = hashlib.sha256((base / 'bundle/manifest.json').read_bytes()).hexdigest()
            assert artifact['contexts'][0]['dependency_bundle_sha256'] == bundle_sha
            summary = json.loads(result.stdout)
            assert summary['dependency_bundle_sha256'] == bundle_sha
            workspace = Path(summary['workspace'])
            assets = json.loads((workspace / 'obj/project.assets.json').read_text())
            assert set(assets['libraries']) == {'Offline.Fixture/1.2.3'}
            # SDKs may also supply their own local library-packs directory.
            allowed = {str(workspace.parent.parent / 'packages'),
                       str(sdk.parent.parent / 'library-packs')}
            assert set(assets['project']['restore']['sources']) <= allowed
            assert any(s['key']['descriptor'] == 'M:Api.Read' for s in artifact['symbols'])
            report['artifact_sha256'] = hashlib.sha256(output.read_bytes()).hexdigest()
        else:
            assert not output.exists()
            assert not (base / (name + '-workspace')).exists()
            assert 'offline restore' in result.stderr.decode()
            assert ('NU1102' if bundled else 'NU1101') in result.stderr.decode()
            report['no_artifact_or_workspace'] = True
        reports.append(report)
        (base / 'result.json').write_text(json.dumps(reports, indent=2) + '\n')
    print('PASS: floating package resolved offline; absent version and absent bundle fail closed')


if __name__ == '__main__':
    main()
