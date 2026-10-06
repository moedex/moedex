"""Neutral fixed-wave runner ownership and actual-result retention.

No providers, source, scoring, retries or schedule selection live here. The
caller supplies a frozen roster, local commands, a ready owned runtime and one
startup-inclusive execution guard. Runtime closure remains the caller's duty.
"""
import ctypes
import errno
import hashlib
import json
import math
import os
from pathlib import Path
import re
import signal
import subprocess
import time

from execution_guard import ARCHIVE_TREES, RunLimitExceeded, REASONS as GUARD_REASONS


NAME = re.compile(r'^[A-Za-z0-9][A-Za-z0-9_.-]{0,199}$')
REASONS = GUARD_REASONS | {'signal', 'operator_abort', 'runtime_unavailable',
                          'runner_launch_failed', 'runner_cancel_failed',
                          'retention_failed', 'orchestration_failed', 'runner_postcheck_failed'}


class WaveRunFailed(RuntimeError):
    """Fixed safe failure when orchestration cannot retain a complete receipt."""
    def __init__(self, reason):
        if reason not in REASONS:
            raise ValueError('unknown wave failure reason')
        self.reason = reason
        super().__init__(reason)


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'),
                      ensure_ascii=False, allow_nan=False).encode() + b'\n'


def _terminal_without_reaping(pid):
    """Check our child using waitid while retaining its PID and owned PGID.

    macOS Python exposes the wait flags but not os.waitid. Its libc implements
    the same POSIX operation; only the leading siginfo_t signo is inspected.
    The generously sized, long-aligned buffer avoids platform layout parsing.
    """
    options = os.WEXITED | os.WNOHANG | os.WNOWAIT
    if hasattr(os, 'waitid'):
        value = os.waitid(os.P_PID, pid, options)
        return value is not None and value.si_pid == pid
    libc = ctypes.CDLL(None, use_errno=True)
    waitid = libc.waitid
    waitid.argtypes = [ctypes.c_int, ctypes.c_uint, ctypes.c_void_p, ctypes.c_int]
    waitid.restype = ctypes.c_int
    buffer = (ctypes.c_long * 64)()
    # P_PID is 1 on the supported Darwin/Linux POSIX hosts.
    while waitid(1, pid, ctypes.byref(buffer), options) != 0:
        number = ctypes.get_errno()
        if number == errno.EINTR:
            continue
        raise OSError(number, 'owned child terminal check failed')
    signo = ctypes.cast(buffer, ctypes.POINTER(ctypes.c_int))[0]
    if signo not in (0, signal.SIGCHLD):
        raise OSError('owned child terminal check failed')
    return signo == signal.SIGCHLD


def _running_groups(timeout):
    """Observe group IDs and kernel states, never commands or environments."""
    value = subprocess.run(['/bin/ps', '-ax', '-o', 'pgid=', '-o', 'stat='],
                           stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                           env={'PATH': '/bin:/usr/bin', 'LC_ALL': 'C'}, timeout=timeout)
    if value.returncode != 0:
        raise OSError('owned group check failed')
    states = [line.split() for line in value.stdout.decode().splitlines()]
    return {int(row[0]) for row in states
            if len(row) == 2 and row[0].isdigit() and not row[1].startswith('Z')}


class OwnedWaves:
    """One owner thread drives one fixed schedule without resuming or retrying.

    command_for(name) returns argv, never a shell string. The ready runtime
    implements ensure_healthy(), failure_reason and abort_requested. The guard
    implements check(). Both callbacks must be bounded; runtime health deadlines
    belong to the runtime. Runner subprocesses get fresh sessions/process groups.
    """
    def __init__(self, archive_root, waves, command_for, *, runtime, guard,
                 env=None, cwd=None, poll_interval_seconds=.25,
                 health_interval_seconds=5, termination_grace_seconds=5,
                 install_signal_handlers=True, clock=time.monotonic,
                 wall_clock=time.time, sleep=time.sleep, terminal_check=None):
        if os.name != 'posix':
            raise ValueError('owned waves requires POSIX process ownership')
        if not isinstance(waves, (list, tuple)) or not waves:
            raise ValueError('nonempty fixed wave roster required')
        names = []
        for wave in waves:
            if not isinstance(wave, (list, tuple)) or not wave:
                raise ValueError('nonempty fixed waves required')
            for name in wave:
                if (type(name) is not str or not NAME.fullmatch(name) or
                        name in ARCHIVE_TREES):
                    raise ValueError('confined assignment identity required')
                names.append(name)
        if len(set(names)) != len(names):
            raise ValueError('unique assignment identities required')
        for value in (poll_interval_seconds, health_interval_seconds, termination_grace_seconds):
            if type(value) not in (int, float) or not math.isfinite(value) or value <= 0:
                raise ValueError('positive finite wave intervals required')
        if not all(callable(callback) for callback in (command_for, clock, wall_clock, sleep)):
            raise ValueError('local wave callbacks required')
        if not callable(getattr(runtime, 'ensure_healthy', None)) or not callable(getattr(guard, 'check', None)):
            raise ValueError('runtime and execution guard interfaces required')
        if type(install_signal_handlers) is not bool:
            raise ValueError('explicit signal handler option required')
        if terminal_check is not None and not callable(terminal_check):
            raise ValueError('terminal checker must be callable')
        self.terminal_check=terminal_check
        self.root = Path(os.path.abspath(os.fspath(archive_root)))
        self.waves = tuple(tuple(wave) for wave in waves)
        self.command_for, self.runtime, self.guard = command_for, runtime, guard
        self.env, self.cwd = env, cwd
        self.poll_interval = poll_interval_seconds
        self.health_interval, self.grace = health_interval_seconds, termination_grace_seconds
        self.install_signals = install_signal_handlers
        self.clock, self.wall_clock, self.sleep = clock, wall_clock, sleep
        self._ran = self._graceful_abort = self._hard_stop = False
        self._stop_reason = None
        self._reasons = []
        self._active = []
        self._last_health = None
        self._handlers = {}
        self._handler = self._signal_handler
        self._rows = [{'assignment': name, 'wave': index + 1, 'status': 'unlaunched',
                       'intent': None, 'result': None, 'exit_code': None}
                      for index, wave in enumerate(self.waves) for name in wave]
        self._by_name = {row['assignment']: row for row in self._rows}

    @property
    def abort_requested(self):
        return self._graceful_abort or self._hard_stop

    @property
    def failure_reason(self):
        return self._stop_reason

    def _stop(self, reason, *, hard):
        if reason not in REASONS:
            reason = 'orchestration_failed'
        if reason not in self._reasons:
            self._reasons.append(reason)
        if hard:
            if not self._hard_stop:
                self._stop_reason = reason
            self._hard_stop = True
        elif not self._hard_stop:
            self._graceful_abort = True
            self._stop_reason = self._stop_reason or reason

    def request_abort(self):
        """Refuse further admissions; finish already admitted work gracefully."""
        self._stop('operator_abort', hard=False)

    def _signal_handler(self, signum, frame):
        # No filesystem I/O, process killing or runtime callback in a handler.
        self._stop('signal', hard=False)

    def _write(self, relative, value):
        raw = _canonical(value)
        path = self.root / relative
        fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        directory_fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
        return {'path': relative, 'sha256': hashlib.sha256(raw).hexdigest()}

    def _conditions(self, *, force_health=False):
        try:
            self.guard.check()
        except RunLimitExceeded as error:
            self._stop(error.reason, hard=True)
            return
        except Exception:
            self._stop('orchestration_failed', hard=True)
            return
        try:
            if self.runtime.failure_reason is not None:
                self._stop('runtime_unavailable', hard=True)
                return
            if self.runtime.abort_requested:
                self.request_abort()
                # An externally aborted runtime refuses ensure_healthy. Its
                # failure property still detects daemon death on later polls.
                return
            now = self.clock()
            if force_health or self._last_health is None or now - self._last_health >= self.health_interval:
                self.runtime.ensure_healthy()
                self._last_health = self.clock()
                self.guard.check()  # Include time spent in the bounded probe.
        except RunLimitExceeded as error:
            self._stop(error.reason, hard=True)
        except Exception:
            self._stop('runtime_unavailable', hard=True)

    def _record_result(self, child, code):
        row = child['row']
        row['exit_code'] = code
        row['status'] = 'terminal_result_unretained'
        try:
            row['result'] = self._write(
                f'execution/{row["assignment"]}.result.json',
                {'assignment': row['assignment'], 'exit_code': code,
                 'finished_unix': self.wall_clock()})
            row['status'] = 'launched_terminal'
        finally:
            child['stdout'].close()
            child['stderr'].close()
            self._postcheck(row)

    def _postcheck(self,row):
        if self.terminal_check is not None and 'runner_postcheck_passed' not in row:
            try:row['runner_postcheck_passed']=self.terminal_check(row['assignment']) is True
            except Exception:row['runner_postcheck_passed']=False
            if not row['runner_postcheck_passed']:self._stop('runner_postcheck_failed',hard=True)

    def _launch(self, name, wave):
        row = self._by_name[name]
        row['intent'] = self._write(
            f'execution/{name}.intent.json',
            {'assignment': name, 'wave': wave, 'launched_unix': self.wall_clock()})
        row['status'] = 'launch_intended'
        stdout = stderr = None
        try:
            command = self.command_for(name)
            if (not isinstance(command, (list, tuple)) or not command or
                    not all(type(arg) is str and arg and '\x00' not in arg for arg in command)):
                raise ValueError('local argv required')
            stdout_fd = os.open(self.root/f'execution/{name}.stdout',
                                os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
            stdout = os.fdopen(stdout_fd, 'wb')
            stderr_fd = os.open(self.root/f'execution/{name}.stderr',
                                os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
            stderr = os.fdopen(stderr_fd, 'wb')
            process = subprocess.Popen(list(command), env=self.env, cwd=self.cwd,
                                       stdout=stdout, stderr=stderr, start_new_session=True)
        except Exception as error:
            if stdout is not None:
                stdout.close()
            if stderr is not None:
                stderr.close()
            errno = error.errno if isinstance(error, OSError) and type(error.errno) is int else None
            row['result'] = self._write(
                f'execution/{name}.result.json',
                {'assignment': name, 'exit_code': None, 'launch_failed_errno': errno})
            row['status'] = 'launch_failed'
            self._stop('runner_launch_failed', hard=True)
            return
        row['status'] = 'launched'
        row['pid'] = process.pid
        self._active.append({'row': row, 'process': process, 'stdout': stdout, 'stderr': stderr})

    def _reap_finished(self):
        remaining, finished = [], []
        for child in self._active:
            if _terminal_without_reaping(child['process'].pid):
                finished.append(child)
            else:
                remaining.append(child)
        # Even a successfully exited leader can leave owned descendants alive.
        # Stop those groups before reaping any leader or advancing the schedule.
        self._close_children(finished, monitor=True)
        self._active = remaining

    def _signal_owned_group(self, child, signum):
        pid = child['process'].pid
        try:
            # ECHILD refuses signaling if another component violated exclusive
            # wait ownership; this check never releases the leader's identity.
            terminal = _terminal_without_reaping(pid)
            # Only this module's new session/group is eligible. During hard
            # cleanup the direct leader is not reaped until TERM/grace/KILL has
            # finished, retaining its PID against unrelated group reuse.
            # Darwin can hide getpgid for a zombie even while waitid confirms
            # exclusive unreaped ownership. That retained PID still protects
            # the originally created group ID against reuse.
            if not terminal and os.getpgid(pid) != pid:
                self._stop('runner_cancel_failed', hard=True)
                os.kill(pid, signum)  # Still our unreaped direct child.
                return
            os.killpg(pid, signum)
        except ProcessLookupError:
            pass
        except PermissionError:
            # Darwin may deny signaling a group containing only zombies. This
            # is neither success nor failure until bounded group observation.
            pass
        except OSError:
            self._stop('runner_cancel_failed', hard=True)

    def _close_children(self, children, *, monitor=False):
        if not children:
            return
        for child in children:
            self._signal_owned_group(child, signal.SIGTERM)
        # Physical cleanup has its own real, finite monotonic grace. Injected
        # orchestration clocks/sleep callbacks cannot delay or prevent KILL.
        deadline = time.monotonic() + self.grace
        try:
            while time.monotonic() < deadline:
                if monitor:
                    self._conditions()
                time.sleep(min(self.poll_interval, max(0, deadline - time.monotonic())))
        finally:
            for child in children:
                self._signal_owned_group(child, signal.SIGKILL)
        # KILL delivery alone is not proof of group quiescence. Keep the leaders
        # unreaped while observing their groups, including orphaned zombies.
        pending = {child['process'].pid for child in children}
        closure_deadline = time.monotonic() + self.grace
        try:
            while pending:
                remaining = closure_deadline - time.monotonic()
                if remaining <= 0:
                    break
                pending &= _running_groups(min(1, remaining))
                if pending:
                    time.sleep(min(self.poll_interval, max(0, closure_deadline - time.monotonic())))
        except Exception:
            self._stop('runner_cancel_failed', hard=True)
        for child in children:
            child['row']['owned_group_stopped'] = child['process'].pid not in pending
        if pending:
            self._stop('runner_cancel_failed', hard=True)
        reap_deadline = time.monotonic() + self.grace
        for child in children:
            try:
                code = child['process'].wait(timeout=max(.001, reap_deadline - time.monotonic()))
                self._record_result(child, code)
            except subprocess.TimeoutExpired:
                child['row']['status'] = 'launched_unreaped'
                self._stop('runner_cancel_failed', hard=True)
                child['stdout'].close()
                child['stderr'].close()
            except Exception:
                self._stop('retention_failed', hard=True)
            finally:
                # Even an unreaped leader requires external-resource cleanup.
                # A successful callback never repairs unknown process closure.
                self._postcheck(child['row'])

    def _cancel_active(self):
        self._close_children(self._active)
        self._active = []

    def _terminal(self):
        complete = (not self.abort_requested and
                    all(row['status'] == 'launched_terminal' for row in self._rows))
        return {'schema': 'owned-wave-execution-v1', 'execution_complete': complete,
                'runtime_closure_verified': False, 'abort_requested': self.abort_requested,
                'stop_reason': self._stop_reason, 'stop_reasons': list(self._reasons),
                'planned_slots': len(self._rows),
                'launched_slots': sum('pid' in row for row in self._rows),
                'terminal_results': sum(row['result'] is not None for row in self._rows),
                'actual_results': [{key: row[key] for key in
                                    ('assignment', 'wave', 'pid', 'status', 'exit_code', 'result',
                                     'owned_group_stopped') if key in row}
                                   for row in self._rows if 'pid' in row],
                'slots': self._rows,
                'limitations': 'Driver schedule coverage only. Runtime closure, capture integrity, '
                              'identity, classification and semantic correctness require separate evidence.'}

    def run(self):
        """Create new execution/capture roots, retain outcomes, return full roster.

        A nonzero runner exit remains an actual outcome and does not select a
        different schedule. On impossible retention, a safe WaveRunFailed may
        be raised; no missing result is filled with an invented exit code.
        """
        if self._ran:
            raise WaveRunFailed('orchestration_failed')
        self._ran = True
        try:
            if self.root.is_symlink() or not self.root.is_dir():
                raise ValueError('existing trusted archive required')
            if any(path.exists() or path.is_symlink()
                   for path in (self.root/'execution', self.root/'captures')):
                raise ValueError('fresh execution roots required')
            if any((self.root/row['assignment']).exists() or
                   (self.root/row['assignment']).is_symlink() for row in self._rows):
                raise ValueError('previous assignment rejected')
        except Exception:
            raise WaveRunFailed('orchestration_failed') from None
        try:
            (self.root/'execution').mkdir(mode=0o700)
            (self.root/'captures').mkdir(mode=0o700)
            self._write('execution/waves.created.json',
                        {'schema': 'owned-wave-roster-v1', 'waves': self.waves,
                         'planned_slots': len(self._rows)})
            if self.install_signals:
                for signum in (signal.SIGINT, signal.SIGTERM):
                    self._handlers[signum] = signal.signal(signum, self._handler)
            for index, wave in enumerate(self.waves):
                self._conditions(force_health=True)
                if self.abort_requested:
                    break
                for name in wave:
                    self._conditions()
                    if self.abort_requested:
                        break
                    self._launch(name, index + 1)
                    if self._hard_stop:
                        break
                while self._active and not self._hard_stop:
                    self._conditions()
                    if self._hard_stop:
                        break
                    self._reap_finished()
                    if self._active and not self._hard_stop:
                        self.sleep(self.poll_interval)
                if self._hard_stop:
                    self._cancel_active()
                if self.abort_requested:
                    break
            if not self._hard_stop:
                self._conditions()  # Final guard/runtime check before coverage claim.
        except BaseException:
            self._stop('orchestration_failed', hard=True)
            self._cancel_active()
        finally:
            for signum, previous in self._handlers.items():
                if signal.getsignal(signum) is self._handler:
                    signal.signal(signum, previous)
            self._handlers.clear()
        value = self._terminal()
        try:
            self._write('execution/waves.result.json', value)
        except Exception:
            raise WaveRunFailed('retention_failed') from None
        return value
