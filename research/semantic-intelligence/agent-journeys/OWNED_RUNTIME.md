# Launcher-owned runtime

`owned_runtime.py` owns one POSIX daemon process group for a complete manual
schedule. It never restarts the daemon or retries an assignment. A separate
guardian is the launcher's direct child and creates a new session/process group.
The daemon inherits that private group. Launcher exit closes the private control
pipe and the guardian stops the group. The daemon receives null stdin and cannot
read or keep open that control pipe.

The launcher retains the guardian unreaped until every group signal and closure
check has finished. This kernel PID anchor reserves the group ID, including after
a guardian crash. Cleanup uses `waitid(WNOWAIT)` and never signals a guessed birth
identity. Another component reaping the private guardian violates ownership and
causes cleanup to refuse group signals. An unrelated process or existing service
cannot be adopted. Only one owner may wait for this module's private guardian.

The guardian handles group TERM while the daemon and descendants stop. Its own
process observations run outside the owned group. A group requiring KILL also
kills the guardian and remains an incomplete lifecycle; the launcher confirms
group quiescence while retaining the anchor, but cannot invent a daemon exit code
or successful guardian acknowledgment after that crash.

```python
from owned_runtime import OwnedRuntime, RuntimeUnavailable

with OwnedRuntime(
    command, new_receipt_directory, readiness_probe,
    startup_timeout=30, health_timeout=3, shutdown_grace=5,
) as runtime:
    for wave in frozen_schedule:
        if runtime.abort_requested:
            break
        runtime.ensure_healthy()
        # Check again before each assignment and periodically while it runs.
        # The launcher retains actual results even after a failed health check.
        run_and_retain_wave(wave, runtime)
    runtime.finish(schedule_complete=all_planned_results_retained)
```

The launcher supplies a read-only boolean readiness callback that verifies the
expected service, snapshot fingerprint and repository roster. A true callback
result and a live owned child are required before dispatch. Each callback runs
in a short-lived POSIX fork process; the owner terminates and joins it at the
deadline. The callback must be safe to fork and must not mutate coordinator
state. Give its network client its own finite timeout as well. The module uses
no background health threads. The owner calls `ensure_healthy()` before new
assignments and periodically while existing work is active.
The private health worker also watches its parent's lifetime and kills itself
if the owner disappears, so a hung callback cannot keep running after launcher
death or retain the guardian's ownership pipe.

An unexpected daemon exit or failed health check makes later dispatch checks
raise `RuntimeUnavailable`. The failure remains latched even if the service
would subsequently recover. `abort_requested`, `failure_reason` and
`daemon_exit_code` expose the observed state. SIGINT and SIGTERM request an
orderly abort without terminating an in-flight wave; the owner stops admitting
work, retains the wave's actual terminal results, and calls `finish(False)`.
The context manager also cleans up on exceptions. Prior signal handlers are
restored only if the owner still has this module's handler installed. Use
`install_signal_handlers=False` when the launcher already handles signals and
calls `request_abort()` itself. Only one owner thread drives this object.

`finish(True)` is an assertion by the launcher that every planned driver result
exists. It performs a final health check and can report `completed:true` only
after healthy readiness, no abort or daemon failure, and acknowledged owned
shutdown. A partial schedule, context exit, startup failure, health failure,
unexpected exit or signal can never produce successful completion. Calling
`finish()` again returns the original terminal receipt; starting again fails.
After abrupt owner death, existing receipts stay incomplete: the guardian can
clean up, but cannot invent a completed schedule or write a completion receipt
on behalf of a dead launcher.

The receipt directory must be new. Events are exclusive-create mode-0600 JSON
files, numbered and linked by exact SHA256 hashes. Files and their directory
are fsynced. Events contain lifecycle categories, process IDs, boolean states,
timestamps and exit codes. They omit command arguments, environment, working
directories, endpoint URLs, exception messages and health callback output.
Unknown free-text reason categories are rejected. An optional new `output_path`
stores daemon output separately with mode 0600; the caller controls that log's
privacy and destination. Freeze the command, binary, index and this module's
hash separately in the launcher's packet. A lifecycle receipt authenticates
neither service provenance nor actual schedule coverage by itself.
If receipt storage fails, physical cleanup still runs and no successful terminal
receipt is fabricated. The launcher must treat that exception or missing closure
as incomplete evidence.

Run the neutral lifecycle tests with:

```sh
python3 -B -m unittest discover -s research/semantic-intelligence/agent-journeys \
  -p test_owned_runtime.py -v
```
