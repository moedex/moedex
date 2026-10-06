"""Neutral lifecycle failures and process ownership; no corpus or provider."""
import hashlib
import json
import multiprocessing
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

import owned_runtime as owned


DAEMON = '''import pathlib, signal, sys, time
root=pathlib.Path(sys.argv[1])
root.joinpath('starts').write_text(root.joinpath('starts').read_text()+'1' if root.joinpath('starts').exists() else '1')
def stop(signum, frame):
 root.joinpath('stopped').write_text('stopped')
 raise SystemExit(0)
signal.signal(signal.SIGTERM,stop)
root.joinpath('ready').write_text('ready')
while not root.joinpath('exit').exists(): time.sleep(.01)
raise SystemExit(7)
'''


@unittest.skipUnless(os.name == 'posix', 'POSIX child ownership required')
class OwnedRuntimeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='owned-runtime-neutral-')
        self.root = Path(self.tmp.name)
        self.runtime = None

    def tearDown(self):
        if self.runtime is not None:
            self.runtime.finish(False)
        self.tmp.cleanup()

    def create(self, **kwargs):
        values = dict(startup_timeout=2, health_timeout=.2, health_interval=.02,
                      shutdown_grace=.2, install_signal_handlers=False)
        values.update(kwargs)
        self.runtime = owned.OwnedRuntime(
            [sys.executable, '-c', DAEMON, str(self.root)], self.root/'receipts',
            values.pop('health_check', lambda: (self.root/'ready').exists()), **values)
        return self.runtime

    def events(self):
        return [json.loads(p.read_bytes()) for p in sorted((self.root/'receipts').glob('*.json'))]

    def wait_for(self, predicate, timeout=3):
        deadline = time.monotonic()+timeout
        while time.monotonic() < deadline:
            if predicate():
                return
            time.sleep(.01)
        self.fail('neutral process lifecycle deadline exceeded')

    def test_healthy_daemon_owned_across_multiple_dispatch_checks_and_completed(self):
        runtime = self.create().start()
        runtime.ensure_healthy()
        runtime.ensure_healthy()
        result = runtime.finish(True)
        self.assertTrue(result['completed'])
        self.assertTrue(result['owned_child_stopped'])
        self.assertEqual((self.root/'starts').read_text(), '1')
        self.assertTrue((self.root/'stopped').exists())
        self.assertEqual(runtime.daemon_exit_code, 0)
        self.assertFalse(runtime.abort_requested)
        self.assertIs(runtime.finish(False), result)
        with self.assertRaises(owned.RuntimeUnavailable):
            runtime.start()

    def test_startup_readiness_timeout_never_completes_and_cleans_child(self):
        runtime = self.create(health_check=lambda: False, startup_timeout=.15)
        with self.assertRaises(owned.RuntimeUnavailable):
            runtime.start()
        self.assertFalse(self.events()[-1]['completed'])
        self.assertEqual(runtime.failure_reason, 'startup_timeout')
        self.assertIsNotNone(runtime._guardian.poll())
        self.assertTrue(self.events()[-1]['owned_child_stopped'])

    def test_missing_executable_is_retained_as_startup_failure(self):
        runtime = self.create()
        runtime.command = ['/no-such-neutral-daemon']
        with self.assertRaises(owned.RuntimeUnavailable):
            runtime.start()
        self.assertEqual(runtime.failure_reason, 'startup_failed')
        self.assertFalse(self.events()[-1]['completed'])

    def test_unexpected_exit_stops_new_dispatch_and_never_restarts(self):
        runtime = self.create().start()
        (self.root/'exit').write_text('exit')
        self.wait_for(lambda: runtime.failure_reason is not None)
        with self.assertRaises(owned.RuntimeUnavailable):
            runtime.ensure_healthy()
        self.assertEqual(runtime.daemon_exit_code, 7)
        self.assertFalse(runtime.finish(True)['completed'])
        self.assertEqual((self.root/'starts').read_text(), '1')

    def test_health_failure_does_not_retry_after_readiness(self):
        runtime = self.create().start()
        (self.root/'ready').unlink()
        with self.assertRaises(owned.RuntimeUnavailable):
            runtime.ensure_healthy()
        (self.root/'ready').write_text('recovered but retry forbidden')
        with self.assertRaises(owned.RuntimeUnavailable):
            runtime.ensure_healthy()
        self.assertTrue(runtime.abort_requested)
        self.assertEqual(runtime.failure_reason, 'health_failed')
        self.assertFalse(runtime.finish(True)['completed'])

    def test_hung_health_probe_is_bounded_and_joined(self):
        runtime = self.create().start()
        runtime.check = lambda: time.sleep(20)
        before = {child.pid for child in multiprocessing.active_children()}
        started = time.monotonic()
        with self.assertRaises(owned.RuntimeUnavailable):
            runtime.ensure_healthy()
        self.assertLess(time.monotonic()-started, 2)
        self.assertEqual({child.pid for child in multiprocessing.active_children()}, before)
        self.assertFalse(runtime.finish(True)['completed'])

    def test_abort_keeps_daemon_until_end_of_wave_cleanup(self):
        runtime = self.create().start()
        runtime.request_abort()
        with self.assertRaises(owned.RuntimeUnavailable):
            runtime.ensure_healthy()
        self.assertFalse((self.root/'stopped').exists())
        self.assertFalse(runtime.finish(True)['completed'])
        self.assertTrue((self.root/'stopped').exists())

    def test_context_exception_cleans_only_owned_runtime(self):
        sentinel = subprocess.Popen([sys.executable, '-c', 'import time;time.sleep(30)'])
        try:
            runtime = self.create()
            with self.assertRaisesRegex(ValueError, 'neutral owner failure'):
                with runtime:
                    raise ValueError('neutral owner failure')
            self.assertIsNone(sentinel.poll())
            self.assertTrue((self.root/'stopped').exists())
            self.assertFalse(self.events()[-1]['completed'])
        finally:
            sentinel.terminate()
            sentinel.wait(timeout=2)

    def test_owned_descendant_ignoring_term_is_killed_without_affecting_sentinel(self):
        child = '''import pathlib,signal,sys,time
signal.signal(signal.SIGTERM,signal.SIG_IGN)
p=pathlib.Path(sys.argv[1])
while True:
 p.write_text(str(time.monotonic_ns()))
 time.sleep(.01)
'''
        daemon = '''import pathlib,signal,subprocess,sys,time
root=pathlib.Path(sys.argv[1])
child=subprocess.Popen([sys.executable,'-c',sys.argv[2],str(root/'heartbeat')])
def stop(signum,frame): raise SystemExit(0)
signal.signal(signal.SIGTERM,stop)
while not (root/'heartbeat').exists(): time.sleep(.01)
(root/'ready').write_text('ready')
while True: time.sleep(.01)
'''
        sentinel = subprocess.Popen([sys.executable, '-c', 'import time;time.sleep(30)'])
        try:
            runtime = self.create()
            runtime.command = [sys.executable, '-c', daemon, str(self.root), child]
            runtime.start()
            self.assertFalse(runtime.finish(True)['completed'])
            last = (self.root/'heartbeat').read_bytes()
            time.sleep(.1)
            self.assertEqual((self.root/'heartbeat').read_bytes(), last)
            self.assertIsNone(sentinel.poll())
        finally:
            sentinel.terminate()
            sentinel.wait(timeout=2)

    def test_stale_health_marker_cannot_hide_child_exit_before_readiness(self):
        runtime = self.create(health_timeout=1)
        runtime.command = [sys.executable, '-c', 'raise SystemExit(7)']
        def misleading_health():
            for _ in range(80):
                try:
                    os.kill(runtime._daemon_pid, 0)
                except ProcessLookupError:
                    return True  # A stale service check claims success after exit.
                time.sleep(.01)
            return False
        runtime.check = misleading_health
        with self.assertRaises(owned.RuntimeUnavailable):
            runtime.start()
        self.assertFalse(self.events()[-1]['completed'])

    def test_signal_requests_abort_and_restores_prior_handler(self):
        previous = signal.getsignal(signal.SIGTERM)
        runtime = self.create(install_signal_handlers=True).start()
        os.kill(os.getpid(), signal.SIGTERM)
        self.assertTrue(runtime.abort_requested)
        self.assertFalse((self.root/'stopped').exists())
        self.assertFalse(runtime.finish(True)['completed'])
        self.assertEqual(signal.getsignal(signal.SIGTERM), previous)
        self.assertTrue(any(e.get('signal') == signal.SIGTERM for e in self.events()))

    def test_launcher_abrupt_exit_releases_guardian_and_stops_daemon(self):
        script = '''import os,pathlib,sys
sys.path.insert(0,sys.argv[1])
from owned_runtime import OwnedRuntime
root=pathlib.Path(sys.argv[2])
r=OwnedRuntime([sys.executable,'-c',sys.argv[3],str(root)],root/'receipts',lambda:(root/'ready').exists(),startup_timeout=2,health_timeout=.2,shutdown_grace=.2)
r.start()
os._exit(0)
'''
        result = subprocess.run([sys.executable, '-B', '-c', script,
                                 str(Path(owned.__file__).parent), str(self.root), DAEMON], timeout=5)
        self.assertEqual(result.returncode, 0)
        self.wait_for(lambda: (self.root/'stopped').exists())
        self.assertFalse(any(e.get('completed') for e in self.events()))

    def test_receipts_are_exclusive_hash_chained_and_do_not_dump_secrets(self):
        runtime = self.create(env=dict(os.environ, NEUTRAL_SECRET='never-retain-this-value')).start()
        runtime.finish(True)
        previous = None
        for number, path in enumerate(sorted((self.root/'receipts').glob('*.json')), 1):
            raw = path.read_bytes()
            event = json.loads(raw)
            self.assertEqual(event['sequence'], number)
            self.assertEqual(event['previous_sha256'], previous)
            self.assertNotIn(b'never-retain-this-value', raw)
            self.assertNotIn(DAEMON.encode(), raw)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            previous = hashlib.sha256(raw).hexdigest()
        with self.assertRaises(FileExistsError):
            owned.OwnedRuntime(['unused'], self.root/'receipts', lambda: True)
        before = (self.root/'receipts'/'000001.json').read_bytes()
        runtime._sequence = 0
        with self.assertRaises(FileExistsError):
            runtime._write('created')
        self.assertEqual((self.root/'receipts'/'000001.json').read_bytes(), before)

    def test_invalid_config_and_unbounded_reason_rejected_before_spawn(self):
        for number, kwargs in enumerate(({'startup_timeout': float('nan')}, {'health_timeout': True}, {'command': []})):
            with self.subTest(kwargs=kwargs):
                args = {'command': ['unused'], 'receipts_dir': self.root/str(number), 'health_check': lambda: True, **kwargs}
                with self.assertRaises(ValueError):
                    owned.OwnedRuntime(**args)
        runtime = self.create()
        with self.assertRaises(ValueError):
            runtime.request_abort('raw exception or secret text')
        runtime.finish(False)

    def test_receipt_storage_failure_cannot_prevent_physical_shutdown(self):
        runtime = self.create().start()
        original = runtime._write
        def unavailable_storage(*args, **kwargs):
            raise OSError('neutral storage unavailable')
        runtime._write = unavailable_storage
        with self.assertRaises(OSError):
            runtime.finish(True)
        self.assertTrue((self.root/'stopped').exists())
        self.assertIsNotNone(runtime._guardian.poll())
        self.assertFalse(any(e.get('completed') for e in self.events()))
        self.assertIsNone(runtime.finish(True))
        runtime._write = original

    def test_startup_receipt_failure_still_releases_owned_child(self):
        runtime = self.create()
        original = runtime._write
        def fail_ready(event, **kwargs):
            if event in ('ready', 'unavailable'):
                raise OSError('neutral storage unavailable')
            return original(event, **kwargs)
        runtime._write = fail_ready
        with self.assertRaises(OSError):
            runtime.start()
        self.assertTrue((self.root/'stopped').exists())
        self.assertIsNotNone(runtime._guardian.poll())
        self.assertFalse(any(e.get('completed') for e in self.events()))

    def test_guardian_crash_stops_acknowledged_group_without_touching_sentinel(self):
        sentinel = subprocess.Popen([sys.executable, '-c', 'import time;time.sleep(30)'])
        try:
            runtime = self.create().start()
            runtime._guardian.kill()
            owned._terminal_without_reaping(runtime._guardian.pid)
            with self.assertRaises(owned.RuntimeUnavailable):
                runtime.ensure_healthy()
            self.assertEqual(runtime.failure_reason, 'guardian_exit')
            result = runtime.finish(False)
            self.assertFalse(result['completed'])
            self.assertTrue(result['owned_child_stopped'])
            self.assertTrue((self.root/'stopped').exists())
            self.assertIsNone(sentinel.poll())
        finally:
            sentinel.terminate()
            sentinel.wait(timeout=2)

    def test_lost_exclusive_wait_ownership_never_signals_group(self):
        with patch.object(owned, '_terminal_without_reaping', side_effect=ChildProcessError), \
                patch.object(owned.os, 'killpg') as kill:
            self.assertFalse(owned._stop_owned_group(12345, .1))
            kill.assert_not_called()

    def test_changed_live_child_group_never_signals_other_group(self):
        with patch.object(owned, '_terminal_without_reaping', return_value=False), \
                patch.object(owned.os, 'getpgid', return_value=67890), \
                patch.object(owned.os, 'killpg') as kill:
            self.assertFalse(owned._stop_owned_group(12345, .1))
            kill.assert_not_called()

    def test_permission_denial_requires_observed_nonrunning_owned_group(self):
        for running in (True, False, None):
            with self.subTest(running=running):
                with patch.object(owned, '_terminal_without_reaping', return_value=True), \
                        patch.object(owned.os, 'killpg', side_effect=PermissionError), \
                        patch.object(owned, '_group_running', return_value=running):
                    self.assertEqual(owned._stop_owned_group(12345, .1), running is False)

    def test_group_query_timeout_cannot_confirm_quiescence(self):
        with patch.object(owned.subprocess, 'run', side_effect=subprocess.TimeoutExpired('neutral-ps', 1)):
            self.assertIsNone(owned._group_running(12345))

    def test_guardian_anchor_reserves_group_before_child_acknowledgement(self):
        runtime = self.create()
        runtime._guardian = subprocess.Popen([sys.executable, '-c', 'import time;time.sleep(30)'],
                                             stdin=subprocess.PIPE, stdout=subprocess.PIPE, start_new_session=True)
        runtime._guardian.kill()
        self.wait_for(lambda: owned._terminal_without_reaping(runtime._guardian.pid))
        result = runtime.finish(False)
        self.assertFalse(result['completed'])
        self.assertTrue(result['owned_child_stopped'])
        self.assertTrue(self.events()[-2]['known_owned_group'])

    def test_guardian_and_daemon_share_parent_reserved_group(self):
        runtime = self.create().start()
        self.assertEqual(os.getpgid(runtime._guardian.pid), runtime._guardian.pid)
        self.assertEqual(os.getpgid(runtime._daemon_pid), runtime._guardian.pid)
        self.assertFalse(owned._terminal_without_reaping(runtime._guardian.pid))
        self.assertTrue(runtime.finish(True)['completed'])

    def test_daemon_stdin_cannot_consume_guardian_control_messages(self):
        runtime = self.create()
        daemon = '''import pathlib,signal,sys,time
root=pathlib.Path(sys.argv[1])
root.joinpath('stdin').write_bytes(sys.stdin.buffer.read())
def stop(signum,frame): raise SystemExit(0)
signal.signal(signal.SIGTERM,stop)
root.joinpath('ready').write_text('ready')
while True:time.sleep(.01)
'''
        runtime.command = [sys.executable, '-c', daemon, str(self.root)]
        runtime.start()
        self.assertEqual((self.root/'stdin').read_bytes(), b'')
        self.assertTrue(runtime.finish(True)['completed'])

    def test_owner_sigkill_during_hung_health_check_stops_worker_and_daemon(self):
        script = '''import pathlib,sys,time
sys.path.insert(0,sys.argv[1])
from owned_runtime import OwnedRuntime
root=pathlib.Path(sys.argv[2])
r=OwnedRuntime([sys.executable,'-c',sys.argv[3],str(root)],root/'receipts',lambda:(root/'ready').exists(),startup_timeout=2,health_timeout=20,shutdown_grace=.2)
r.start()
def hung():
 import os
 (root/'health-pid').write_text(str(os.getpid()))
 time.sleep(30)
r.check=hung
r.ensure_healthy()
'''
        owner = subprocess.Popen([sys.executable, '-B', '-c', script,
                                  str(Path(owned.__file__).parent), str(self.root), DAEMON])
        try:
            self.wait_for(lambda: (self.root/'health-pid').exists())
            worker_pid = int((self.root/'health-pid').read_text())
            owner.kill()
            owner.wait(timeout=2)
            self.wait_for(lambda: (self.root/'stopped').exists())
            def worker_stopped():
                result = subprocess.run(['/bin/ps', '-p', str(worker_pid), '-o', 'stat='],
                                        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=1)
                state = result.stdout.decode().strip()
                return not state or state.startswith('Z')
            self.wait_for(worker_stopped)
            self.assertFalse(any(e.get('completed') for e in self.events()))
        finally:
            if owner.poll() is None:
                owner.kill()
                owner.wait(timeout=2)


if __name__ == '__main__':
    unittest.main()
