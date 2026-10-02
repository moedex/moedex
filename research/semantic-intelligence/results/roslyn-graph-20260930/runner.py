import json, os, signal, subprocess, sys, threading, time
from pathlib import Path

base = Path('/private/tmp/moedex-roslyn-graph-20260930')
name, cwd, *command = sys.argv[1:]
if (base/(name+'.json')).exists() or (base/(name+'.stdout')).exists():
    raise SystemExit('refusing existing run outputs')
env = {'PATH': str(base/'dotnet')+':/usr/bin:/bin', 'HOME': str(base/'home'),
       'DOTNET_CLI_HOME': str(base/'home'), 'DOTNET_ROOT': str(base/'dotnet'),
       'NUGET_PACKAGES': str(base/'packages'), 'TMPDIR': str(base/'tmp'),
       'DOTNET_NOLOGO': '1', 'DOTNET_CLI_TELEMETRY_OPTOUT': '1',
       'DOTNET_SKIP_FIRST_TIME_EXPERIENCE': '1', 'MSBUILDDISABLENODEREUSE': '1'}
env = {'PATH': '/usr/bin:/bin', 'HOME': str(base/'home'), 'TMPDIR': str(base/'tmp'), 'GOMAXPROCS': '8'}
for directory in ['home','packages','tmp']:
    (base/directory).mkdir(exist_ok=True)
started = time.monotonic()
p = subprocess.Popen(command, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                     stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
overflow = []
def stop():
    try: os.killpg(p.pid, signal.SIGKILL)
    except ProcessLookupError: pass
def drain(stream, suffix):
    total = 0
    with (base/(name+suffix)).open('wb') as log:
        while chunk := stream.read1(65536):
            remaining = (16 << 20) - total
            log.write(chunk[:max(0, remaining)])
            total += len(chunk)
            if total > 16 << 20:
                overflow.append(suffix)
                stop()
                break
    stream.close()
threads = [threading.Thread(target=drain,args=(p.stdout,'.stdout')), threading.Thread(target=drain,args=(p.stderr,'.stderr'))]
for thread in threads: thread.start()
timeout = False
try: code = p.wait(timeout=600)
except subprocess.TimeoutExpired:
    timeout = True
    stop()
    code = p.wait(timeout=10)
for thread in threads: thread.join(timeout=5)
if any(thread.is_alive() for thread in threads):
    stop()
    for thread in threads: thread.join(timeout=5)
manifest = dict(command=command,cwd=cwd,environment=env,timeout_seconds=600,log_cap_bytes=16<<20,
                exit_code=code,timed_out=timeout,log_overflow=overflow,elapsed_seconds=time.monotonic()-started)
(base/(name+'.json')).write_text(json.dumps(manifest,indent=2)+'\n')
print(json.dumps(manifest))
sys.exit(code if 0 <= code < 256 else 1)
