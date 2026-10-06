"""POSIX launcher-owned daemon lifetime and bounded health checks.

Only the guardian's newly owned process group is stopped. Commands and environments are never
written to lifecycle receipts. The guardian's private control pipe closes when
the launcher dies, including abrupt launcher termination.
"""
import atexit
import ctypes
import errno
import datetime
import hashlib
import json
import math
import multiprocessing
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import threading
import time
import uuid


class RuntimeUnavailable(RuntimeError):
    pass


REASONS = {'operator_abort', 'signal', 'owner_exit', 'context_exit',
           'schedule_incomplete', 'schedule_complete', 'startup_timeout',
           'startup_failed', 'health_failed', 'daemon_exit', 'guardian_exit',
           'guardian_protocol', 'cleanup_failed'}


def _terminal_without_reaping(pid):
    """Inspect our exclusive direct child without releasing its PID/PGID."""
    options = os.WEXITED | os.WNOHANG | os.WNOWAIT
    if hasattr(os, 'waitid'):
        value = os.waitid(os.P_PID, pid, options)
        return value is not None and value.si_pid == pid
    libc = ctypes.CDLL(None, use_errno=True)
    waitid = libc.waitid
    waitid.argtypes = [ctypes.c_int, ctypes.c_uint, ctypes.c_void_p, ctypes.c_int]
    waitid.restype = ctypes.c_int
    buffer = (ctypes.c_long * 64)()
    while waitid(1, pid, ctypes.byref(buffer), options) != 0:
        number = ctypes.get_errno()
        if number == errno.EINTR:
            continue
        raise OSError(number, 'owned child terminal check failed')
    signo = ctypes.cast(buffer, ctypes.POINTER(ctypes.c_int))[0]
    if signo not in (0, signal.SIGCHLD):
        raise OSError('owned child terminal check failed')
    return signo == signal.SIGCHLD


def _group_running(pid, exclude_pid=None):
    try:
        result = subprocess.run(['/bin/ps', '-ax', '-o', 'pid=', '-o', 'pgid=', '-o', 'stat='],
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                env={'PATH': '/bin:/usr/bin', 'LC_ALL': 'C'}, timeout=1,
                                start_new_session=True)
    except (OSError, subprocess.TimeoutExpired):
        return None
    if result.returncode != 0:
        return None
    states = [line.split() for line in result.stdout.decode().splitlines()]
    return any(len(row) == 3 and row[1] == str(pid) and row[0] != str(exclude_pid)
               and not row[2].startswith('Z') for row in states)


def _stop_owned_group(pid, grace):
    """Signal only the group reserved by our still-unreaped direct child.

    ECHILD refuses signaling if anyone violated exclusive wait ownership. No
    birth token or identity-to-signal query can release the kernel PID anchor.
    """
    try:
        terminal = _terminal_without_reaping(pid)
        if not terminal and os.getpgid(pid) != pid:
            return False
    except OSError:
        return False
    try:
        os.killpg(pid, signal.SIGTERM)
    except ProcessLookupError:
        return True
    except PermissionError:
        return _group_running(pid) is False
    deadline = time.monotonic()+grace
    while time.monotonic() < deadline:
        running = _group_running(pid)
        if running is False:
            return True
        if running is None:
            break
        time.sleep(.01)
    # Recheck exclusive unreaped ownership before another signal. Another
    # component reaping this private child invalidates cleanup permission.
    try:
        _terminal_without_reaping(pid)
        os.killpg(pid, signal.SIGKILL)
    except ProcessLookupError:
        return True
    except PermissionError:
        return _group_running(pid) is False
    except OSError:
        return False
    deadline = time.monotonic()+1
    while time.monotonic() < deadline:
        running = _group_running(pid)
        if running is False:
            return True
        if running is None:
            return False
        time.sleep(.01)
    return False


def _health_worker(check, sender, control_fd, owner_pid):
    # A health child must not keep the launcher's guardian ownership pipe open.
    if control_fd is not None:
        os.close(control_fd)
    signal.signal(signal.SIGTERM, signal.SIG_DFL)
    signal.signal(signal.SIGINT, signal.SIG_DFL)
    with open(os.devnull, 'wb') as sink:
        os.dup2(sink.fileno(), 1)
        os.dup2(sink.fileno(), 2)
    def owner_lifetime():
        while os.getppid() == owner_pid:
            time.sleep(.05)
        os.kill(os.getpid(), signal.SIGKILL)
    # The launcher has no health threads. This private worker's watchdog prevents
    # a hung callback surviving abrupt owner death; it can kill only itself.
    threading.Thread(target=owner_lifetime, daemon=True).start()
    try:
        sender.send(check() is True)
    except BaseException:
        sender.send(False)
    finally:
        sender.close()


class OwnedRuntime:
    """One owner thread, one daemon, no restart and no assignment retries.

    Call ensure_healthy before dispatch and periodically while work is active.
    The injected health callback runs in a bounded, joined POSIX fork child.
    It must perform only a read-only readiness probe and be safe to fork.
    Signals request an orderly abort; the owner finishes its current work and
    calls finish(False). EOF independently cleans up after owner death.
    """
    def __init__(self, command, receipts_dir, health_check, *, startup_timeout=30,
                 health_timeout=3, health_interval=.25, shutdown_grace=5,
                 cwd=None, env=None, output_path=None, install_signal_handlers=True):
        if os.name != 'posix':
            raise ValueError('owned runtime requires POSIX process ownership')
        if not isinstance(command, (list, tuple)) or not command or not all(
                type(item) is str and item and '\x00' not in item for item in command):
            raise ValueError('nonempty command arguments required')
        if not callable(health_check):
            raise ValueError('health callback required')
        for value in (startup_timeout, health_timeout, health_interval, shutdown_grace):
            if type(value) not in (int, float) or not math.isfinite(value) or value <= 0:
                raise ValueError('positive finite lifecycle deadlines required')
        self.command, self.cwd, self.env = list(command), os.fspath(cwd) if cwd is not None else None, env
        self.check = health_check
        self.startup_timeout, self.health_timeout = startup_timeout, health_timeout
        self.health_interval, self.shutdown_grace = health_interval, shutdown_grace
        self.output_path = output_path
        self.install_signals = install_signal_handlers
        self.directory = Path(receipts_dir)
        self.directory.mkdir(mode=0o700)  # A new directory; never reuse a receipt set.
        self.session = uuid.uuid4().hex
        self._sequence, self._previous = 0, None
        self._guardian = None
        self._buffer = b''
        self._daemon_pid = self._exit_code = None
        self._fallback_cleanup = None
        self._guardian_eof = False
        self._probe_serial = self._probe_ack = 0
        self._failure = None
        self._abort = False
        self._pending_signal = None
        self._signal_recorded = False
        self._ready = self._stopping = self._stopped = self._finished = False
        self._group_closed = False
        self._terminal = None
        self._old_signals = {}
        self._handler = self._signal_handler
        self._atexit_handler = self._owner_exit
        self._write('created')

    def _write(self, event, **fields):
        self._sequence += 1
        value = {'schema': 'owned-runtime-lifecycle-v1', 'session': self.session,
                 'sequence': self._sequence, 'event': event, 'owner_pid': os.getpid(),
                 'observed_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                 'previous_sha256': self._previous, **fields}
        raw = json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode() + b'\n'
        path = self.directory / f'{self._sequence:06d}.json'
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        directory_fd = os.open(self.directory, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
        self._previous = hashlib.sha256(raw).hexdigest()
        return value

    def _signal_handler(self, signum, frame):
        # Do not take locks, write files or terminate an in-flight wave here.
        self._pending_signal = signum
        self._abort = True

    def _refresh(self):
        if self._finished:
            return
        if self._pending_signal is not None and not self._signal_recorded:
            self._signal_recorded = True
            self._write('abort_requested', reason='signal', signal=self._pending_signal)
        if self._guardian is None:
            return
        fd = self._guardian.stdout.fileno()
        while select.select([fd], [], [], 0)[0]:
            chunk = os.read(fd, 4096)
            if not chunk:
                self._guardian_eof = True
                break
            self._buffer += chunk
            while b'\n' in self._buffer:
                line, self._buffer = self._buffer.split(b'\n', 1)
                try:
                    message = json.loads(line)
                    event = message['event']
                    if (event == 'started' and set(message) == {'event', 'pid', 'group'} and
                            type(message['pid']) is int and message['pid'] > 0 and
                            message['group'] == self._guardian.pid):
                        self._daemon_pid = message['pid']
                        self._write('daemon_started', daemon_pid=self._daemon_pid,
                                    owned_group=self._guardian.pid)
                    elif event in ('exited', 'stopped', 'cleaned') and set(message) == {'event', 'returncode'} and type(message['returncode']) is int:
                        self._exit_code = message['returncode']
                        self._stopped = self._stopped or event == 'stopped'
                        self._group_closed = self._group_closed or event in ('stopped', 'cleaned')
                        self._write('daemon_' + event, returncode=self._exit_code)
                        if event == 'exited':
                            self._fail('daemon_exit')
                    elif event == 'startup_failed' and set(message) == {'event'}:
                        self._fail('startup_failed')
                    elif event == 'cleanup_failed' and set(message) == {'event'}:
                        self._fail('cleanup_failed')
                    elif event == 'alive' and set(message) == {'event', 'nonce'} and type(message['nonce']) is int:
                        self._probe_ack = message['nonce']
                    else:
                        self._fail('guardian_protocol')
                except (ValueError, TypeError, KeyError):
                    self._fail('guardian_protocol')
        if self._guardian_eof and not self._group_closed and not self._failure:
            self._fail('guardian_exit')

    def _fail(self, reason):
        if self._failure is None:
            self._failure = reason
            self._abort = True
            self._write('unavailable', reason=reason)

    @property
    def abort_requested(self):
        self._refresh()
        return self._abort

    @property
    def failure_reason(self):
        self._refresh()
        return self._failure

    @property
    def daemon_exit_code(self):
        self._refresh()
        return self._exit_code

    def request_abort(self, reason='operator_abort'):
        if reason not in REASONS:
            raise ValueError('known lifecycle reason required')
        if not self._abort:
            self._abort = True
            self._write('abort_requested', reason=reason)

    def _health(self, timeout):
        context = multiprocessing.get_context('fork')
        receiver, sender = context.Pipe(duplex=False)
        control_fd = self._guardian.stdin.fileno() if self._guardian and not self._guardian.stdin.closed else None
        child = context.Process(target=_health_worker, args=(self.check, sender, control_fd, os.getpid()))
        try:
            child.start()
            sender.close()
            if receiver.poll(timeout):
                try:
                    result = receiver.recv() is True
                except EOFError:
                    result = False
            else:
                result = False
            child.join(timeout=.1)
            return result
        finally:
            if child.pid is not None:
                if child.is_alive():
                    child.terminate()
                    child.join(timeout=.2)
                if child.is_alive():
                    child.kill()
                    child.join(timeout=1)
                child.close()
            sender.close()
            receiver.close()

    def _probe_alive(self, timeout, startup=False):
        """Ask the guardian to poll its owned child before accepting health."""
        self._refresh()
        if self._abort:
            return False
        self._probe_serial += 1
        try:
            self._guardian.stdin.write(json.dumps({'event': 'probe', 'nonce': self._probe_serial}).encode()+b'\n')
            self._guardian.stdin.flush()
        except (BrokenPipeError, OSError):
            self._fail('guardian_exit')
            return False
        deadline = time.monotonic()+timeout
        while time.monotonic() < deadline:
            self._refresh()
            if self._abort:
                return False
            if self._probe_ack == self._probe_serial:
                return True
            time.sleep(.005)
        if not startup:
            self._fail('health_failed')
        return False

    def start(self):
        if self._guardian is not None or self._finished:
            raise RuntimeUnavailable('runtime cannot be restarted')
        deadline = time.monotonic() + self.startup_timeout
        log_fd = None
        try:
            if self.install_signals:
                for signum in (signal.SIGINT, signal.SIGTERM):
                    self._old_signals[signum] = signal.signal(signum, self._handler)
            if self.output_path is not None:
                log_fd = os.open(self.output_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            self._guardian = subprocess.Popen(
                [sys.executable, '-B', str(Path(__file__).resolve()), '--owned-supervisor',
                 str(self.shutdown_grace), str(log_fd if log_fd is not None else -1)],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                env=self.env, start_new_session=True,
                pass_fds=() if log_fd is None else (log_fd,))
            if log_fd is not None:
                os.close(log_fd)
                log_fd = None
            atexit.register(self._atexit_handler)
            self._guardian.stdin.write(json.dumps({'command': self.command, 'cwd': self.cwd}).encode() + b'\n')
            self._guardian.stdin.flush()
            while time.monotonic() < deadline:
                self._refresh()
                if self._abort:
                    raise RuntimeUnavailable('runtime startup unavailable')
                if (self._daemon_pid is not None and
                        self._probe_alive(min(self.health_timeout, max(.001, deadline-time.monotonic())), startup=True) and
                        self._health(min(self.health_timeout, max(.001, deadline-time.monotonic()))) and
                        self._probe_alive(min(self.health_timeout, max(.001, deadline-time.monotonic())), startup=True)):
                    self._refresh()
                    if self._abort:
                        raise RuntimeUnavailable('runtime exited during readiness')
                    if time.monotonic() >= deadline:
                        break
                    self._ready = True
                    self._write('ready')
                    return self
                time.sleep(min(self.health_interval, max(0, deadline-time.monotonic())))
            self._fail('startup_timeout')
            raise RuntimeUnavailable('runtime readiness deadline exceeded')
        except BaseException:
            try:
                if log_fd is not None:
                    os.close(log_fd)
                if not self._failure and not self._abort:
                    self._fail('startup_failed')
            finally:
                self.finish(False)
            raise

    def ensure_healthy(self):
        self._refresh()
        if not self._ready or self._abort or self._finished:
            raise RuntimeUnavailable('runtime unavailable; stop new assignments')
        try:
            healthy = (self._probe_alive(self.health_timeout) and self._health(self.health_timeout) and
                       self._probe_alive(self.health_timeout))
        except (OSError, RuntimeError):
            healthy = False
        self._refresh()
        if not healthy:
            self._fail('health_failed')
        if self._abort:
            raise RuntimeUnavailable('runtime unavailable; stop new assignments')
        self._write('health_verified')
        return True

    def _stop_guardian(self):
        """Close the anchored group before reaping its direct guardian leader."""
        if self._guardian is None:
            return
        try:
            if not self._guardian.stdin.closed:
                self._guardian.stdin.write(b'{"event":"stop"}\n')
                self._guardian.stdin.flush()
        except (BrokenPipeError, OSError):
            pass
        finally:
            try:
                self._guardian.stdin.close()
            except (BrokenPipeError, OSError):
                pass
        deadline = time.monotonic()+self.shutdown_grace+1
        # WNOWAIT retains the session/group leader even when it has crashed.
        try:
            while not _terminal_without_reaping(self._guardian.pid):
                if time.monotonic() >= deadline:
                    self._failure = self._failure or 'cleanup_failed'
                    self._abort = True
                    break
                time.sleep(.01)
            self._fallback_cleanup = _stop_owned_group(self._guardian.pid, self.shutdown_grace)
            if not self._fallback_cleanup:
                self._failure = self._failure or 'cleanup_failed'
                self._abort = True
            # Closure was attempted while the PID was exclusively held. Reap
            # only after all group signals and confirmation have finished.
            self._guardian.wait(timeout=1)
        except (OSError, subprocess.TimeoutExpired):
            self._failure = self._failure or 'cleanup_failed'
            self._abort = True
            self._fallback_cleanup = False
        if self._guardian.returncode != 0:
            self._failure = self._failure or 'guardian_exit'
            self._abort = True

    def finish(self, schedule_complete=False, reason=None):
        if type(schedule_complete) is not bool:
            raise ValueError('explicit schedule completeness boolean required')
        if self._finished:
            return self._terminal
        reason = reason or ('schedule_complete' if schedule_complete else 'schedule_incomplete')
        if reason not in REASONS:
            raise ValueError('known lifecycle reason required')
        try:
            try:
                candidate = schedule_complete and self._ready and not self.abort_requested
                if candidate:
                    try:
                        self.ensure_healthy()
                    except RuntimeUnavailable:
                        candidate = False
            finally:
                self._stopping = True
                self._stop_guardian()
            self._refresh()
            if self._fallback_cleanup is not None:
                self._write('guardian_anchor_cleanup', known_owned_group=True,
                            owned_group_stop_confirmed=self._fallback_cleanup)
            completed = bool(candidate and not self._abort and not self._failure and self._stopped
                             and self._fallback_cleanup is True)
            self._terminal = self._write('finished', schedule_complete_asserted=schedule_complete,
                                        completed=completed, abort_requested=self._abort,
                                        reason=self._failure or ('signal' if self._pending_signal else reason),
                                        daemon_returncode=self._exit_code,
                                        owned_child_stopped=self._fallback_cleanup is True)
            return self._terminal
        finally:
            self._finished = True
            if self._guardian is not None:
                self._guardian.stdout.close()
            for signum, previous in self._old_signals.items():
                if signal.getsignal(signum) is self._handler:
                    signal.signal(signum, previous)
            self._old_signals.clear()
            atexit.unregister(self._atexit_handler)

    close = finish

    def _owner_exit(self):
        if not self._finished:
            try:
                try:
                    self.request_abort('owner_exit')
                finally:
                    self.finish(False, 'owner_exit')
            except BaseException:
                if self._guardian is not None and not self._guardian.stdin.closed:
                    self._guardian.stdin.close()

    def __enter__(self):
        return self.start()

    def __exit__(self, exc_type, exc_value, traceback):
        if not self._finished:
            try:
                self.request_abort('context_exit')
            finally:
                self.finish(False, 'context_exit')


def _supervise(grace, output_fd):
    daemon = None
    stop_signal = False
    group = os.getpid()  # This supervisor is its owner's direct session leader.
    def stop(signum, frame):
        nonlocal stop_signal
        stop_signal = True
    def emit(event, **fields):
        sys.stdout.write(json.dumps({'event': event, **fields})+'\n')
        sys.stdout.flush()
    def cleanup(report=True):
        if daemon is None:
            return
        code = daemon.poll()
        unexpected = code is not None
        if unexpected and report:
            emit('exited', returncode=code)
        try:
            os.killpg(group, signal.SIGTERM)
        except ProcessLookupError:
            pass
        deadline = time.monotonic()+grace
        while time.monotonic() < deadline:
            if _group_running(group, exclude_pid=group) is False:
                code = daemon.wait(timeout=1)
                emit('cleaned' if unexpected or not report else 'stopped', returncode=code)
                return
            time.sleep(.01)
        # A hard group kill also ends this guardian. The primary owner retains
        # our unreaped leader as its safe cleanup anchor and reports incomplete.
        emit('cleanup_failed')
        os.killpg(group, signal.SIGKILL)
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        if os.getpgrp() != group:
            raise OSError('guardian must be its owned group leader')
        config = json.loads(sys.stdin.buffer.readline(1 << 20))
        output = output_fd if output_fd >= 0 else subprocess.DEVNULL
        daemon = subprocess.Popen(config['command'], cwd=config['cwd'],
                                  stdin=subprocess.DEVNULL, stdout=output, stderr=output)
        emit('started', pid=daemon.pid, group=group)
        messages = b''
        while not stop_signal:
            if daemon.poll() is not None:
                emit('exited', returncode=daemon.returncode)
                cleanup(report=False)
                daemon = None
                return
            if select.select([sys.stdin.fileno()], [], [], .05)[0]:
                chunk = os.read(sys.stdin.fileno(), 4096)
                if not chunk:
                    break
                messages += chunk
                while b'\n' in messages:
                    line, messages = messages.split(b'\n', 1)
                    message = json.loads(line)
                    if message.get('event') == 'probe' and type(message.get('nonce')) is int:
                        if daemon.poll() is not None:
                            emit('exited', returncode=daemon.returncode)
                            cleanup(report=False)
                            daemon = None
                            return
                        emit('alive', nonce=message['nonce'])
                    else:
                        return
    except (OSError, ValueError, KeyError, TypeError):
        emit('startup_failed')
    finally:
        try:
            cleanup()
        except (BrokenPipeError, OSError):
            # Even after owner death closes stdout, the guardian must still stop
            # its anchored group. It cannot wait for receipt storage or peers.
            try:
                os.killpg(group, signal.SIGTERM)
                time.sleep(grace)
            finally:
                os.killpg(group, signal.SIGKILL)


if __name__ == '__main__':
    if len(sys.argv) != 4 or sys.argv[1] != '--owned-supervisor':
        raise SystemExit('module is imported by its launcher')
    _supervise(float(sys.argv[2]), int(sys.argv[3]))
