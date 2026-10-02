import json,subprocess,time
from pathlib import Path
r=Path('.local/compact-artifacts').resolve();out=r/'quartz';out.mkdir();plan=json.loads(Path('.local/holdout-quartz/evaluation/launch-plan-02.json').read_text());args=plan['capture_argv'];args[0]=str(r/'moedex');args[args.index('--worker')+1]=str(r.parent/'project-analyzers/final-worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll');args[args.index('--workspace')+1]=str(out/'workspace');args[args.index('--output')+1]=str(out/'project.semantic');args+=['--max-worker-bytes','268435456'];record={'classification':'development compact artifact storage validation, not holdout','argv':args};start=time.monotonic()
with (out/'stdout').open('wb') as o,(out/'stderr').open('wb') as e:
 p=subprocess.run(args,stdout=o,stderr=e,timeout=500)
record.update(returncode=p.returncode,seconds=time.monotonic()-start);(out/'command.json').write_text(json.dumps(record,indent=2)+'\n');print(json.dumps(record))
