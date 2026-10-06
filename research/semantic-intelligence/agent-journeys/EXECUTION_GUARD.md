# Launcher execution guard

`execution_guard.py` supplies read-only wall-time and filesystem checks for one
manual launcher schedule. Construct one `ExecutionGuard` before runtime startup
and reuse it before admitting every slot and periodically while runners are
active. Its deadline includes startup, idle time and all waves. `check()` returns
`None` while open and raises `RunLimitExceeded` on the first observed limit or
measurement failure. That failure stays latched on subsequent calls.

```python
from execution_guard import ExecutionGuard, RunLimitExceeded

guard = ExecutionGuard(
    archive_root,
    max_wall_seconds=24 * 60 * 60,
    max_archive_bytes=32 * (1 << 30),
    min_free_bytes=10 * (1 << 30),
    scan_interval_seconds=5,
)
# The caller checks before runtime startup, before each admission, and in its
# active-runner monitor. On a stop it refuses further slots, terminates only its
# owned runners, allows bounded cleanup grace, and retains the actual captures.
guard.check()
```

The 24-hour deadline and 32 GiB archive threshold stop at equality. The 10 GiB
free-space floor accepts equality and stops below it. Wall time is sampled on
every call and again after a filesystem poll. Filesystem checks run on the first
call and then at the configured interval after the previous successful poll;
zero interval polls on every call. Read-only properties `started_at`, `deadline`,
`archive_bytes`, `free_bytes` and `failure_reason` support safe launcher receipts.
`archive_bytes` is the latest scan total, or its partial total at a size stop.
The properties and fixed reason categories contain no file contents or paths.

The scanner recursively sums regular-file logical sizes under the archive's
`execution/`, `captures/`, `runtime/` and `assignments/` directories. It also
supports top-level assignment directories identified by a regular
`assignment.json` marker. It includes all files in each selected tree, including
capture journals, repeated copies, daemon logs and nested blobs; it does not
reset accounting per wave or assignment. Sparse files count at their logical
size, and hard-linked paths each count once. Deleting retained files could lower
a later poll's total, but can never clear a previously latched failure. The
launcher must preserve captures and must not prune evidence to regain admission.

Only immediate directory entries and candidate marker metadata are inspected
outside the selected trees. Source/dependency directories (`corpus`,
`shared-corpus`, `source`, `sources`, `dependencies`, `vendor`, `node_modules`,
`code`, `runners`, `tools`, `evidence`) and names beginning with `index` are
excluded even if they contain an assignment marker. Other unmarked trees and
root-level loose files are not counted. Place runtime output under `runtime/`,
and execution output under one of the selected trees; this is an explicit
archive scope, not a measurement of the complete workspace or host disk usage.

Traversal uses POSIX directory descriptors, relative opens and `O_NOFOLLOW`.
Symlink entries and special files are skipped; a symlink archive root is
rejected. A directory swapped to a symlink during traversal fails the scan
without following it. A previously observed root replaced with another inode
fails closed. The archive root must already exist, have trusted ancestor
directories, and be controlled by the launcher. The injected `disk_usage`
callback receives the open root directory descriptor, not a path; the default
`shutil.disk_usage` supports it on POSIX and measures that same filesystem.

The fixed safe reasons are `wall_limit`, `archive_limit`, `free_space_floor`,
`archive_scan_failed`, `disk_usage_failed` and `clock_failed`. A failed scan,
invalid free-space result, clock exception, nonfinite clock or regressing clock
sample stops admission. Underlying paths and exception text are never included
in `RunLimitExceeded`; the module logs nothing and reads no file bodies. One
owner thread drives the guard. Inject a monotonic clock and disk-usage callback
for deterministic tests. A new guard starts a new deadline and is not a resume
mechanism for an interrupted schedule.

These safeguards are **best-effort filesystem polling**, not hard quotas,
cumulative token reservations, authenticated billing limits or monetary caps.
Active writers can cross thresholds between polls, during an individual scan,
or while a launcher terminates runners and retains their cleanup output.
Filesystem races and unavailable metadata fail closed, but scans are not atomic
snapshots and this module does not install an independent watchdog process.
Monitor-loop delays and filesystem calls can delay detection. Keep calls
frequent and reserve disk space for stop/cleanup artifacts. Cleanup grace starts
after admission has stopped: it never extends the guard deadline or permits
another slot. The caller implements owned-process termination and result
retention. The guard never launches work, retries, restarts a service, deletes
captures, fabricates terminal outcomes or changes a frozen schedule.

Run the neutral synthetic suite with:

```sh
python3 -B -m unittest discover -s research/semantic-intelligence/agent-journeys \
  -p test_execution_guard.py -v
```
