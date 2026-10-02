#!/usr/bin/env python3
"""Capture real public framework binding and check independently authored gold.

Only disposable copies are built. Dependencies are exact project pins; the
acquisition manifest records each restored nupkg hash. This validates compilation
facts, never runtime activation/delivery/table existence. Use --worker to reuse a
freshly built worker, then run TestPublicDomainSourceToMCP with the printed env.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess


def digest(data):
    return hashlib.sha256(data).hexdigest()


def check_capture(rows, root, gold, incomplete=False):
    assert rows and rows[-1]['record_type'] == 'stream_summary'
    projects = [r for r in rows if r['record_type'] == 'project']
    assert projects
    by_context = {r['build_context']: r for r in projects}
    checked = []
    positive = 0
    for case in gold['cases']:
        if bool(case.get('incomplete')) != incomplete:
            continue
        source = (root / case['path']).read_bytes()
        marker = ('/*gold:' + case['id'] + '*/').encode()
        assert source.count(marker) == 1, case['id']
        offset = source.index(marker) + len(marker)
        candidates = [r for r in rows if r['record_type'] == 'reference'
                      and r['source_path'] == case['path']
                      and r['span']['byte_offset'] == offset]
        # Dynamic/unresolved references remain evidence even when no fact exists.
        assert len(candidates) == 1, (case['id'], 'reference anchor count', len(candidates))
        row = candidates[0]
        assert row['source_sha256'] == digest(source), case['id']
        assert source[offset:offset + row['span']['byte_length']].decode() == row['source_text']
        actual = row.get('domain_facts') or []
        assert len(actual) == len(case['expected']), (case['id'], actual, case['expected'])
        for fact, expected in zip(actual, case['expected']):
            assert fact['rule'] == 'csharp-framework-v1' and fact['evidence_scope'] == 'compile_time'
            for key in ['kind', 'lifetime', 'table', 'schema']:
                assert (fact.get(key) or '') == expected.get(key, ''), (case['id'], key, fact)
            targets = [{'role': t['role'], 'descriptor': t['symbol']['descriptor']} for t in fact['targets']]
            assert targets == expected['targets'], (case['id'], targets, expected['targets'])
            for target in fact['targets']:
                assert target['symbol']['namespace_kind'] == 'project'
                assert target['symbol']['namespace'] == 'domain-gold/Domain.csproj'
            assert row['binding_status'] == 'resolved'
            if case['id'] == 'consumes':
                assert row['enclosing_symbol']['descriptor'] == 'T:DomainGold.Consumer', row
            positive += 1
        checked.append({'id': case['id'], 'offset': offset, 'facts': len(actual),
                        'binding_status': row['binding_status'], 'context': row['build_context']})
    total_facts = sum(len(r.get('domain_facts') or []) for r in rows)
    assert total_facts == positive, ('unexpected facts outside gold anchors', total_facts, positive)
    return {'cases': checked, 'positive_facts': positive, 'project_count': len(by_context)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--dotnet', required=True)
    parser.add_argument('--sdk-path', required=True)
    parser.add_argument('--output-dir', required=True)
    parser.add_argument('--worker', required=True)
    parser.add_argument('--reuse-fixture', action='store_true', help='use previously restored disposable fixture')
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[2]
    base = Path(args.output_dir).resolve()
    root = base / 'fixture'
    base.mkdir(parents=True, exist_ok=True)
    if not args.reuse_fixture:
        if root.exists():
            raise SystemExit('output fixture exists; use a fresh directory or --reuse-fixture')
        shutil.copytree(repo / 'internal/eval/testdata/semantic-intelligence/domain-v1', root)
    gold = json.loads((repo / 'internal/eval/testdata/semantic-intelligence/domain-v1/gold.json').read_text())
    env = dict(os.environ, DOTNET_ROOT=str(Path(args.dotnet).resolve().parent),
               DOTNET_CLI_HOME=str(base/'home'), NUGET_PACKAGES=str(base/'packages'),
               DOTNET_SKIP_FIRST_TIME_EXPERIENCE='1', DOTNET_CLI_TELEMETRY_OPTOUT='1',
               DOTNET_NOLOGO='1', MSBUILDDISABLENODEREUSE='1')
    def run(label, command, expected=0):
        proc = subprocess.run(command, env=env, capture_output=True, timeout=180)
        (base/(label+'.stdout')).write_bytes(proc.stdout)
        (base/(label+'.stderr')).write_bytes(proc.stderr)
        assert proc.returncode == expected, (label, proc.returncode, proc.stdout[-3000:], proc.stderr[-3000:])
        return proc.stdout
    reports = {}
    for label, project, incomplete in [('complete','Domain.csproj',False),('incomplete','Incomplete/Incomplete.csproj',True)]:
        run(label+'-restore',[args.dotnet,'restore',str(root/project),'--nologo'])
        output = run(label,[args.dotnet,args.worker,'--repo','domain-gold','--root',str(root),
                           '--project',project,'--framework','net10.0','--sdk-path',args.sdk_path], 2 if incomplete else 0)
        (base/(label+'.jsonl')).write_bytes(output)
        rows = [json.loads(line) for line in output.splitlines()]
        reports[label] = check_capture(rows,root,gold,incomplete)
    packages = []
    for path in sorted((base/'packages').rglob('*.nupkg')):
        packages.append({'path':str(path.relative_to(base/'packages')),'bytes':path.stat().st_size,'sha256':digest(path.read_bytes())})
    (base/'packages.json').write_text(json.dumps(packages,indent=2)+'\n')
    result = {'status':'passed','worker_sha256':digest(Path(args.worker).read_bytes()),
              'source_sha256':{str(p.relative_to(root)):digest(p.read_bytes()) for p in sorted(root.rglob('*.cs')) if 'obj' not in p.parts},
              'gold_sha256':digest((repo/'internal/eval/testdata/semantic-intelligence/domain-v1/gold.json').read_bytes()),'reports':reports}
    (base/'worker-gold.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2))
    print(f'MOEDEX_DOMAIN_STREAM={base}/complete.jsonl MOEDEX_DOMAIN_ROOT={root} go test ./internal/semanticimport -run TestPublicDomain -count=1 -v')


if __name__ == '__main__':
    main()
