import hashlib,json,subprocess,tarfile,urllib.request
from pathlib import Path
root=Path('.local/holdout-cleanarchitecture');out=root/'readiness'
url='https://dotnetcli.blob.core.windows.net/dotnet/release-metadata/10.0/releases.json'
raw=urllib.request.urlopen(url,timeout=45).read();(out/'dotnet-releases.json').write_bytes(raw)
release=json.loads(raw)
sdks=[s for r in release['releases'] for s in r.get('sdks',[r.get('sdk',{})]) if s.get('version')=='10.0.401']
f=next(f for s in sdks for f in s['files'] if f['rid']=='osx-arm64' and f['url'].endswith('.tar.gz'))
(out/'sdk-selection.json').write_text(json.dumps({'version':'10.0.401','metadata_url':url,'metadata_sha256':hashlib.sha256(raw).hexdigest(),**f},indent=2)+'\n')
archive=root/'sdk-10.0.401.tar.gz'
subprocess.run(['curl','--fail','--location','--retry','2','--max-time','240','--output',str(archive),f['url']],check=True)
h=hashlib.sha512(archive.read_bytes()).hexdigest();assert h.lower()==f['hash'].lower()
dest=root/'dotnet';dest.mkdir(exist_ok=False)
with tarfile.open(archive) as t:t.extractall(dest,filter='data')
r=subprocess.run([str(dest.resolve()/'dotnet'),'--info'],capture_output=True,check=True)
(out/'sdk-info.txt').write_bytes(r.stdout)
print(json.dumps({'version':'10.0.401','sha512_verified':True,'archive_bytes':archive.stat().st_size,'dotnet':str(dest/'dotnet')}))
