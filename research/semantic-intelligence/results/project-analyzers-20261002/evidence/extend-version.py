import json,re,shutil
from pathlib import Path
files=json.loads(Path('.local/build-event-severity/version-gate-files.json').read_text())+['internal/mcp/compiler_implementations_test.go','internal/semanticindex/implementations_test.go']
for name in files:
 p=Path(name);before=Path('.local/project-analyzers/before')/name;before.parent.mkdir(parents=True,exist_ok=True);s=before.read_text()
 if p.suffix=='.py':s=s.replace("'19','20']","'19','20','21']").replace("default='20'","default='21'")
 else:
  s=s.replace('"21"','"22"')
  s=s.replace('"19", "20"','"19", "20", "21"')
  s=re.sub(r'\b([\w.]+) (==|!=) "20"',lambda m:'('+m[0]+(' || ' if m[2]=='==' else ' && ')+m[1]+' '+m[2]+' "21")',s)
  if name.endswith('revalidate_test.go'):
   s=s.replace('capture["build_diagnostics"] = buildDiagnosticProofFixture()', 'proof := buildDiagnosticProofFixture()\n\t\t\t\tif version == "21" { proof["policy"] = "native-build-events-v2" }\n\t\t\t\tcapture["build_diagnostics"] = proof')
 p.write_text(s)
Path('.local/project-analyzers/version-files.json').write_text(json.dumps(files,indent=2)+'\n')
