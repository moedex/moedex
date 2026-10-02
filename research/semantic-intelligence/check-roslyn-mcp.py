#!/usr/bin/env python3
"""Replay ten frozen Roslyn labels against a local MCP stdio server."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import threading
import time

CAP = 16 * 1024 * 1024

def check(ok, message):
    if not ok:
        raise RuntimeError(message)

def sha(data):
    return hashlib.sha256(data).hexdigest()

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    for key in ('binary', 'index-dir', 'artifact', 'output-dir'):
        ap.add_argument('--' + key, type=Path, required=True)
    ap.add_argument('--gold', type=Path, default=Path(__file__).with_name('roslyn-errorfacts-gold.json'))
    ap.add_argument('--timeout', type=float, default=600)
    args = ap.parse_args()
    check(0 < args.timeout <= 3600, 'timeout must be in (0,3600]')
    args.output_dir.mkdir(exist_ok=False)
    output = args.output_dir.resolve()
    requests, records, outcomes, threads = [], [], [], []
    result = {'status': 'failed', 'passed': 0, 'total': 10}
    process = None
    started = time.monotonic()
    try:
        check(args.artifact.stat().st_size <= 64*1024*1024, 'artifact exceeds 64MiB')
        check(args.gold.stat().st_size <= 1024*1024, 'gold exceeds 1MiB')
        gold_bytes, artifact_bytes = args.gold.read_bytes(), args.artifact.read_bytes()
        gold, envelope = json.loads(gold_bytes), json.loads(artifact_bytes)
        check(gold['schema'] == 'moedex.roslyn-compiler-acceptance.v1', 'wrong gold schema')
        check(len(gold['labels']) == 10 and len({x['id'] for x in gold['labels']}) == 10, 'expected ten distinct labels')
        payload = base64.b64decode(envelope['payload'], validate=True)
        check(sha(payload) == envelope['sha256'], 'artifact payload checksum mismatch')
        artifact = json.loads(payload)
        contexts = [x for x in artifact['contexts'] if x['project'] == gold['project']]
        check(len(contexts) == 1 and contexts[0]['status'] == 'complete', 'expected one complete project context')
        context = contexts[0]
        snapshots = [x for x in artifact['snapshots'] if x['id'] == context['snapshot_id']]
        check(len(snapshots) == 1 and snapshots[0]['repo'] == 'roslyn' and snapshots[0]['commit'] == gold['commit'], 'wrong source snapshot')
        sources = {x['id']: x for x in artifact['sources']}
        for label in gold['labels']:
            check(any(sources[s]['path'] == label['path'] and sources[s]['raw_sha256'] == label['source_sha256'] for s in context['source_ids']), 'missing gold source: ' + label['id'])
        artifact_hash = sha(artifact_bytes)
        command = [str(args.binary.resolve()), 'serve', '-index-dir', str(args.index_dir.resolve()), '-mcp', '-embed', 'none']
        result.update(command=command, context_id=context['id'], artifact_sha256=artifact_hash, gold_sha256=sha(gold_bytes))
        process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        responses = queue.Queue()
        errors = queue.Queue()

        def drain(stream, name):
            try:
                count = 0
                with (output / ('mcp.' + name)).open('wb') as log:
                    while True:
                        data = stream.readline(CAP + 1) if name == 'stdout' else stream.read1(65536)
                        if not data:
                            if name == 'stdout':
                                responses.put(None)
                            break
                        count += len(data)
                        check(count <= CAP, name + ' exceeds 16MiB log cap')
                        log.write(data)
                        log.flush()
                        if name == 'stdout':
                            check(data.endswith(b'\n'), 'oversized or incomplete MCP line')
                            responses.put(data)
            except Exception as exc:
                errors.put(str(exc))
                responses.put(None)

        for stream, name in [(process.stdout, 'stdout'), (process.stderr, 'stderr')]:
            thread = threading.Thread(target=drain, args=(stream, name), daemon=True)
            thread.start()
            threads.append(thread)

        def call(method, params):
            request = {'jsonrpc': '2.0', 'id': len(requests)+1, 'method': method, 'params': params}
            requests.append(request)
            process.stdin.write((json.dumps(request)+'\n').encode())
            process.stdin.flush()
            remaining = min(120, started + args.timeout - time.monotonic())
            check(remaining > 0, 'overall timeout')
            try:
                line = responses.get(timeout=remaining)
            except queue.Empty:
                raise RuntimeError('MCP response timeout') from None
            check(errors.empty(), 'MCP stream failure: ' + (errors.get() if not errors.empty() else ''))
            check(line is not None, 'MCP stdout closed before response')
            response = json.loads(line)
            records.append(response)
            check(response.get('id') == request['id'] and 'error' not in response, 'MCP error or response ID mismatch: '+str(response))
            return response['result']

        generations = set()
        def tool(name, arguments):
            response = call('tools/call', {'name': name, 'arguments': arguments})
            check(not response.get('isError'), 'tool failed: '+str(response))
            content = response['structuredContent']
            check(content['status'] == 'ok' and not content['truncated'], 'bad tool status/truncation: '+str(content))
            check(content['artifact_sha256'] == artifact_hash, 'served artifact mismatch')
            check(content['evidence'] == 'recorded-compiler-context', 'unexpected evidence claim')
            check(bool(content['snapshot_id']), 'missing serving generation')
            generations.add(content['snapshot_id'])
            return content

        call('initialize', {'protocolVersion':'2024-11-05', 'capabilities':{}, 'clientInfo':{'name':'roslyn-frozen-gold','version':'1'}})
        for label in gold['labels']:
            content = tool('compiler_binding_at', {'repo':'roslyn', 'path':label['path'], 'byte_offset':label['byte_offset'], 'context_id':context['id'], 'raw_sha256':label['source_sha256']})
            found = [r for r in content['results'] if r['reference_kind'] == label['reference_kind'] and r['role'] == 'reference' and r['byte_length'] == label['byte_length']]
            check(len(found) == 1, 'expected exactly one matching fact: '+label['id'])
            fact = found[0]
            check(fact['binding_status'] == 'resolved' and fact['symbol']['descriptor'] == label['expected_descriptor'], 'wrong binding: '+label['id'])
            check(fact['context_id'] == context['id'] and fact['repo'] == 'roslyn' and fact['path'] == label['path'] and fact['raw_sha256'] == label['source_sha256'] and fact['byte_offset'] == label['byte_offset'], 'wrong fact provenance: '+label['id'])
            definition_status = None
            if label['definition_expected']:
                defs = tool('compiler_definitions', {'repo':'roslyn', 'context_id':context['id'], 'symbol_id':fact['symbol']['id']})
                valid = [r for r in defs['results'] if r['role'] == 'declaration' and r['binding_status'] == 'resolved' and r['symbol']['id'] == fact['symbol']['id'] and r['symbol']['descriptor'] == label['expected_descriptor'] and r['context_id'] == context['id'] and r['repo'] == 'roslyn' and r['path'] == label['path'] and r['raw_sha256'] == label['source_sha256']]
                check(len(valid) == 1, 'expected local source definition: '+label['id'])
                definition_status = defs['status']
            outcomes.append({'id':label['id'], 'passed':True, 'definition_status':definition_status})
        check(len(generations) == 1, 'queries crossed serving generations')
        result.update(status='passed', passed=len(outcomes), snapshot_id=next(iter(generations)))
    except Exception as exc:
        result['error'] = str(exc)
    finally:
        if process is not None:
            try:
                process.stdin.close()
            except BrokenPipeError:
                pass
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            for thread in threads:
                thread.join(timeout=5)
            process.stdout.close()
            process.stderr.close()
            result['server_exit_code'] = process.returncode
            if not errors.empty():
                result.update(status='failed', error=errors.get())
        result.update(outcomes=outcomes, elapsed_seconds=time.monotonic()-started)
        for name, rows in [('mcp-input.jsonl', requests), ('mcp.jsonl', records)]:
            (output/name).write_text(''.join(json.dumps(r)+'\n' for r in rows))
        (output/'mcp-evaluation.json').write_text(json.dumps(result, indent=2)+'\n')
    print(json.dumps(result))
    return 0 if result['status'] == 'passed' else 1

if __name__ == '__main__':
    raise SystemExit(main())
