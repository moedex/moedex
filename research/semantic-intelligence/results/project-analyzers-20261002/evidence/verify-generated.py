import base64,hashlib,json
from pathlib import Path
root=Path('.local/project-analyzers');rows=[]
for variant in ('debug','release'):
 path=root/'fixture-final'/variant/'project.semantic';a=json.loads(base64.b64decode(json.loads(path.read_text())['payload']));symbols={s['id'] for s in a['symbols'] if s['key']['descriptor']=='F:FixtureGenerated.Value'};occ={o['id']:o for o in a['occurrences']};sources={s['id']:s for s in a['sources']};uses=[occ[b['occurrence_id']] for b in a['bindings'] if b.get('symbol_id') in symbols and b['status']=='resolved'];refs=[o for o in uses if o['role']=='reference' and sources[o['source_id']]['path']=='App/Api.cs'];decls=[o for o in uses if o['role']=='declaration' and sources[o['source_id']].get('generated')];assert refs and decls
 rows.append(dict(variant=variant,artifact_sha256=hashlib.sha256(path.read_bytes()).hexdigest(),generated_symbol_ids=sorted(symbols),source_references=refs,generated_declarations=decls))
(root/'generated-binding-audit.json').write_text(json.dumps(rows,indent=2)+'\n');print('Debug/Release generated declarations and source uses share resolved symbol identities.')
