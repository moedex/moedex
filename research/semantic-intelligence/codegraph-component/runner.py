#!/usr/bin/env python3
"""Prepare/build or capture unmodified CodeGraph extraction components.

Preparation/build is independent of gold. Capture requires an existing frozen
protocol, records its hash, and does not score or inspect answers.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import time

HERE = Path(__file__).resolve().parent
FILES = [
    'TC.CodeGraphApi.Extractors.CSharp/CodeGraphSyntaxWalker.cs',
    'TC.CodeGraphApi.Extractors.CSharp/SolutionAnalyzer.cs',
    'TC.CodeGraphApi.Extractors.CSharp/_GlobalUsings.cs',
    'TC.CodeGraphApi.Services/Extractors/ICodeExtractor.cs',
    'TC.CodeGraphApi.Services/Extractors/ISolutionAnalyzer.cs',
    'TC.CodeGraphApi.Services/Analyzers/LintResultCache.cs',
    'TC.CodeGraphApi.Services/Analyzers/ILintRunner.cs',
    'TC.CodeGraphApi.Services/Metadata/DotnetSupportInspector.cs',
    'TC.CodeGraphApi.Services/Configuration/IndexingOptions.cs',
    'TC.CodeGraphApi.Services/Pipeline/GraphBuffer.cs',
    'TC.CodeGraphApi.Models/GraphNode.cs',
    'TC.CodeGraphApi.Models/GraphEdge.cs',
    'TC.CodeGraphApi.Models/NodeLabel.cs',
    'TC.CodeGraphApi.Models/EdgeType.cs',
    'TC.CodeGraphApi.Models/PipelineTypes.cs',
    'TC.CodeGraphApi.Models/DotnetSupportInfo.cs',
    'TC.CodeGraphApi.Models/CodeGraphJsonDefaults.cs',
]
PACKAGES = {
    'Microsoft.CodeAnalysis.CSharp.Workspaces': '4.12.0',
    'Microsoft.CodeAnalysis.Workspaces.MSBuild': '4.12.0',
    'Microsoft.Build.Locator': '1.7.8',
    'Microsoft.Build.Tasks.Core': '17.14.28',
    'System.Security.Cryptography.Xml': '10.0.7',
    'Microsoft.Extensions.Logging.Abstractions': '10.0.7',
}

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def write(path, value):
    Path(path).write_text(json.dumps(value, indent=2) + '\n')

def execute(command, output, env, cwd=None, timeout=600):
    started = time.time()
    with open(str(output)+'.stdout', 'wb') as stdout, open(str(output)+'.stderr', 'wb') as stderr:
        result = subprocess.run(command, cwd=cwd, env=env, stdout=stdout, stderr=stderr, timeout=timeout)
    write(str(output)+'.command.json', dict(argv=command, cwd=str(cwd) if cwd else None,
          returncode=result.returncode, elapsed_seconds=time.time()-started))
    result.check_returncode()

def environment(dotnet, packages, home):
    env = {k:v for k,v in os.environ.items() if not k.startswith(('CODEGRAPH_', 'OPENAI_', 'ANTHROPIC_', 'GITLAB_', 'NUGET_', 'DOTNET_', 'MSBUILD'))}
    env.update(DOTNET_ROOT=str(dotnet.parent), PATH=str(dotnet.parent)+os.pathsep+env.get('PATH',''),
               NUGET_PACKAGES=str(packages), DOTNET_CLI_HOME=str(home),
               DOTNET_CLI_TELEMETRY_OPTOUT='1', DOTNET_NOLOGO='1', DOTNET_PROCESSOR_COUNT='2')
    return env

p=argparse.ArgumentParser()
s=p.add_subparsers(dest='mode',required=True)
b=s.add_parser('prepare'); b.add_argument('--reference',type=Path,required=True);b.add_argument('--output',type=Path,required=True);b.add_argument('--dotnet',type=Path,required=True);b.add_argument('--build',action='store_true')
c=s.add_parser('capture')
for key in ('prepared','source','output','dotnet','packages','protocol'):c.add_argument('--'+key,type=Path,required=True)
c.add_argument('--commit',required=True);c.add_argument('--solution',default='Sample-Outbox.sln');c.add_argument('--repo-name',default='Sample-Outbox')
a=p.parse_args()
a.output=a.output.resolve();a.dotnet=a.dotnet.resolve();a.output.mkdir(parents=True,exist_ok=False)
if a.mode=='prepare':
    a.reference=a.reference.resolve(); copied={}
    for relative in FILES:
        source=a.reference/'src'/relative;dest=a.output/'original'/relative
        dest.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(source,dest)
        assert sha(source)==sha(dest);copied['src/'+relative]=sha(source)
    shutil.copyfile(HERE/'Program.cs',a.output/'Program.cs')
    refs='\n'.join('<PackageReference Include="'+name+'" Version="'+version+'"'+(' ExcludeAssets="runtime"' if name=='Microsoft.Build.Tasks.Core' else '')+' />' for name,version in PACKAGES.items())
    (a.output/'CodeGraphComponent.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net10.0</TargetFramework><ImplicitUsings>enable</ImplicitUsings><Nullable>enable</Nullable><RestorePackagesWithLockFile>true</RestorePackagesWithLockFile></PropertyGroup><ItemGroup><FrameworkReference Include="Microsoft.AspNetCore.App" />'+refs+'</ItemGroup></Project>')
    (a.output/'NuGet.Config').write_text('<configuration><packageSources><clear /><add key="nuget.org" value="https://api.nuget.org/v3/index.json" /></packageSources></configuration>')
    write(a.output/'preparation.json',dict(version=1,reference=str(a.reference),original_sources=copied,
        reference_source_roster_sha256=hashlib.sha256(json.dumps(copied,sort_keys=True,separators=(',',':')).encode()).hexdigest(),
        original_project_sha256=sha(a.reference/'src/TC.CodeGraphApi.Extractors.CSharp/TC.CodeGraphApi.Extractors.CSharp.csproj'),
        dependency_pins=PACKAGES,adapter_sources={f.name:sha(f) for f in (HERE/'Program.cs',HERE/'runner.py')},
        limitations=['Unmodified extraction components; isolated host project substitutes for full private-package project graph.','No native MCP/query/store or private IndexingPipeline.ResolveCalls.','Build only; no application extraction or scoring performed.']))
    if a.build:
        env=environment(a.dotnet,a.output/'packages',a.output/'home')
        execute([str(a.dotnet),'restore',str(a.output/'CodeGraphComponent.csproj'),'--configfile',str(a.output/'NuGet.Config')],a.output/'restore',env)
        execute([str(a.dotnet),'build',str(a.output/'CodeGraphComponent.csproj'),'--no-restore','-c','Release','--nologo'],a.output/'build',env)
        hashes={str(f.relative_to(a.output)):sha(f) for f in (a.output/'bin/Release/net10.0').rglob('*') if f.is_file()}
        write(a.output/'build-provenance.json',dict(files=hashes,lock_sha256=sha(a.output/'packages.lock.json'),sdk=str(a.dotnet),dotnet_sha256=sha(a.dotnet),sdk_version=subprocess.check_output([str(a.dotnet),'--version'],env=env,text=True).strip()))
else:
    a.prepared=a.prepared.resolve();a.source=a.source.resolve();a.protocol=a.protocol.resolve();a.packages=a.packages.resolve()
    if not a.protocol.is_file():raise ValueError('Frozen component protocol required')
    preparation=json.loads((a.prepared/'preparation.json').read_text())
    for name,digest in preparation['original_sources'].items():
        if sha(a.prepared/'original'/name.removeprefix('src/'))!=digest:raise ValueError('Modified prepared source '+name)
    if sha(a.prepared/'Program.cs')!=preparation['adapter_sources']['Program.cs']:raise ValueError('Modified prepared host')
    build=json.loads((a.prepared/'build-provenance.json').read_text())
    for name,digest in build['files'].items():
        if sha(a.prepared/name)!=digest:raise ValueError('Modified prepared binary '+name)
    head=subprocess.check_output(['git','-C',str(a.source),'rev-parse','HEAD'],text=True).strip()
    if head!=a.commit:raise ValueError('Source pin mismatch')
    if subprocess.check_output(['git','-C',str(a.source),'status','--porcelain','--untracked-files=no']):raise ValueError('Source has tracked changes')
    archive=subprocess.check_output(['git','-C',str(a.source),'archive','--format=tar',head])
    root=a.output/'source';root.mkdir()
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:tar.extractall(root,filter='data')
    original={str(f.relative_to(root)):sha(f) for f in root.rglob('*') if f.is_file()}
    if (root/'NuGet.Config').exists():raise ValueError('Refuse to overwrite tracked NuGet config')
    (root/'NuGet.Config').write_text('<configuration><packageSources><clear /></packageSources></configuration>')
    env=environment(a.dotnet,a.packages,a.output/'home')
    write(a.output/'inputs.json',dict(commit=head,protocol=str(a.protocol),protocol_sha256=sha(a.protocol),
        prepared_manifest_sha256=sha(a.prepared/'preparation.json'),build_manifest_sha256=sha(a.prepared/'build-provenance.json'),
        capture_runner_sha256=sha(HERE/'runner.py'),dotnet_sha256=sha(a.dotnet),source_files=original,generated_nuget_config_sha256=sha(root/'NuGet.Config'),cpu_count_limit=2))
    execute([str(a.dotnet),str(a.prepared/'bin/Release/net10.0/CodeGraphComponent.dll'),str(root),str(root/a.solution),str(a.output/'native'),a.repo_name],a.output/'capture',env,root)
    for name,digest in original.items():
        if sha(root/name)!=digest:raise ValueError('Tracked source changed '+name)
    coverage=json.loads((a.output/'native/coverage.json').read_text())
    documents={d['path']:d['sha256'] for project in coverage['projects'] for d in project['documents']}
    missing=[name for name,digest in original.items() if name.endswith('.cs') and documents.get(name)!=digest]
    errors=[error for project in coverage['projects'] for error in (project['errors'] or [])]
    complete=not missing and not errors and all(project['compilationAvailable'] for project in coverage['projects']) and not coverage['workspaceDiagnostics']
    write(a.output/'source-verification.json',dict(unchanged=True,files=len(original),commit=head, supplemental_workspace_complete=complete,missing_or_changed_csharp=missing,compilation_errors=errors))
    if not complete:raise ValueError('Supplemental workspace incomplete; capture retained, comparison gate not passed')
