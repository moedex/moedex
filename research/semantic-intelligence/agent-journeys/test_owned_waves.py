"""Neutral real subprocess waves; no provider, corpus or scored assignments."""
import errno
import hashlib
import json
import os
from pathlib import Path
import signal
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

from execution_guard import ExecutionGuard, RunLimitExceeded
from owned_runtime import OwnedRuntime
from owned_waves import OwnedWaves, WaveRunFailed


class Runtime:
    failure_reason = None
    abort_requested = False

    def __init__(self, fail_when=lambda: False):
        self.fail_when = fail_when
        self.checks = 0

    def ensure_healthy(self):
        self.checks += 1
        if self.fail_when():
            self.failure_reason = 'health_failed'
            raise RuntimeError('private health callback details')
        return True


class Guard:
    def __init__(self, fail_when=lambda: False, reason='archive_limit'):
        self.fail_when, self.reason = fail_when, reason

    def check(self):
        if self.fail_when():
            raise RunLimitExceeded(self.reason)


@unittest.skipUnless(os.name == 'posix', 'POSIX process groups required')
class OwnedWavesTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='owned-waves-neutral-')
        self.root = Path(self.tmp.name)/'archive'
        self.root.mkdir()

    def tearDown(self):
        self.tmp.cleanup()

    def create(self, waves, command_for, **kwargs):
        options = dict(runtime=Runtime(), guard=Guard(), poll_interval_seconds=.01,
                       health_interval_seconds=.02, termination_grace_seconds=.2,
                       install_signal_handlers=False)
        options.update(kwargs)
        return OwnedWaves(self.root, waves, command_for, **options)

    def command(self, code=0, delay=.02):
        return [sys.executable, '-B', '-c',
                f'import time; time.sleep({delay}); print("neutral-output"); raise SystemExit({code})']

    def result(self, name):
        return json.loads((self.root/'execution'/f'{name}.result.json').read_bytes())

    def assert_refs(self, value):
        for row in value['slots']:
            for key in ('intent', 'result'):
                if row[key] is not None:
                    path = self.root/row[key]['path']
                    self.assertEqual(hashlib.sha256(path.read_bytes()).hexdigest(), row[key]['sha256'])
                    self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_fixed_waves_keep_actual_nonzero_codes_and_continue_without_retry(self):
        dispatched = []
        def command(name):
            dispatched.append(name)
            return self.command(code=7 if name == 'first' else 0)
        instance = self.create([['first', 'second'], ['third']], command)
        value = instance.run()
        self.assertEqual(dispatched, ['first', 'second', 'third'])
        self.assertTrue(value['execution_complete'])
        self.assertFalse(value['runtime_closure_verified'])
        self.assertFalse(value['abort_requested'])
        self.assertEqual(value['planned_slots'], 3)
        self.assertEqual(value['launched_slots'], 3)
        self.assertEqual(value['terminal_results'], 3)
        self.assertEqual([r['exit_code'] for r in value['actual_results']], [7, 0, 0])
        self.assertTrue(all(r['owned_group_stopped'] for r in value['actual_results']))
        self.assertEqual(self.result('first')['exit_code'], 7)
        self.assertEqual(self.result('third')['exit_code'], 0)
        intent = json.loads((self.root/'execution'/'third.intent.json').read_bytes())
        self.assertEqual(intent['wave'], 2)
        self.assert_refs(value)
        self.assertEqual(json.loads((self.root/'execution'/'waves.result.json').read_bytes()), value)
        with self.assertRaises(WaveRunFailed):
            instance.run()
        self.assertEqual(dispatched, ['first', 'second', 'third'])

    def test_terminal_postcheck_runs_before_next_wave_and_keeps_exit(self):
        closed=[]
        def check(name):
            self.assertEqual(self.result(name)['exit_code'],7)
            closed.append(name)
            return True
        def command(name):
            if name=='second':self.assertEqual(closed,['first'])
            return self.command(code=7)
        value=self.create([['first'],['second']],command,terminal_check=check).run()
        self.assertTrue(value['execution_complete'])
        self.assertTrue(all(r['runner_postcheck_passed'] for r in value['slots']))

    def test_unknown_terminal_postcheck_stops_admission_and_retains_real_exit(self):
        value=self.create([['first'],['second']],lambda name:self.command(code=7),terminal_check=lambda name:False).run()
        self.assertFalse(value['execution_complete'])
        self.assertEqual(value['stop_reason'],'runner_postcheck_failed')
        self.assertEqual(self.result('first')['exit_code'],7)
        self.assertEqual(value['slots'][1]['status'],'unlaunched')
        self.assertFalse((self.root/'execution/second.intent.json').exists())

    def test_failed_terminal_postcheck_does_not_expose_callback_details(self):
        def check(name):raise ValueError('private endpoint credential details')
        value=self.create([['first']],lambda name:self.command(code=7),terminal_check=check).run()
        self.assertFalse(value['execution_complete'])
        self.assertEqual(value['stop_reason'],'runner_postcheck_failed')
        self.assertNotIn('private endpoint',json.dumps(value))

    def test_partial_wave_manual_abort_finishes_admitted_runner_skips_rest(self):
        dispatched = []
        def command(name):
            dispatched.append(name)
            instance.request_abort()
            return self.command(code=8)
        instance = self.create([['first', 'second'], ['third']], command)
        value = instance.run()
        self.assertEqual(dispatched, ['first'])
        self.assertFalse(value['execution_complete'])
        self.assertEqual(value['stop_reason'], 'operator_abort')
        self.assertEqual(self.result('first')['exit_code'], 8)
        self.assertEqual([r['status'] for r in value['slots']],
                         ['launched_terminal', 'unlaunched', 'unlaunched'])
        for name in ('second', 'third'):
            self.assertFalse((self.root/'execution'/f'{name}.intent.json').exists())
            self.assertFalse((self.root/'execution'/f'{name}.result.json').exists())

    def test_real_signal_finishes_active_wave_under_health_checks_and_restores_handler(self):
        previous = signal.getsignal(signal.SIGTERM)
        parent = os.getpid()
        runtime = Runtime()
        script = f'import os, signal, time; time.sleep(.03); os.kill({parent},signal.SIGTERM); time.sleep(.05); raise SystemExit(9)'
        commands = {'first': [sys.executable, '-B', '-c', script],
                    'second': self.command(delay=.12), 'third': self.command()}
        instance = self.create([['first', 'second'], ['third']], commands.__getitem__,
                               runtime=runtime, install_signal_handlers=True)
        value = instance.run()
        self.assertEqual(value['stop_reason'], 'signal')
        self.assertEqual(self.result('first')['exit_code'], 9)
        self.assertEqual(self.result('second')['exit_code'], 0)
        self.assertEqual(value['slots'][2]['status'], 'unlaunched')
        self.assertGreater(runtime.checks, 2)
        self.assertIs(signal.getsignal(signal.SIGTERM), previous)

    def test_spawn_failure_has_null_errno_result_and_cancels_active_group(self):
        dispatched = []
        def command(name):
            dispatched.append(name)
            return ['/no-such-neutral-runner'] if name == 'second' else self.command(delay=5)
        value = self.create([['first', 'second', 'third']], command).run()
        self.assertEqual(dispatched, ['first', 'second'])
        self.assertEqual(value['stop_reason'], 'runner_launch_failed')
        self.assertEqual(self.result('second'),
                         {'assignment': 'second', 'exit_code': None, 'launch_failed_errno': errno.ENOENT})
        self.assertIn(self.result('first')['exit_code'], (-signal.SIGTERM, -signal.SIGKILL))
        self.assertEqual(value['slots'][2]['status'], 'unlaunched')
        self.assert_refs(value)

    def test_guard_cancel_kills_owned_descendants_but_not_an_unrelated_group(self):
        marker = self.root/'captures'/'ready'
        heartbeat = self.root/'captures'/'heartbeat'
        script = '''import os, pathlib, signal, sys, time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
child = os.fork()
if child == 0:
    count = 0
    while True:
        pathlib.Path(sys.argv[2]).write_text(str(count)); count += 1; time.sleep(.005)
pathlib.Path(sys.argv[1]).write_text(str(child))
time.sleep(10)
'''
        unrelated = subprocess.Popen(self.command(delay=10), start_new_session=True,
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            command = [sys.executable, '-B', '-c', script, str(marker), str(heartbeat)]
            instance = self.create([['first'], ['second']], lambda name: command,
                                   guard=Guard(lambda: marker.exists()))
            value = instance.run()
            self.assertEqual(value['stop_reason'], 'archive_limit')
            self.assertEqual(self.result('first')['exit_code'], -signal.SIGKILL)
            self.assertEqual(value['slots'][1]['status'], 'unlaunched')
            self.assertIsNone(unrelated.poll())
            before = heartbeat.read_text()
            time.sleep(.03)
            self.assertEqual(heartbeat.read_text(), before)
        finally:
            if unrelated.poll() is None:
                os.killpg(unrelated.pid, signal.SIGKILL)
            unrelated.wait(timeout=2)

    def test_health_failure_cancels_active_and_preserves_actual_wait_result(self):
        marker = self.root/'captures'/'ready'
        script = 'import pathlib,sys,time; pathlib.Path(sys.argv[1]).write_text("ready"); time.sleep(10)'
        runtime = Runtime(lambda: marker.exists())
        value = self.create([['first'], ['second']],
                            lambda name: [sys.executable, '-B', '-c', script, str(marker)],
                            runtime=runtime).run()
        self.assertEqual(value['stop_reason'], 'runtime_unavailable')
        self.assertIn(self.result('first')['exit_code'], (-signal.SIGTERM, -signal.SIGKILL))
        self.assertEqual(value['slots'][1]['status'], 'unlaunched')
        self.assertNotIn('private health', json.dumps(value))

    def test_terminal_leader_owned_descendants_stop_before_next_wave(self):
        unrelated = subprocess.Popen(self.command(delay=10), start_new_session=True,
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        original_root = self.root
        script = '''import os, pathlib, signal, sys, time
heartbeat = pathlib.Path(sys.argv[1])
child = os.fork()
if child == 0:
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    count = 0
    while True:
        heartbeat.write_text(str(count)); count += 1; time.sleep(.005)
while not heartbeat.exists(): time.sleep(.005)
raise SystemExit(int(sys.argv[2]))
'''
        try:
            for code in (0, 7):
                with self.subTest(leader_code=code):
                    self.root = original_root/f'case-{code}'
                    self.root.mkdir()
                    heartbeat = self.root/'captures'/'heartbeat'
                    observed_at_next_wave = []
                    def command(name):
                        if name == 'first':
                            return [sys.executable, '-B', '-c', script, str(heartbeat), str(code)]
                        before = heartbeat.read_text()
                        time.sleep(.03)
                        observed_at_next_wave.append(heartbeat.read_text() == before)
                        return self.command()
                    value = self.create([['first'], ['second']], command,
                                        termination_grace_seconds=1).run()
                    self.assertTrue(value['execution_complete'], value)
                    self.assertEqual(self.result('first')['exit_code'], code)
                    self.assertTrue(value['slots'][0]['owned_group_stopped'])
                    self.assertEqual(observed_at_next_wave, [True])
                    before = heartbeat.read_text()
                    time.sleep(.03)
                    self.assertEqual(heartbeat.read_text(), before)
                    self.assertIsNone(unrelated.poll())
        finally:
            self.root = original_root
            if unrelated.poll() is None:
                os.killpg(unrelated.pid, signal.SIGKILL)
            unrelated.wait(timeout=2)

    def test_cleanup_is_bounded_when_orchestration_sleep_raises(self):
        def bad_sleep(seconds):
            raise RuntimeError('private sleep callback details')
        value = self.create([['first'], ['second']], lambda name: self.command(delay=10),
                            sleep=bad_sleep, clock=lambda: 0).run()
        self.assertEqual(value['stop_reason'], 'orchestration_failed')
        self.assertLess(self.result('first')['exit_code'], 0)
        self.assertTrue(value['slots'][0]['owned_group_stopped'])
        self.assertEqual(value['slots'][1]['status'], 'unlaunched')

    def test_unconfirmed_group_closure_keeps_actual_result_but_refuses_completion(self):
        instance = self.create([['first'], ['second']], lambda name: self.command(),
                               termination_grace_seconds=.02)
        with patch('owned_waves._running_groups',
                   side_effect=lambda timeout: {child['process'].pid for child in instance._active}):
            value = instance.run()
        self.assertFalse(value['execution_complete'])
        self.assertEqual(value['stop_reason'], 'runner_cancel_failed')
        self.assertEqual(self.result('first')['exit_code'], 0)
        self.assertFalse(value['slots'][0]['owned_group_stopped'])
        self.assertEqual(value['slots'][1]['status'], 'unlaunched')

    def test_permission_denial_with_running_group_never_fabricates_result(self):
        marker = self.root/'captures'/'ready'
        command = [sys.executable, '-B', '-c',
                   'import pathlib,sys,time; pathlib.Path(sys.argv[1]).write_text("ready"); time.sleep(10)',
                   str(marker)]
        instance = self.create([['first'], ['second']], lambda name: command,
                               guard=Guard(lambda: marker.exists()),terminal_check=lambda name:checks.append(name) or True)
        checks=[]
        process = None
        real_popen = subprocess.Popen
        def remember(argv, *args, **kwargs):
            nonlocal process
            child = real_popen(argv, *args, **kwargs)
            if argv == command:
                process = child
            return child
        try:
            with patch('owned_waves.subprocess.Popen', side_effect=remember), \
                 patch('owned_waves.os.killpg', side_effect=PermissionError(errno.EPERM, 'neutral denial')):
                value = instance.run()
            self.assertFalse(value['execution_complete'])
            self.assertIn('runner_cancel_failed', value['stop_reasons'])
            self.assertFalse(value['slots'][0]['owned_group_stopped'])
            self.assertEqual(value['slots'][0]['status'], 'launched_unreaped')
            self.assertIsNone(value['slots'][0]['exit_code'])
            self.assertIsNone(value['slots'][0]['result'])
            self.assertFalse((self.root/'execution'/'first.result.json').exists())
            self.assertEqual(checks,['first'])
            self.assertTrue(value['slots'][0]['runner_postcheck_passed'])
        finally:
            if process is not None:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=2)

    def test_signal_then_guard_failure_cancels_instead_of_waiting_indefinitely(self):
        marker = self.root/'captures'/'ready'
        def command(name):
            instance.request_abort()
            return [sys.executable, '-B', '-c',
                    'import pathlib,sys,time; pathlib.Path(sys.argv[1]).write_text("ready"); time.sleep(10)',
                    str(marker)]
        instance = self.create([['first', 'second']], command,
                               guard=Guard(lambda: marker.exists(), 'wall_limit'))
        value = instance.run()
        self.assertEqual(value['stop_reason'], 'wall_limit')
        self.assertEqual(value['stop_reasons'], ['operator_abort', 'wall_limit'])
        self.assertLess(self.result('first')['exit_code'], 0)
        self.assertEqual(value['slots'][1]['status'], 'unlaunched')

    def test_guard_before_first_dispatch_retains_full_unlaunched_roster(self):
        dispatched = []
        instance = self.create([['first', 'second'], ['third']],
                               lambda name: dispatched.append(name),
                               guard=Guard(lambda: True, 'free_space_floor'))
        value = instance.run()
        self.assertEqual(dispatched, [])
        self.assertFalse(value['execution_complete'])
        self.assertEqual(value['launched_slots'], 0)
        self.assertEqual(value['terminal_results'], 0)
        self.assertTrue(all(r['status'] == 'unlaunched' for r in value['slots']))
        self.assertFalse(list((self.root/'execution').glob('*.intent.json')))
        self.assertEqual([path.name for path in (self.root/'execution').glob('*.result.json')],
                         ['waves.result.json'])

    def test_real_execution_guard_counts_wave_retention_without_reset(self):
        execution_guard = ExecutionGuard(self.root, 10, 100000, 0, scan_interval_seconds=0)
        value = self.create([['first'], ['second']], lambda name: self.command(),
                            guard=execution_guard).run()
        self.assertTrue(value['execution_complete'])
        self.assertGreater(execution_guard.archive_bytes, 0)
        self.assertIsNone(execution_guard.failure_reason)

    def test_real_runtime_guard_and_wave_receipts_require_separate_closure(self):
        ready = Path(self.tmp.name)/'ready'
        starts = Path(self.tmp.name)/'starts'
        daemon = '''import pathlib, signal, sys, time
pathlib.Path(sys.argv[1]).write_text('ready')
pathlib.Path(sys.argv[2]).write_text('1')
signal.signal(signal.SIGTERM, lambda signum, frame: sys.exit(0))
while True: time.sleep(.01)
'''
        guard = ExecutionGuard(self.root, 30, 100000, 0, scan_interval_seconds=0)
        deadline = guard.deadline
        runtime = OwnedRuntime([sys.executable, '-B', '-c', daemon, str(ready), str(starts)],
                               self.root/'runtime', lambda: ready.exists(), startup_timeout=2,
                               health_timeout=.2, health_interval=.02, shutdown_grace=.1,
                               install_signal_handlers=False)
        try:
            runtime.start()
            value = self.create([['first'], ['second']], lambda name: self.command(code=7),
                                runtime=runtime, guard=guard).run()
            self.assertTrue(value['execution_complete'])
            self.assertFalse(value['runtime_closure_verified'])
            self.assertEqual([r['exit_code'] for r in value['actual_results']], [7, 7])
            self.assertEqual(guard.deadline, deadline)
            self.assertGreater(guard.archive_bytes, 0)
            terminal = runtime.finish(schedule_complete=value['execution_complete'])
            self.assertTrue(terminal['completed'])
            self.assertTrue(terminal['owned_child_stopped'])
            self.assertEqual(starts.read_text(), '1')
        finally:
            runtime.finish(False)

    def test_retention_error_does_not_invent_result_and_cancels_remaining_work(self):
        instance = self.create([['first', 'second'], ['third']],
                               lambda name: self.command(code=7, delay=.01 if name == 'first' else 5))
        real_write = instance._write
        def fail_one_result(path, value):
            if path == 'execution/first.result.json':
                raise OSError('private storage failure')
            return real_write(path, value)
        with patch.object(instance, '_write', side_effect=fail_one_result):
            value = instance.run()
        self.assertEqual(value['stop_reason'], 'retention_failed')
        first = value['slots'][0]
        self.assertEqual(first['exit_code'], 7)
        self.assertEqual(first['status'], 'terminal_result_unretained')
        self.assertIsNone(first['result'])
        self.assertFalse((self.root/'execution'/'first.result.json').exists())
        self.assertLess(self.result('second')['exit_code'], 0)
        self.assertEqual(value['slots'][2]['status'], 'unlaunched')
        self.assertNotIn('private storage', json.dumps(value))

    def test_existing_execution_or_assignment_roots_reject_without_mutation(self):
        existing = self.root/'execution'
        existing.mkdir()
        sentinel = existing/'retained'
        sentinel.write_bytes(b'original')
        with self.assertRaises(WaveRunFailed):
            self.create([['first']], lambda name: self.command()).run()
        self.assertEqual(list(existing.iterdir()), [sentinel])
        self.assertEqual(sentinel.read_bytes(), b'original')
        sentinel.unlink()
        existing.rmdir()
        (self.root/'first').mkdir()
        with self.assertRaises(WaveRunFailed):
            self.create([['first']], lambda name: self.command()).run()
        self.assertFalse(existing.exists())

    def test_receipts_omit_command_arguments_environment_and_callback_errors(self):
        private_marker = 'neutral-private-marker-not-a-real-secret'
        command = self.command() + [private_marker]
        value = self.create([['first']], lambda name: command,
                            env=dict(os.environ, NEUTRAL_PRIVATE_CONFIGURATION=private_marker)).run()
        self.assertTrue(value['execution_complete'])
        for path in (self.root/'execution').glob('*.json'):
            self.assertNotIn(private_marker, path.read_text())
            self.assertNotIn('NEUTRAL_PRIVATE_CONFIGURATION', path.read_text())

    def test_invalid_rosters_and_options_are_rejected(self):
        for waves in ([], [['first', 'first']], [['../outside']], [['execution']], [[]]):
            with self.subTest(waves=waves), self.assertRaises(ValueError):
                self.create(waves, lambda name: self.command())
        for options in (dict(poll_interval_seconds=0), dict(health_interval_seconds=float('nan')),
                        dict(termination_grace_seconds=-1)):
            with self.subTest(options=options), self.assertRaises(ValueError):
                self.create([['first']], lambda name: self.command(), **options)


if __name__ == '__main__':
    unittest.main()
