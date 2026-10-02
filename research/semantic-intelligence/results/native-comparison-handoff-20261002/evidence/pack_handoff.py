import hashlib,json,shutil,zipfile
from pathlib import Path
base=Path('.local/codegraph-native-provenance');out=base/'handoff';out.mkdir()
def copy(p,d):d.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(p,d)
for folder in ('codegraph-native','agent-journeys'):
 for p in (Path('research/semantic-intelligence')/folder).iterdir():
  if p.is_file() and p.suffix in ('.py','.md','.cs','.csproj'):copy(p,out/folder/p.name)
copy(base/'reference-fingerprint.json',out/'reference-fingerprint.json')
copy(base/'NuGet.Config',out/'offline.NuGet.Config')
for p in (base/'final').iterdir():
 if p.is_file():copy(p,out/'evidence'/p.name)
for name in ('CODEGRAPH-NATIVE-PREFLIGHT.md','COMPACT-ARTIFACT-ACCEPTANCE.md'):copy(Path('research/semantic-intelligence')/name,out/'historical-reports'/name)
copy(Path('research/semantic-intelligence/codegraph-native/MACHINE-HANDOFF.md'),out/'START-HERE.md')
(out/'verify_bundle.py').write_text('''import hashlib,json
from pathlib import Path
root=Path(__file__).resolve().parent
m=json.loads((root/'manifest.json').read_text())
for f in m['files']:
 p=(root/f['path']).resolve()
 assert p.is_relative_to(root), f['path']
 data=p.read_bytes()
 assert len(data)==f['bytes'] and hashlib.sha256(data).hexdigest()==f['sha256'], f['path']
print('Verified',len(m['files']),'packet files. Checksums establish integrity, not a trusted signature.')
''')
manifest={'schema':'native-comparison-machine-handoff-v1','classification':'offline migration packet; native comparison unexecuted','files':[{'path':p.relative_to(out).as_posix(),'bytes':p.stat().st_size,'sha256':hashlib.sha256(p.read_bytes()).hexdigest()} for p in sorted(out.rglob('*')) if p.is_file()]}
(out/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
archive=base/'native-comparison-handoff.zip'
with zipfile.ZipFile(archive,'x',compression=zipfile.ZIP_DEFLATED,compresslevel=9) as z:
 for p in sorted(out.rglob('*')):
  if p.is_file():
   info=zipfile.ZipInfo(p.relative_to(out).as_posix(),date_time=(2026,10,2,0,0,0));info.compress_type=zipfile.ZIP_DEFLATED;info.external_attr=0o100644<<16;z.writestr(info,p.read_bytes())
print(json.dumps({'path':str(archive),'bytes':archive.stat().st_size,'sha256':hashlib.sha256(archive.read_bytes()).hexdigest(),'files':len(manifest['files'])+1}))
with zipfile.ZipFile(archive) as z:z.extractall(base/'handoff-test')
