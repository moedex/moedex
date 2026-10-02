#!/usr/bin/env python3
"""Score frozen development observations against unmodified native extraction JSON.
Never infers missing edges, callsites, runtime behavior or private pipeline output.
"""
import argparse
import collections
import hashlib
import json
import pathlib
import re

PROTOCOL_SHA = 'c249d3d5f8eb4101c9c0f68cd1739847afd2251be72ece43e1b89a4c96bfb644'
ALIASES = {'string':'System.String', 'decimal':'System.Decimal', 'void':'System.Void', 'bool':'System.Boolean', 'int':'System.Int32'}

def canonical(value):
    if isinstance(value, dict):
        value = value['descriptor']
    value = re.sub(r'^[MT]:', '', value or '')
    value = value.removeprefix('global::')
    value = value.replace('{', '<').replace('}', '>')
    value = re.sub(r'(?<![\w.])(?:string|decimal|void|bool|int)(?![\w.])', lambda m: ALIASES[m[0]], value)
    return re.sub(r'\s+', '', value)

def load(path):
    return json.loads(pathlib.Path(path).read_text())

def sha(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()

def relative(path, source_root):
    p = pathlib.Path(path)
    if p.is_absolute():
        try:
            return p.relative_to(source_root).as_posix()
        except ValueError:
            return None
    return p.as_posix().removeprefix('./')

def score(protocol, raw, source_root):
    source_root = pathlib.Path(source_root).resolve()
    source_hashes = protocol['reviewed_source_closure']['source_sha256']
    for path, expected in source_hashes.items():
        if sha(source_root / path) != expected:
            raise ValueError('reviewed source changed: ' + path)
    nodes, edges, unresolved = [], [], []
    for ri, result in enumerate(raw):
        for ni, node in enumerate(result.get('nodes', [])):
            nodes.append((ri, ni, node))
        for ei, edge in enumerate(result.get('edges', [])):
            edges.append((ri, ei, edge))
        unresolved.extend(result.get('unresolvedCalls', []))
    used_nodes, used_edges = set(), set()
    # DotnetProject is native project.Name, not a globally unique project key.
    # It is only mapped with the exact gold source path and a unique source owner.
    def owner_nodes(owner, source):
        return [(ri, ni, n) for ri, ni, n in nodes
                if canonical(n.get('qualifiedName')) == canonical(owner)
                and relative(n.get('filePath', ''), source_root) == source['path']
                and n.get('dotnetProject') == pathlib.Path(source['project']).stem]
    def evidence_edge(row):
        ri, ei, e = row
        return {'result_index':ri, 'edge_index':ei, 'native':e}
    def matching_edges(kind, owner, target):
        return [(ri, ei, e) for ri, ei, e in edges
                if e.get('type') == kind and canonical(e.get('sourceQN')) == canonical(owner)
                and canonical(e.get('targetQN')) == canonical(target)]
    def attribute(matches, owners):
        owner_results = {r for r, _, _ in owners}
        # Only exact source-owner nodes in the same native extraction result may
        # attribute an edge to this project. Cross-result lookup is not invented.
        return [e for e in matches if e[0] in owner_results]
    rows = []
    for case in protocol['positive_cases']:
        row = {'case_id':case['id'], 'family':case['family'], 'outcome':'not_represented', 'evidence':[], 'contexts':[], 'provenance':'none'}
        source = case['source']
        owner = case.get('owner')
        owners = owner_nodes(owner, source) if owner else []
        targets = case.get('expected_targets', [])
        kind = case.get('expected_kind')
        native_kind, target = None, None
        if case['family'] == 'resolved_call':
            native_kind, target = 'CALLS', case['target']
        elif kind in ('message_publish', 'message_consumer'):
            native_kind = {'message_publish':'PUBLISHES', 'message_consumer':'CONSUMES'}[kind]
            target = targets[0]['symbol']
        if native_kind:
            matches = attribute(matching_edges(native_kind, owner, target), owners)
            # Retain duplicate native evidence, but don't merge different source
            # owners or project attribution into a single asserted binding.
            owner_identities = {(n.get('dotnetProject'), relative(n.get('filePath',''),source_root), n.get('startLine'), n.get('endLine')) for _,_,n in owners}
            target_projects = {n.get('dotnetProject') for _, _, n in nodes if canonical(n.get('qualifiedName')) == canonical(target)}
            if matches and (len(owner_identities) != 1 or len(target_projects) > 1):
                row['outcome'] = 'ambiguous_identity'
            elif matches:
                resolved = native_kind != 'CALLS' or all((e.get('properties') or {}).get('confidence') == 1.0 for _,_,e in matches)
                row['outcome'] = 'supported_owner_level' if resolved else 'partial_native_evidence'
                row['provenance'] = 'owner_declaration_lines_only_no_native_edge_callsite'
                row['evidence'] = [evidence_edge(x) for x in matches]
                row['contexts'] = sorted({n['dotnetProject'] for _,_,n in owners})
                used_edges.update((r,i) for r,i,_ in matches)
                used_nodes.update((r,i) for r,i,_ in owners)
            elif matching_edges(native_kind,owner,target):
                row['outcome'] = 'mapping_unavailable'
                row['reason'] = 'Qualified edge exists but exact native source owner/project attribution unavailable.'
        elif kind == 'di_registration':
            want = {x['role']:canonical(x['symbol']) for x in targets}
            expected_line = (source_root/source['path']).read_bytes()[:source['byte_offset']].count(b'\n')+1
            found=[]
            for ri,ni,n in nodes:
                props=n.get('properties',{})
                if n.get('label')=='Service' and canonical(props.get('interface'))==want.get('service') and canonical(props.get('implementation'))==want.get('implementation') and relative(n.get('filePath',''),source_root)==source['path'] and n.get('dotnetProject')==pathlib.Path(source['project']).stem and n.get('startLine')==expected_line:
                    found.append((ri,ni,n))
            if found:
                lifetimes={str(n.get('properties',{}).get('lifetime','')).lower() for _,_,n in found}
                row['outcome']='supported_owner_level' if lifetimes=={case['expected_lifetime']} else 'partial_native_evidence' if lifetimes=={''} else 'wrong_assertion'
                row['provenance']='registration_line_only_no_token_span'
                row['evidence']=[{'result_index':r,'node_index':i,'native':n} for r,i,n in found]
                row['contexts']=sorted({n['dotnetProject'] for _,_,n in found})
                used_nodes.update((r,i) for r,i,_ in found)
        elif case['family']=='method_implementation':
            iface_type=case['interface_member']['descriptor'][2:].split('(')[0].rsplit('.',1)[0]
            typ=case['implementing_type']
            type_owners=owner_nodes(typ,source)
            partial=attribute(matching_edges('IMPLEMENTS',typ,iface_type),type_owners)
            if partial:
                row['outcome']='partial_native_evidence'
                row['provenance']='type_implementation_only'
                row['evidence']=[evidence_edge(x) for x in partial]
                row['reason']='Native type IMPLEMENTS does not identify the implementing method or interface member.'
                used_edges.update((r,i) for r,i,_ in partial)
        else:
            row['reason']='No corresponding native semantic relation/property in this extractor schema; generic CALLS is not the required domain assertion.'
        rows.append(row)
    negatives=[]
    for case in protocol['negative_cases']:
        row={'case_id':case['id'],'outcome':'attribution_unavailable','evidence':[]}
        if 'caller_owner' in case:
            owners=owner_nodes(case['caller_owner'],case['source'])
            matches=attribute(matching_edges('PUBLISHES',case['caller_owner'],case['message']),owners)
            row['outcome']='violation' if matches else 'no_forbidden_assertion_observed' if owners else 'attribution_unavailable'
            row['evidence']=[evidence_edge(x) for x in matches]
        else:
            row['reason']='Native edges lack callsite positions; declaration ranges cannot attribute a domain assertion to this individual negative invocation.'
        negatives.append(row)
    interesting={'PUBLISHES','CONSUMES','IMPLEMENTS'}
    extras=[evidence_edge(x) for x in edges if x[2].get('type') in interesting and (x[0],x[1]) not in used_edges]
    extra_services=[{'result_index':r,'node_index':i,'native':n} for r,i,n in nodes if n.get('label')=='Service' and (r,i) not in used_nodes]
    return {'rows':rows,'negative_controls':negatives,'outcome_counts':dict(collections.Counter(x['outcome'] for x in rows)),'raw_inventory':{'nodes':len(nodes),'pending_edges':len(edges),'unresolved_calls':len(unresolved)},'unscored_extra_candidates':extras,'unscored_service_nodes':extra_services,'limitations':['Selected developmental component observations; not full product/MCP or agent task performance.','Native project.Name plus exact file/owner attribution does not provide Moedex build-context or assembly identity guarantees.','Owner-level semantic coverage never becomes exact source-occurrence provenance.','Unmatched extra candidates require independent source review; they are not automatically false positives.']}

def main():
    p=argparse.ArgumentParser();p.add_argument('--protocol',required=True);p.add_argument('--extraction',required=True);p.add_argument('--source-root',required=True);p.add_argument('--output',required=True);p.add_argument('--capture-status',choices=['unassessed','complete-reviewed','incomplete'],default='unassessed');a=p.parse_args()
    if sha(a.protocol)!=PROTOCOL_SHA:raise ValueError('frozen protocol hash changed')
    report=score(load(a.protocol),load(a.extraction),a.source_root)
    report.update(protocol_sha256=sha(a.protocol),native_extraction_sha256=sha(a.extraction),capture_status=a.capture_status)
    pathlib.Path(a.output).write_text(json.dumps(report,indent=2)+'\n')
if __name__=='__main__':main()
