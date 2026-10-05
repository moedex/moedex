#!/usr/bin/env python3
"""Record exactly displayed browser bytes; enforce the full assignment deadline."""
import argparse,fcntl,hashlib,json,subprocess,sys
from pathlib import Path
from journey_clock import monotonic
BROWSER=Path(__file__).with_name('browse.py')
def main():
 p=argparse.ArgumentParser();p.add_argument('--run-dir',type=Path,required=True);a,rest=p.parse_known_args()
 run=a.run_dir.resolve();views=run/'views';views.mkdir(exist_ok=True)
 with (run/'view.lock').open('a') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX)
  ordinal=len(list(views.glob('*.stdout')))+1;stem=f'{ordinal:03}'
  control=json.loads((run/'assignment.json').read_text());remaining=control['deadline_monotonic']-monotonic()
  started=monotonic();cmd=[sys.executable,'-B',str(BROWSER),'--run-dir',str(run),*rest]
  if remaining<=0:
   stdout=b'';stderr=b'{"view_stopped":"full assignment deadline exhausted"}\n';code=2
  elif any(x.startswith('--') and '--max-bytes'.startswith(x.split('=', 1)[0]) for x in rest):
   stdout=b'';stderr=b'{"view_stopped":"display cap is frozen at 8192 bytes"}\n';code=2
  else:
   try:
    result=subprocess.run(cmd,capture_output=True,timeout=remaining);stdout,stderr,code=result.stdout,result.stderr,result.returncode
   except subprocess.TimeoutExpired as e:
    stdout=e.stdout or b'';stderr=(e.stderr or b'')+b'\n{"view_stopped":"full assignment deadline exhausted during call"}\n';code=2
  (views/(stem+'.stdout')).write_bytes(stdout);(views/(stem+'.stderr')).write_bytes(stderr)
  record={'ordinal':ordinal,'argv':rest,'started_monotonic':started,'finished_monotonic':monotonic(),'returncode':code,'stdout_bytes':len(stdout),'stdout_sha256':hashlib.sha256(stdout).hexdigest(),'stderr_bytes':len(stderr),'stderr_sha256':hashlib.sha256(stderr).hexdigest()}
  (views/(stem+'.json')).write_text(json.dumps(record,indent=2)+'\n')
  sys.stdout.buffer.write(stdout);sys.stderr.buffer.write(stderr)
  return code
if __name__=='__main__':sys.exit(main())
