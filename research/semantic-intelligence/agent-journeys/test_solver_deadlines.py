"""Source-free deadline regressions using the real bridge and loopback fixture."""
import base64
import json
import os
from pathlib import Path
import selectors
import shutil
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch

import isolated_solver as solver


FAKE_CODEX = r'''#!/usr/bin/env node
const http = require('node:http');
const args = process.argv.slice(2);
function endpoint(prefix) {
  const pair = args.find(value => value.startsWith(prefix + '='));
  return JSON.parse(pair.slice(pair.indexOf('=') + 1));
}
function post(url, value) {
  return new Promise((resolve, reject) => {
    const request = http.request(url, {method: 'POST'}, response => {
      response.resume();
      response.on('end', () => resolve(response.statusCode));
    });
    request.on('error', reject);
    request.end(JSON.stringify(value));
  });
}
process.stdin.resume();
process.stdin.on('end', async () => {
  try {
    const mcp = await post(endpoint('mcp_servers.retrieval.url'),
      {jsonrpc: '2.0', id: 1, method: 'tools/list'});
    const provider = await post(endpoint('model_providers.recording_bridge.base_url') + '/responses',
      {model: 'synthetic', reasoning: {effort: 'high'}});
    console.log(JSON.stringify({type: 'fixture.completed', statuses: [mcp, provider]}));
    process.exit(mcp === 200 && provider === 200 ? 0 : 2);
  } catch { process.exit(2); }
});
'''


class ObservedInput:
    def __init__(self, stream, messages):
        self.stream, self.messages = stream, messages
    def write(self, raw):
        self.messages.append(json.loads(raw))
        return self.stream.write(raw)
    def flush(self):
        return self.stream.flush()
    def close(self):
        return self.stream.close()


class Record:
    def __init__(self, seconds):
        self.deadline = time.monotonic() + seconds
        self.stop_reason = None
    def remaining_seconds(self):
        return self.deadline - time.monotonic()
    def stop(self, reason):
        self.stop_reason = reason


@unittest.skipUnless(shutil.which('node'), 'Node is required for bridge deadline regression checks')
class DeadlineTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='solver-deadline-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        fake = self.root / 'codex'
        fake.write_text(FAKE_CODEX)
        fake.chmod(0o755)
        # Confine fixed container /tmp paths to this fixture. No real answer or
        # host workspace is inspected by these source-free subprocesses.
        bridge = Path(solver.__file__).with_name('container_solver.cjs').read_text()
        bridge = bridge.replace('/tmp/solver-workspace', str(self.root / 'workspace'))
        bridge = bridge.replace('/tmp/solver-answer.json', str(self.root / 'answer.json'))
        self.bridge = self.root / 'bridge.cjs'
        self.bridge.write_text(bridge)
        self.timer = self.root / 'timer.cjs'
        # Accelerate the historical implicit 60-second timer only. Assignment
        # and explicit exchange deadlines retain their real durations.
        self.timer.write_text('const original = global.setTimeout;\n'
                              'global.setTimeout = (fn, ms, ...args) => original(fn, ms === 60000 ? 50 : ms, ...args);\n')
        self.command = ['node', '--require', str(self.timer), str(self.bridge)]
        self.env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ['PATH'])

    def controller(self, delay=0, seconds=2, record=None, expire_on_response=False):
        messages, processes = [], []
        real_popen = subprocess.Popen
        response = solver.probe_provider_response
        def launch(command, **kwargs):
            command = list(command)
            cid = command.index('--cidfile')
            del command[cid:cid + 2]
            process = real_popen(command, env=self.env, **kwargs)
            process.stdin = ObservedInput(process.stdin, messages)
            processes.append(process)
            return process
        def delayed_response(*args):
            time.sleep(delay)
            completed = response(*args)
            if expire_on_response:
                record.deadline = time.monotonic() - 1
            return completed
        config = {'image': 'sha256:' + 'a' * 64, 'model': 'synthetic',
                  'reasoning_effort': 'high', 'prompt': 'synthetic deadline probe',
                  'enabled_tools': ['allowed'], 'timeout_seconds': seconds}
        root = self.root / 'capture'
        try:
            with patch.object(solver, 'container_command', return_value=self.command), \
                    patch.object(solver.subprocess, 'Popen', side_effect=launch), \
                    patch.object(solver, 'probe_provider_response', side_effect=delayed_response):
                result = solver.run_container(config, root, record=record, probe=True)
            return result, messages, root
        finally:
            for process in processes:
                try:
                    process.stdin.close()
                except BrokenPipeError:
                    pass
                process.stdout.close()

    def test_controller_forwards_remaining_deadline_and_allows_slow_complete_exchange(self):
        record = Record(300)
        result, messages, root = self.controller(delay=0.12, seconds=600, record=record)
        start = messages[0]['config']
        self.assertGreater(start['timeout_ms'], 295000)
        self.assertLessEqual(start['timeout_ms'], 300000)
        self.assertEqual(start['exchange_timeout_ms'], start['timeout_ms'])
        self.assertEqual(result['exit_code'], 0)
        self.assertTrue(result['capture_complete'])
        self.assertEqual(result['provider_requests'], 1)
        self.assertEqual(len([m for m in messages if m['channel'] == 'response']), 2)
        events = [json.loads(line) for line in (root / 'events.jsonl').read_text().splitlines()]
        self.assertFalse(any(event.get('channel') == 'error' for event in events))

    def test_fast_provider_and_mcp_exchanges_complete(self):
        result, messages, root = self.controller()
        self.assertEqual(result['exit_code'], 0)
        self.assertTrue(result['capture_complete'])
        self.assertTrue((root / 'mcp-0001.response.raw').is_file())
        self.assertTrue((root / 'provider-0001.response.raw').is_file())
        self.assertEqual([m['status'] for m in messages if m['channel'] == 'response'], [200, 200])

    def test_expired_assignment_retains_completed_exchange_without_late_reply(self):
        record = Record(30)
        messages = []
        real_write = ObservedInput.write
        def observe(stream, raw):
            messages.append(json.loads(raw))
            return real_write(stream, raw)
        with patch.object(ObservedInput, 'write', observe), self.assertRaises(TimeoutError):
            self.controller(seconds=2, record=record, expire_on_response=True)
        root = self.root / 'capture'
        receipt = json.loads((root / 'provider-0001.receipt.json').read_text())
        self.assertTrue(receipt['transport_complete'])
        self.assertEqual(receipt['status'], 200)
        self.assertEqual(record.stop_reason, 'TimeoutError')
        self.assertFalse(json.loads((root / 'inventory.json').read_text())['complete'])
        self.assertEqual(len([m for m in messages if m['channel'] == 'response']), 1)

    def direct_bridge(self, config, delay=0, respond=True):
        process = subprocess.Popen(self.command, stdin=subprocess.PIPE,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=self.env)
        selector = selectors.DefaultSelector()
        selector.register(process.stdout, selectors.EVENT_READ)
        events, buffer = [], b''
        def send(value):
            process.stdin.write(solver.canonical(value) + b'\n')
            process.stdin.flush()
        send({'channel': 'start', 'config': config})
        started = time.monotonic()
        try:
            while time.monotonic() - started < 3:
                if not selector.select(0.1):
                    if process.poll() is not None:
                        break
                    continue
                chunk = os.read(process.stdout.fileno(), 65536)
                if not chunk:
                    break
                buffer += chunk
                while b'\n' in buffer:
                    line, buffer = buffer.split(b'\n', 1)
                    value = json.loads(line)
                    events.append(value)
                    if value['channel'] in ('mcp', 'provider'):
                        if value['channel'] == 'provider':
                            if not respond:
                                continue
                            time.sleep(delay)
                        try:
                            send({'channel': 'response', 'id': value['id'], 'status': 200,
                                  'content_type': 'application/json',
                                  'body_base64': base64.b64encode(b'{}').decode()})
                        except BrokenPipeError:
                            pass
                if any(value['channel'] == 'error' and value['error_type'] == 'invalid_start_config'
                       for value in events):
                    break
            process.wait(timeout=2)
            return events, process.returncode, time.monotonic() - started
        finally:
            selector.close()
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=2)
            try:
                process.stdin.close()
            except BrokenPipeError:
                pass
            process.stdout.close()
            process.stderr.close()

    def start_config(self, **updates):
        return dict({'model': 'synthetic', 'reasoning_effort': 'high', 'prompt': 'synthetic probe',
                     'enabled_tools': ['allowed'], 'timeout_ms': 1500}, **updates)

    def test_bridge_default_borrows_assignment_deadline(self):
        events, code, _ = self.direct_bridge(self.start_config(timeout_ms=600000), delay=0.12)
        self.assertEqual(code, 0)
        self.assertFalse(any(event['channel'] == 'error' for event in events))

    def test_explicit_shorter_exchange_deadline_still_expires(self):
        events, code, elapsed = self.direct_bridge(self.start_config(exchange_timeout_ms=80), respond=False)
        self.assertNotEqual(code, 0)
        self.assertLess(elapsed, 1.5)
        self.assertIn('relay_response_timeout', [e['error_type'] for e in events if e['channel'] == 'error'])

    def test_assignment_deadline_stops_unanswered_bridge(self):
        events, code, elapsed = self.direct_bridge(self.start_config(timeout_ms=500), respond=False)
        self.assertNotEqual(code, 0)
        self.assertLess(elapsed, 2)
        self.assertIn('solver_assignment_timeout', [e['error_type'] for e in events if e['channel'] == 'error'])

    def test_bridge_rejects_invalid_exchange_deadlines(self):
        for value in (0, -1, 1.5, True, '1000', None, 3600001):
            with self.subTest(value=value):
                events, code, _ = self.direct_bridge(self.start_config(exchange_timeout_ms=value), respond=False)
                self.assertNotEqual(code, 0)
                self.assertIn({'channel': 'error', 'error_type': 'invalid_start_config'}, events)

    def test_controller_rejects_deadlines_outside_bridge_range_without_spawning(self):
        for value in (0, -1, True, float('inf'), 3601):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as tmp, \
                    patch.object(solver.subprocess, 'Popen') as launch:
                with self.assertRaises(ValueError):
                    solver.run_container({'image': 'sha256:' + 'a' * 64, 'timeout_seconds': value},
                                         Path(tmp) / 'capture', probe=True)
                launch.assert_not_called()


if __name__ == '__main__':
    unittest.main()
