#!/usr/bin/env python3
"""Offline CLI regression: project-built netstandard generator, custom outputs and integrity."""
import argparse,base64,hashlib,json,subprocess
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--moedex',required=True);p.add_argument('--dotnet',required=True);p.add_argument('--sdk-path',required=True);p.add_argument('--worker',required=True);p.add_argument('--dependency-bundle',required=True);p.add_argument('--output-dir',required=True);a=p.parse_args()
base=Path(a.output_dir).resolve();base.mkdir(parents=True,exist_ok=False);repo=base/'corpus/Fixture';repo.mkdir(parents=True)
def write(path,text):
 q=repo/path;q.parent.mkdir(parents=True,exist_ok=True);q.write_text(text)
def git(*args):return subprocess.check_output(['git','-C',str(repo),*args],stderr=subprocess.STDOUT).decode().strip()
write('Directory.Build.props','<Project><PropertyGroup><UseArtifactsOutput>true</UseArtifactsOutput></PropertyGroup></Project>')
write('App/App.csproj','<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFrameworks>net10.0;netstandard2.0</TargetFrameworks></PropertyGroup><ItemGroup><ProjectReference Include="../Library/Library.csproj" /><ProjectReference Include="../Generator/Generator.csproj" ReferenceOutputAssembly="false" OutputItemType="Analyzer" /></ItemGroup></Project>')
write('Library/Library.csproj','<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>netstandard2.0</TargetFramework></PropertyGroup></Project>')
write('Library/Shared.cs','public static class Shared { public static int Number => 1; }')
write('App/Api.cs','public static class Api { public static int Read() => FixtureGenerated.Value + Shared.Number; }')
write('Generator/Generator.csproj','<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>netstandard2.0</TargetFramework><LangVersion>latest</LangVersion></PropertyGroup><ItemGroup><PackageReference Include="Microsoft.CodeAnalysis.CSharp" Version="4.12.0" PrivateAssets="all" /></ItemGroup></Project>')
write('Generator/Generate.cs','''using Microsoft.CodeAnalysis;
[Generator] public sealed class Generate : ISourceGenerator {
public void Initialize(GeneratorInitializationContext c) {}
public void Execute(GeneratorExecutionContext c) {
#if DEBUG
c.AddSource("Value.g.cs", "public static class FixtureGenerated { public const int Value = 42; }");
#else
c.AddSource("Value.g.cs", "public static class FixtureGenerated { public const int Value = 84; }");
#endif
} }
''')
git('init','-q');git('config','user.name','Capture Fixture');git('config','user.email','fixture@example.invalid');git('remote','add','origin','https://example.com/Fixture.git')
reports=[]
def capture(name,configuration='Debug',success=True,expected_error=None):
 git('add','Directory.Build.props','App','Generator','Library');git('commit','-qm',name);commit=git('rev-parse','HEAD');out=base/name;out.mkdir()
 argv=[str(Path(a.moedex).resolve()),'semantic','capture','--checkout',str(repo),'--repo','Fixture','--commit',commit,'--origin','https://example.com/Fixture.git','--project','App/App.csproj','--framework','net10.0','--configuration',configuration,'--dotnet',str(Path(a.dotnet).resolve()),'--sdk-path',str(Path(a.sdk_path).resolve()),'--worker',str(Path(a.worker).resolve()),'--dependency-bundle',str(Path(a.dependency_bundle).resolve()),'--restore-offline','--workspace',str(out/'workspace'),'--output',str(out/'project.semantic'),'--timeout','2m']
 r=subprocess.run(argv,capture_output=True,timeout=130);(out/'stdout').write_bytes(r.stdout);(out/'stderr').write_bytes(r.stderr);row=dict(name=name,argv=argv,returncode=r.returncode);reports.append(row);(base/'result.json').write_text(json.dumps(reports,indent=2)+'\n')
 assert (r.returncode==0)==success,r.stderr.decode()
 if not success:
  assert expected_error in r.stderr.decode(),r.stderr.decode();assert not (out/'project.semantic').exists();return
 artifact=json.loads(base64.b64decode(json.loads((out/'project.semantic').read_text())['payload']));summary=json.loads(r.stdout)
 assert len(artifact['contexts'])==2
 context=next(c for c in artifact['contexts'] if c['project']=='App/App.csproj')
 library=next(c for c in artifact['contexts'] if c['project']=='Library/Library.csproj')
 assert json.loads(library['capture'])['evaluated_properties']['TargetFramework']=='netstandard2.0';assert context['extractor_version']=='21'
 cap=json.loads(context['capture']);assert cap['evaluated_properties']['TargetFramework']=='net10.0'
 analyzers=[x for x in cap['analyzers'] if x['path'].endswith('/Generator.dll')];assert len(analyzers)==1 and analyzers[0]['scope']=='source'
 workspace=Path(summary['workspace']);dll=workspace/analyzers[0]['path'];assert hashlib.sha256(dll.read_bytes()).hexdigest()==analyzers[0]['sha256']
 expected='42' if configuration=='Debug' else '84'
 # Generated source bytes are encoded by Go's []byte JSON representation.
 generated=[base64.b64decode(s['content']).decode() for s in artifact['sources'] if s.get('generated')]
 assert any('Value = '+expected in s for s in generated),generated
 symbols={s['id'] for s in artifact['symbols'] if s['key']['descriptor']=='F:FixtureGenerated.Value'}
 occurrences={o['id']:o for o in artifact['occurrences']}
 sources={s['id']:s for s in artifact['sources']}
 uses=[occurrences[b['occurrence_id']] for b in artifact['bindings'] if b.get('symbol_id') in symbols and b['status']=='resolved']
 assert any(o['role']=='reference' and sources[o['source_id']]['path']=='App/Api.cs' for o in uses)
 assert any(o['role']=='declaration' and sources[o['source_id']].get('generated') for o in uses)
 roots=out/'roots.json';roots.write_text(json.dumps({s['id']:str(workspace) for s in artifact['snapshots']}))
 publish=[str(Path(a.moedex).resolve()),'index','snapshot','build','--corpus',str(base/'corpus'),'--index-dir',str(out/'index'),'--id',name,'--graph=false','--semantic-artifact',str(out/'project.semantic'),'--semantic-workspaces',str(roots)]
 result=subprocess.run(publish,capture_output=True,timeout=60);(out/'publish.stdout').write_bytes(result.stdout);(out/'publish.stderr').write_bytes(result.stderr);assert result.returncode==0,result.stderr.decode()
 original=dll.read_bytes();dll.write_bytes(original+b'changed')
 publish[publish.index('--index-dir')+1]=str(out/'damaged-index')
 damaged=subprocess.run(publish,capture_output=True,timeout=60);(out/'damaged.stderr').write_bytes(damaged.stderr);assert damaged.returncode!=0
 dll.write_bytes(original)
 row.update(generated_value=int(expected),analyzer_hash_verified=True,publication=True,tampered_analyzer_rejected=True,artifact_sha256=hashlib.sha256((out/'project.semantic').read_bytes()).hexdigest())
 (base/'result.json').write_text(json.dumps(reports,indent=2)+'\n')
capture('debug')
write('App/Marker.cs','internal class ConfigurationMarker {}')
capture('release','Release')
write('Generator/Broken.cs','this does not compile')
capture('broken-generator',success=False,expected_error='project reference preparation')
(repo/'Generator/Broken.cs').unlink()
q=repo/'App/App.csproj';q.write_text(q.read_text().replace('</Project>','<ItemGroup><Analyzer Include="missing.dll" /></ItemGroup></Project>'))
capture('missing-analyzer',success=False,expected_error='required analyzer input missing')
q.write_text(q.read_text().replace('<ItemGroup><Analyzer Include="missing.dll" /></ItemGroup>',''))
write('Directory.Build.props','<Project><PropertyGroup><UseArtifactsOutput>true</UseArtifactsOutput><ArtifactsPivots>$(Configuration)</ArtifactsPivots></PropertyGroup></Project>')
capture('ambiguous-framework-output',success=False,expected_error='framework identity is missing or ambiguous')
print(json.dumps(reports,indent=2))
