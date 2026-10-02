import datetime
import hashlib
import json
import shutil
import subprocess
from pathlib import Path

local = Path('.local/default-selection')
base = Path('research/semantic-intelligence/results/default-selection-20261001')
base.mkdir(exist_ok=False)
def digest(p):
    b = p.read_bytes()
    return {'sha256': hashlib.sha256(b).hexdigest(), 'bytes': len(b)}
def copy(p, target):
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(p, target)

for p in sorted(local.iterdir()):
    if p.is_file() and p.suffix in ('.log', '.json', '.py'):
        copy(p, base / 'runs' / p.name)
for name in ('fixture-v1', 'fixture-final', 'bridge-fixture', 'implementation-fixture', 'implementation-fixture-v2', 'probe'):
    for p in sorted((local / name).rglob('*')):
        if p.is_file() and not {'bin', 'obj'} & set(p.relative_to(local / name).parts):
            copy(p, base / 'runs' / p.relative_to(local))
for p in sorted((local / 'public-v1').rglob('*')):
    if p.is_file() and p.name != 'lock':
        copy(p, base / 'runs' / p.relative_to(local))
for p in sorted((local / 'eshop').iterdir()):
    if p.is_file() and p.suffix in ('.json', '.stdout', '.stderr'):
        copy(p, base / 'runs' / p.relative_to(local))
for p in sorted((local / 'worker-integration').glob('*.jsonl')):
    copy(p, base / 'runs' / p.relative_to(local))
sources = '''internal/semantic/implementation.go
internal/semantic/default_selection_test.go
internal/semantic/validate.go
internal/semantic/domain_version.go
internal/semantic/domain_version_test.go
internal/semanticimport/import.go
internal/semanticimport/wire.go
internal/semanticimport/revalidate.go
internal/semanticimport/revalidate_test.go
internal/semanticimport/default_selection_public_test.go
internal/semanticimport/framework_bridge_public_test.go
internal/semanticindex/implementation.go
internal/semanticindex/contract_paths.go
internal/semanticindex/README.md
internal/mcp/compiler.go
internal/mcp/compiler_implementation.go
internal/mcp/compiler_implementations.go
internal/mcp/compiler_contract_paths.go
internal/mcp/compiler_default_selection_test.go
internal/app/servecmd/webhooks_holdout_public_test.go
research/semantic-intelligence/agent-journeys/check_default_selection.py
research/semantic-intelligence/agent-journeys/check_discovery_workflow.py
research/semantic-intelligence/agent-journeys/client.py
research/semantic-intelligence/check-project-capture.py
research/semantic-intelligence/DEFAULT-SELECTION-ACCEPTANCE.md
docs/adr/0048-closed-class-default-interface-selection.md
docs/plans/semantic-intelligence/PROGRAM.md
docs/plans/semantic-intelligence/DELIVERY.md'''.splitlines()
sources += [str(p) for p in Path('tools/semantic-dotnet').iterdir() if p.is_file()]
for name in sources:
    copy(Path(name), base / 'source' / (name + '.txt'))
worker_source = {}
for p in Path('tools/semantic-dotnet').iterdir():
    if p.suffix in ('.cs', '.csproj') or p.name == 'NuGet.Config':
        assert p.read_bytes() == (local / 'worker' / p.name).read_bytes(), p
        worker_source[str(p)] = digest(p)

previous = []
for name in ('framework-bridge-20261001', 'eshop-webhooks-holdout-20261001'):
    p = Path('research/semantic-intelligence/results') / name / 'result.json'
    m = json.loads(p.read_text())
    for rel, expected in m.get('files', {}).items():
        assert digest(p.parent / rel) == expected, rel
    previous.append({'path': str(p), **digest(p), 'verified_inventory_entries': len(m.get('files', {}))})
inputs = json.loads((local / 'eshop/inputs.json').read_text())
source = Path('.local/application-impact/independent/eShop')
assert inputs['source_unchanged'] and inputs['captured_contexts'] == 5 and inputs['reviewed_sources_present'] == 57
assert subprocess.check_output(['git', '-C', str(source), 'rev-parse', 'HEAD'], text=True).strip() == inputs['commit']
assert not subprocess.check_output(['git', '-C', str(source), 'status', '--porcelain', '--untracked-files=no'])
for name, expected in inputs['source_sha256'].items():
    assert digest(source / name)['sha256'] == expected
worker = local / 'worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll'
artifact = local / 'eshop/project.semantic'
assert digest(worker)['sha256'] == inputs['worker_sha256']
assert digest(artifact)['sha256'] == inputs['artifact_sha256']
assert digest(local / 'moedex') == digest(local / 'moedex-final')
public = json.loads((local / 'public-v1/report.json').read_text())
for case in public:
    d = local / 'public-v1' / case['case']
    state = json.loads((d / 'state.json').read_text())
    responses = list(d.glob('*.response.raw'))
    assert state['calls'] == case['calls'] == len(responses)
    assert state['response_bytes'] == case['response_bytes'] == sum(p.stat().st_size for p in responses)
    assert not state['stopped'] and case['passed']
    for p in responses:
        wire = json.loads(p.read_text())
        assert 'error' not in wire and not wire['result'].get('isError')
files = {str(p.relative_to(base)): digest(p) for p in sorted(base.rglob('*')) if p.is_file()}
result = {
    'finalized_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'classification': 'development_successor_not_independent_holdout_or_agent_score',
    'subagents_used': 0, 'worker_version': '8', 'index_version': 5,
    'source_commit': inputs['commit'], 'source_files_unchanged': len(inputs['source_sha256']),
    'reviewed_authored_sources': 57, 'selected_class_observations': 3,
    'artifact_sha256': inputs['artifact_sha256'],
    'public_workflows': [{k: r[k] for k in ('case', 'calls', 'response_bytes', 'passed', 'compiler_identity')} for r in public],
    'public_total_calls': sum(r['calls'] for r in public),
    'public_total_response_bytes': sum(r['response_bytes'] for r in public),
    'previous_supported_assertions': 9, 'previous_http_negative_controls': 2,
    'independent_agent_coverage': {'completed': 3, 'total': 12},
    'validation': {'go_test_all': 'passed', 'go_vet_all': 'passed', 'go_build_all': 'passed',
                   'race_packages': ['semantic', 'semanticimport', 'semanticindex', 'mcp', 'app/servecmd'],
                   'native_selection_fixture': 'passed', 'native_framework_fixture': 'passed',
                   'legacy_correspondence_fixture': 'passed', 'sdk10_worker_regression': 'passed',
                   'worker6_public_reverse_compatibility': 'passed', 'worker7_public_roundtrip': 'passed'},
    'worker_source': worker_source,
    'retained_artifacts': {str(p): digest(p) for p in (artifact, worker, local / 'moedex', local / 'moedex-final')},
    'toolchain': subprocess.check_output(['go', 'version'], text=True).strip(),
    'prior_archives': previous,
    'limitations': ['Compiler selection does not establish forwarding, a successful cast, receiver identity, or runtime execution.',
                   'Only nongeneric source classes and supported closed generic explicit source defaults enter the new rule.',
                   'No new independent agent tasks, CodeGraph production comparison, or large-corpus resource claim.'],
    'files': files}
(base / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
print('archived', len(files), 'files', sum(f['bytes'] for f in files.values()), 'bytes')
print('manifest', digest(base / 'result.json'))
