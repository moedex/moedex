# Launcher-owned fixed waves

`owned_waves.py` drives one frozen local schedule using an already ready
[`OwnedRuntime`](OWNED_RUNTIME.md) and one startup-inclusive
[`ExecutionGuard`](EXECUTION_GUARD.md). It contains no provider configuration,
source access, scoring, retry, resume, reset, or outcome-based schedule selection.
A nonzero runner exit is retained as observed and later planned waves continue.

```python
guard = ExecutionGuard(archive_root, max_wall_seconds, max_archive_bytes,
                       min_free_bytes)
runtime = OwnedRuntime(daemon_command, archive_root / 'runtime', readiness_probe,
                       health_timeout=3, install_signal_handlers=False)
try:
    runtime.start()
    execution = OwnedWaves(
        archive_root, frozen_waves, command_for,
        runtime=runtime, guard=guard, env=runner_env, cwd=runner_cwd,
        poll_interval_seconds=.25, health_interval_seconds=5,
        termination_grace_seconds=5, install_signal_handlers=True,
    ).run()
    closure = runtime.finish(schedule_complete=execution['execution_complete'])
    completed = execution['execution_complete'] and closure['completed']
finally:
    runtime.finish(False)
```

Create the guard before daemon startup; never replace it between waves. Runtime
startup must already have verified the expected service identity and readiness.
`command_for(name)` is a bounded local callback returning a nonempty argv list;
commands use `subprocess.Popen` without a shell and with `start_new_session=True`.
The caller freezes commands and provenance separately. Only one owner thread may
drive these objects, and no other component may reap the runner children.

`OwnedWaves.run()` is single-use. The archive must be an existing trusted private
directory, with fresh `execution` and `captures` roots and no previous assignment
directories under the supplied identities. Duplicate, reserved, or unconfined
identities are rejected. The new roots have mode 0700. Exclusive-create mode-0600
receipts are canonical JSON, hashed and fsynced with their parent directory.
Runner stdout and stderr are retained separately with mode 0600.

The initial `execution/waves.created.json` retains the complete fixed roster.
An admitted slot gets `NAME.intent.json` before its bounded command callback,
then `NAME.stdout` and `NAME.stderr`. Intent fields preserve the legacy shape:
`assignment`, one-based `wave`, and `launched_unix`. A spawned runner's result
contains `assignment`, its actual `exit_code`, and `finished_unix`. An attempted
launch failure contains only `assignment`, null `exit_code`, and integer or null
`launch_failed_errno`. Neither safe receipts nor exceptions contain argv,
environment values, working directories, URLs, or callback error messages.
Captured runner output remains caller-controlled private evidence.

The driver checks the guard before admission, on every poll, and after a bounded
runtime health probe. It forces health before each wave and otherwise probes at
the configured interval, including while admitted runners finish after a driver
abort. `request_abort()`, SIGINT, and SIGTERM stop further admission, including
remaining slots of a partial wave. Already admitted slots finish gracefully.
The wave handler leaves runtime health checking enabled and restores the prior
handlers afterward. An externally aborted runtime refuses further health probes;
its failure property is still checked for unexpected daemon/guardian exit. Use
the wave handler during execution and retain the runtime's handler only during
startup if the caller requires startup signal handling.

A guard, health, launch, orchestration, or retention failure stops admission and
cancels active owned runner groups. Normal leader exits also trigger group
cleanup before the next wave: `waitid(WNOWAIT)` detects a terminal leader without
reaping it, retaining its PID against unrelated group reuse. On macOS Python
without `os.waitid`, the stdlib `ctypes` fallback calls libc's POSIX `waitid`.
The driver sends TERM, permits bounded grace, sends KILL, and observes only
process-group IDs and kernel states through `/bin/ps`. The leader is reaped only
after this bounded group check. Its original exit code is preserved even when
its descendants require KILL. Signal permission denial alone never proves
closure; confirmed nonrunning groups are required. Cleanup has separate real
monotonic deadlines for TERM grace, group observation, and final waits; injected
orchestration clocks and sleep callbacks cannot disable physical cleanup.
Normal cleanup continues bounded health/guard polling. Hard cleanup performs no
new admissions. A failed cleanup remains incomplete evidence.

`execution/waves.result.json` and the returned dictionary contain:

| Field | Meaning |
| --- | --- |
| `schema` | `owned-wave-execution-v1`. |
| `execution_complete` | Every planned slot was spawned, its actual result retained, its owned group stopped, and no abort/failure was observed. Nonzero exits are allowed. |
| `runtime_closure_verified` | Always false; the caller separately retains and checks `runtime.finish(...)`. |
| `abort_requested` | Graceful abort or hard stop was observed. |
| `stop_reason` | Null, first graceful reason, or first hard reason if a graceful abort escalated. |
| `stop_reasons` | All fixed observed reason categories in first-observation order. |
| `planned_slots` | Complete fixed roster denominator. |
| `launched_slots` | Number of successfully spawned runner processes. |
| `terminal_results` | Number of retained per-slot result references, including errno-only launch-failure receipts. |
| `actual_results` | One row per successfully spawned process, in planned order: `assignment`, `wave`, `pid`, `status`, `exit_code`, `result`, and observed `owned_group_stopped` when available. Unretained or unreaped outcomes remain explicit. |
| `slots` | Full terminal roster, including every unlaunched slot, with assignment, wave, status, intent/result references, exit code, and PID/group-closure fields when applicable. |
| `limitations` | Fixed reminder that schedule coverage and runtime closure do not establish capture integrity, identity, classification, or semantic correctness. |

References contain archive-relative `path` and exact `sha256`. Unlaunched slots
have null references and exit code; the driver does not invent intent or result
files for them. `terminal_result_unretained` preserves an observed actual exit
code while its result reference stays null. `launched_unreaped` preserves null
exit code and result when no actual wait result was obtained. A returned receipt
is not automatically successful; check `execution_complete` and runtime closure.
If terminal retention itself is impossible, `WaveRunFailed` reports only a fixed
safe reason. Existing archives are never resumed or overwritten.

This is owned process-group orchestration, not OS isolation. Descendants that
deliberately leave the owned group need a stronger containment boundary. A killed
launcher cannot retain future results or write a terminal roster; old receipts
stay incomplete. The execution guard remains a polling stop, not a hard storage,
token, or billing quota. Callbacks must be bounded and the archive must not be
concurrently replaced by an untrusted writer.

Run the neutral subprocess and composition tests with:

```sh
python3 -B -m unittest discover -s research/semantic-intelligence/agent-journeys \
  -p test_owned_waves.py -v
```

The tests require host permission to observe process IDs/groups/states through
`/bin/ps`; a sandbox denial fails closure conservatively. They use synthetic
processes, temporary archives, and no providers, corpora, or scored assignments.

An optional bounded `terminal_check(assignment)` runs after bounded local group
cleanup and any actual leader result, before later waves. It must return exactly true
to pass; false/exception latches `runner_postcheck_failed` without changing the
actual exit code. Use this to verify daemon-managed resources such as Docker
containers. It also runs during cancellation, for an unreaped leader, and after a result-write exception;
physical cleanup must precede its receipt writes. Local process groups alone do
not prove container closure. A callback must provide its own finite timeouts.
Passing the callback never repairs unknown group closure or a missing exit.
