#!/usr/bin/env python3
"""Run a disposable graph command with recorded time, memory, and log budgets.

Memory monitoring covers the direct child (the default, pure-Go graph builder),
not a tree of optional compiler/LSP subprocesses. macOS measures physical
footprint; Linux measures RSS. These are sampled limits, not OS allocation caps.
Logs are unbuffered so progress is visible while the command runs.
"""

import argparse
import ctypes
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import threading
import time


class MacUsage(ctypes.Structure):
    # rusage_info_v2 from the macOS SDK's sys/resource.h.
    _fields_ = [("uuid", ctypes.c_ubyte * 16)] + [
        (name, ctypes.c_uint64) for name in (
            "user_time", "system_time", "pkg_idle_wkups", "interrupt_wkups",
            "pageins", "wired_size", "resident_size", "phys_footprint",
            "proc_start_abstime", "proc_exit_abstime", "child_user_time",
            "child_system_time", "child_pkg_idle_wkups", "child_interrupt_wkups",
            "child_pageins", "child_elapsed_abstime", "diskio_bytesread",
            "diskio_byteswritten",
        )
    ]


def memory_reader():
    if sys.platform == "darwin":
        lib = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
        lib.proc_pid_rusage.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_void_p]
        lib.proc_pid_rusage.restype = ctypes.c_int

        def read(pid):
            usage = MacUsage()
            if lib.proc_pid_rusage(pid, 2, ctypes.byref(usage)) != 0:
                return None
            return usage.phys_footprint

        return "macos_physical_footprint", read
    if sys.platform.startswith("linux"):
        def read(pid):
            try:
                for line in Path(f"/proc/{pid}/status").read_text().splitlines():
                    if line.startswith("VmRSS:"):
                        return int(line.split()[1]) * 1024
            except (OSError, ValueError):
                pass
            return None

        return "linux_rss", read
    raise RuntimeError("memory monitoring is supported only on macOS and Linux")


def positive(value):
    parsed = int(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return parsed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--name", required=True)
    parser.add_argument("--cwd", type=Path, default=Path.cwd())
    parser.add_argument("--timeout-seconds", type=positive, default=600)
    parser.add_argument("--max-memory-mib", type=positive, default=8192)
    parser.add_argument("--max-log-bytes", type=positive, default=16 << 20)
    parser.add_argument("--go-workers", type=positive, default=8)
    parser.add_argument("--go-memory-limit", default="6GiB")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command or not re.fullmatch(r"[A-Za-z0-9_-]+", args.name):
        parser.error("provide a command and an alphanumeric, dash/underscore run name")
    base = args.output_dir.resolve()
    base.mkdir(parents=True, exist_ok=True)
    outputs = [base / (args.name + suffix) for suffix in (".json", ".stdout", ".stderr")]
    if any(path.exists() for path in outputs):
        parser.error("refusing to overwrite existing run outputs")
    metric, memory = memory_reader()
    if not memory(os.getpid()):
        parser.error("memory measurement unavailable; refusing an unmonitored run")
    for directory in ("home", "tmp"):
        (base / directory).mkdir(exist_ok=True)
    env = {"PATH": "/usr/bin:/bin", "HOME": str(base / "home"),
           "TMPDIR": str(base / "tmp"), "GOMAXPROCS": str(args.go_workers),
           "GOMEMLIMIT": args.go_memory_limit}
    started_utc = datetime.datetime.now(datetime.timezone.utc).isoformat()
    started = time.monotonic()
    process = subprocess.Popen(command, cwd=args.cwd, env=env,
                               stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, start_new_session=True)
    reasons = []
    reason_lock = threading.Lock()

    def stop(reason):
        with reason_lock:
            if reason not in reasons:
                reasons.append(reason)
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    def drain(stream, path):
        total = 0
        try:
            with path.open("xb", buffering=0) as log:
                while chunk := stream.read1(65536):
                    remaining = max(0, args.max_log_bytes - total)
                    log.write(chunk[:remaining])
                    total += len(chunk)
                    if total > args.max_log_bytes:
                        stop("log budget exceeded: " + path.suffix)
        except Exception as exc:
            stop("capture failed: " + repr(exc))
        finally:
            stream.close()

    threads = [threading.Thread(target=drain, args=(stream, path), daemon=True)
               for stream, path in zip((process.stdout, process.stderr), outputs[1:])]
    for thread in threads:
        thread.start()
    peak = 0
    samples = 0
    unavailable = 0
    try:
        while process.poll() is None:
            if time.monotonic() - started >= args.timeout_seconds:
                stop("deadline exceeded")
                break
            value = memory(process.pid)
            if value is None:
                unavailable += 1
                if unavailable >= 3 and process.poll() is None:
                    stop("memory measurement unavailable")
                    break
            else:
                unavailable = 0
                samples += 1
                peak = max(peak, value)
                if value > args.max_memory_mib * (1 << 20):
                    stop("sampled memory budget exceeded")
                    break
            time.sleep(0.25)
    except KeyboardInterrupt:
        stop("operator interrupted runner")
    code = process.wait(timeout=10)
    for thread in threads:
        thread.join(timeout=5)
    if any(thread.is_alive() for thread in threads):
        stop("capture drain deadline exceeded")
        for thread in threads:
            thread.join(timeout=5)
    result = dict(command=command, cwd=str(args.cwd.resolve()), environment=env,
                  started_utc=started_utc, elapsed_seconds=time.monotonic() - started,
                  exit_code=code, stop_reasons=reasons,
                  timeout_seconds=args.timeout_seconds, max_log_bytes=args.max_log_bytes,
                  memory_metric=metric, max_memory_bytes=args.max_memory_mib * (1 << 20),
                  sampled_peak_memory_bytes=peak, memory_samples=samples,
                  sample_interval_seconds=0.25, memory_scope="direct child only",
                  runner_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest())
    outputs[0].write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result), flush=True)
    return 0 if code == 0 and not reasons else 1


if __name__ == "__main__":
    raise SystemExit(main())
