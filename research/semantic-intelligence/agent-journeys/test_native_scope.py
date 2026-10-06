"""Synthetic privacy/binding probes; no corpus, Docker or provider calls."""
from copy import deepcopy
import json
from pathlib import Path
import unittest

import isolated_solver as solver
import native_scope as ns
import run_record as rr
from native_http import rpc_response
import test_run_record as record_fixtures


def policy(backend='codegraph-v1'):
    return {'schema':'native-source-scope-v1','backend':backend,
            'allowed_tools':['codegraph_search','graph_source','graph_trace'] if backend=='codegraph-v1' else ['read_source','search_context','list_repos'],
            'projects':[{'repository':'example/api','url':'https://gitlab.example.com/example/api',
                         'commit':'a'*40,'aliases':['Example.Api'], 'files':{'src/api.cs':'b'*40}},
                        {'repository':'example/library','url':'https://gitlab.example.com/example/library',
                         'commit':'c'*40,'aliases':['Example.Library'], 'files':{'src/library.cs':'d'*40}}],
            'corpus_fingerprint':'f'*64 if backend=='moedex-index-v1' else None,'graph_revision':None}


def node(identity=1, repository='Example.Api', path='src/api.cs'):
    return {'nodeId':identity,'name':'Process','label':'Method','project':repository,
            'location':{'project':repository,'path':path,'startLine':1,'endLine':3},
            'traceSelector':{'tool':'graph_trace','nodeId':identity}}


def envelope(tool='codegraph_search', data=None, revision=7, projects=None):
    if data is None:
        data={'totalCount':1,'shownCount':1,'results':[node()]}
    structured={'tool':tool,'operation':'nodes' if tool=='codegraph_search' else 'file',
                'formatVersion':1,'snapshot':{'graphRevision':revision,'cacheable':True,
                'projects':projects if projects is not None else [{'project':'Example.Api','commitSha':'a'*40}]},
                'warnings':[],'result':data}
    return {'jsonrpc':'2.0','id':1,'result':{'structuredContent':structured,
            'content':[{'type':'text','text':json.dumps(structured)}]}}


def source(cursor=None):
    result={'location':{'project':'Example.Api','path':'src/api.cs','blobSha':'b'*40,'commitSha':'a'*40,
                       'startLine':1,'endLine':1},'language':'csharp','totalLines':3,
            'lines':[{'number':1,'text':'class Api {}'}]}
    value=envelope('graph_source',result)
    if cursor is not None:
        value['result']['structuredContent']['page']={'returned':1,'total':3,'truncated':True,'nextCursor':cursor}
        mirror(value)
    return value


def mirror(value):
    value['result']['content']=[{'type':'text','text':json.dumps(value['result']['structuredContent'])}]


def request(tool='codegraph_search', **args):
    return {'name':tool,'arguments':dict({'project':'Example.Api','format':'json'},**args)}


class ScopeTests(unittest.TestCase):
    def setUp(self):
        self.scope=ns.NativeScope(policy())

    def accept(self, value=None, params=None):
        return self.scope.accept(value or envelope(),self.scope.prepare(params or request()))

    def test_accepted_source_anchor_registers_assignment_local_node(self):
        self.accept()
        context=self.scope.prepare({'name':'graph_trace','arguments':{'nodeId':1,'operation':'call_path'}})
        self.assertEqual(context['project'],'example/api')
        with self.assertRaises(ns.ScopeViolation):
            ns.NativeScope(policy()).prepare({'name':'graph_source','arguments':{'nodeId':1}})

    def test_scope_aliases_and_canonical_urls_resolve_exactly(self):
        for identity in ('Example.Api','example/api','https://gitlab.example.com/example/api'):
            self.assertEqual(self.scope.project(identity),'example/api')
        for identity in ('api','example/api-else',' Example.Api',['Example.Api']):
            with self.assertRaises(ns.ScopeViolation):self.scope.project(identity)

    def test_policy_invalid_rosters_aliases_pins_and_tools_rejected(self):
        mutations=[lambda p:p['allowed_tools'].append('rag_search'),
                   lambda p:p['projects'][1]['aliases'].append('Example.Api'),
                   lambda p:p['projects'][0].update(commit='latest'),
                   lambda p:p['projects'][0]['files'].update({'../outside':'b'*40}),
                   lambda p:p['projects'][0].update(url='https://user:secret@example.com/api'),
                   lambda p:p.update(graph_revision=True)]
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                value=policy();mutate(value)
                with self.assertRaises(ns.ScopeViolation):ns.NativeScope(value)

    def test_outside_and_unissued_request_selectors_rejected(self):
        for args in ({'project':'outside'},{'nodeId':99},{'cursor':'invented'},{'project':'Example.Api','nodeId':99},
                     {'project':'Example.Api','format':'markdown'}):
            with self.assertRaises(ns.ScopeViolation):
                self.scope.prepare({'name':'graph_source','arguments':args})
        with self.assertRaises(ns.ScopeViolation):self.scope.prepare({'name':'graph_cluster','arguments':{}})

    def test_global_project_search_cannot_ignore_supplied_project(self):
        with self.assertRaises(ns.ScopeViolation):
            self.scope.prepare(request(scope='projects'))

    def test_metadata_projection_hides_outside_names_counts_and_prose(self):
        data={'projects':[{'repoUrl':'https://outside.example.com/private','commitSha':'e'*40,'name':'outside-private'},
                          {'repoUrl':'https://gitlab.example.com/example/api','commitSha':'a'*40,'description':'ignored-prose'}],
              'totalCount':9999}
        value=envelope(data=data,projects=[])
        visible=self.accept(value,{'name':'codegraph_search','arguments':{'scope':'projects','namePattern':'*'}})
        text=json.dumps(visible)
        self.assertNotIn('outside-private',text);self.assertNotIn('9999',text);self.assertNotIn('ignored-prose',text)
        self.assertIn('example/api',text)

    def test_metadata_rows_need_exact_pin(self):
        value=envelope(data={'projects':[{'repoUrl':'https://gitlab.example.com/example/api','commitSha':'e'*40}]},projects=[])
        with self.assertRaises(ns.ScopeViolation):
            self.accept(value,{'name':'codegraph_search','arguments':{'scope':'projects'}})

    def test_metadata_url_variants_require_explicit_frozen_alias(self):
        value=envelope(data={'projects':[{'repoUrl':'https://gitlab.example.com/example/api.git','commitSha':'a'*40}]},projects=[])
        params={'name':'codegraph_search','arguments':{'scope':'projects'}}
        self.assertEqual(self.accept(value,params)['result']['structuredContent']['result']['projects'],[])
        frozen=policy();frozen['projects'][0]['aliases'].append('https://gitlab.example.com/example/api.git')
        self.scope=ns.NativeScope(frozen)
        rows=self.accept(value,params)['result']['structuredContent']['result']['projects']
        self.assertEqual(rows,[{'project':'example/api','repoUrl':'https://gitlab.example.com/example/api','commitSha':'a'*40}])
        frozen['projects'][0]['aliases'].append('https://user:secret@example.com/api')
        with self.assertRaises(ns.ScopeViolation):ns.NativeScope(frozen)

    def test_mixed_metadata_projection_cannot_mint_selectors_or_structure_revision(self):
        self.accept()
        value=envelope(data={'projects':[{'repoUrl':'https://gitlab.example.com/example/api','commitSha':'a'*40,
                                         'nodeId':99,'cursor':'private-cursor','extraContext':'private'}]},projects=[],revision=99)
        value['result']['structuredContent']['snapshot']['cacheable']=False
        value['result']['structuredContent']['warnings']=['partial_coverage']
        mirror(value)
        result=self.accept(value,{'name':'codegraph_search','arguments':{'scope':'projects'}})
        self.assertNotIn('private',json.dumps(result));self.assertEqual(self.scope.revision,7)
        self.assertNotIn(99,self.scope.nodes);self.assertFalse(self.scope.cursors)

    def test_snapshot_mismatches_are_transactional(self):
        mutations=[lambda d:d['snapshot'].update(cacheable=False),
                   lambda d:d['snapshot'].update(projects=[]),
                   lambda d:d['snapshot']['projects'][0].update(commitSha='e'*40),
                   lambda d:d['snapshot']['projects'][0].update(project='outside'),
                   lambda d:d.update(warnings=['partial_coverage']),
                   lambda d:d['snapshot'].pop('graphRevision')]
        for mutate in mutations:
            value=envelope();mutate(value['result']['structuredContent']);mirror(value)
            with self.assertRaises(ns.ScopeViolation):self.accept(value)
            self.assertFalse(self.scope.nodes);self.assertIsNone(self.scope.revision)

    def test_graph_revision_change_does_not_register_new_selectors(self):
        self.accept();value=envelope(data={'results':[node(2)]},revision=8)
        with self.assertRaises(ns.ScopeViolation):self.accept(value)
        self.assertEqual(self.scope.revision,7);self.assertNotIn(2,self.scope.nodes)

    def test_fixed_freeze_revision_is_checked_on_first_exchange(self):
        value=policy();value['graph_revision']=8;self.scope=ns.NativeScope(value)
        with self.assertRaises(ns.ScopeViolation):self.accept()

    def test_node_rebinding_and_unanchored_nodes_rejected(self):
        self.accept()
        for bad in (node(2,path='not-tracked.cs'),node(2,repository='outside'),
                    {'nodeId':2,'name':'secret','project':'Example.Api'}):
            value=envelope(data={'results':[bad]})
            with self.assertRaises(ns.ScopeViolation):self.accept(value)
        value=envelope(data={'results':[node(1,'Example.Library','src/library.cs')]},
                       projects=[{'project':'Example.Api','commitSha':'a'*40},{'project':'Example.Library','commitSha':'c'*40}])
        with self.assertRaises(ns.ScopeViolation):self.accept(value)
        self.assertEqual(self.scope.nodes[1],('example/api','src/api.cs'))

    def test_cursor_binding_registered_only_from_accepted_source(self):
        self.accept(source('opaque-cursor'),request('graph_source',filePath='src/api.cs'))
        context=self.scope.prepare({'name':'graph_source','arguments':{'cursor':'opaque-cursor'}})
        self.assertEqual(context['path'],'src/api.cs')
        with self.assertRaises(ns.ScopeViolation):
            self.scope.prepare({'name':'graph_source','arguments':{'cursor':'opaque-cursor','project':'Example.Api'}})
        with self.assertRaises(ns.ScopeViolation):
            ns.NativeScope(policy()).prepare({'name':'graph_source','arguments':{'cursor':'opaque-cursor'}})

    def test_source_blob_commit_path_and_extra_source_fields_rejected(self):
        for field,changed in [('blobSha','e'*40),('commitSha','e'*40),('path','other.cs')]:
            value=source('cursor');value['result']['structuredContent']['result']['location'][field]=changed;mirror(value)
            with self.assertRaises(ns.ScopeViolation):self.accept(value,request('graph_source',filePath='src/api.cs'))
            self.assertFalse(self.scope.cursors)
        value=source();value['result']['structuredContent']['result']['extraContext']='outside fact';mirror(value)
        with self.assertRaises(ns.ScopeViolation):self.accept(value,request('graph_source',filePath='src/api.cs'))

    def test_poisoned_markdown_is_never_displayed_and_json_conflicts_reject(self):
        value=envelope();value['result']['content']=[{'type':'text','text':'# outside-private-source'}]
        self.assertNotIn('outside-private-source',json.dumps(self.accept(value)))
        value=envelope();value['result']['content'][0]['text']='{"outside":"private"}'
        with self.assertRaises(ns.ScopeViolation):self.accept(value)

    def test_unknown_fact_bearing_graph_fields_rejected(self):
        root=node();root.pop('traceSelector')
        result={'root':root,'direction':'outbound','depth':1,'entries':[],'extraContext':'DB/doc fact'}
        value=envelope('graph_trace',result);value['result']['structuredContent']['operation']='call_path';mirror(value)
        with self.assertRaises(ns.ScopeViolation):self.accept(value,request('graph_trace',operation='call_path'))
        result.pop('extraContext');root['properties']={'outside':'secret'};mirror(value)
        with self.assertRaises(ns.ScopeViolation):self.accept(value,request('graph_trace',operation='call_path'))

    def test_valid_call_path_and_previously_anchored_event_node_preserved(self):
        self.accept()
        root=node();root.pop('traceSelector')
        result={'root':root,'direction':'outbound','depth':1,'entries':[
            {'node':dict(node(2),id=2),'depth':1,'edgeType':'CALLS','parentNodeId':1,
             'edgeProperties':{'confidence':1,'confidence_band':'high'}}]}
        value=envelope('graph_trace',result);value['result']['structuredContent']['operation']='call_path';mirror(value)
        self.accept(value,request('graph_trace',operation='call_path'))
        self.assertIn(2,self.scope.nodes)
        result={'roots':[root],'count':1,'publishers':[{'nodeId':2,'project':'Example.Api','name':'Publish'}]}
        value=envelope('graph_trace',result);value['result']['structuredContent']['operation']='publishers';mirror(value)
        self.accept(value,request('graph_trace',operation='publishers'))

    def test_root_and_parent_selectors_need_accepted_source_anchors(self):
        for mutation in ('root', 'parent', 'fractional_parent', 'nested_location', 'candidates'):
            with self.subTest(mutation=mutation):
                result={'root':node(), 'entries':[{'node':node(2),'parentNodeId':1}]}
                value=envelope('graph_trace',result)
                data=value['result']['structuredContent'];data['operation']='call_path'
                if mutation=='root':data['selection']={'rootNodeIds':[999]}
                elif mutation=='parent':result['entries'][0]['parentNodeId']=999
                elif mutation=='fractional_parent':result['entries'][0]['parentNodeId']=1.5
                elif mutation=='nested_location':result['root']['location']['startLine']={'outside':'fact'}
                else:data['selection']={'candidates':{}}
                mirror(value)
                with self.assertRaises(ns.ScopeViolation):
                    self.accept(value,request('graph_trace',operation='call_path'))
                self.assertFalse(self.scope.nodes)

    def test_valid_root_selector_is_registered_in_same_transaction(self):
        value=envelope();value['result']['structuredContent']['selection']={'rootNodeIds':[1]};mirror(value)
        self.accept(value);self.assertIn(1,self.scope.nodes)

    def test_independent_review_nested_identity_and_source_selector_bypasses_rejected(self):
        for mutation in ('nested_id','bool_id','conflicting_id','snippet_line','snippet_project','snippet_path'):
            with self.subTest(mutation=mutation):
                row=node()
                if mutation=='nested_id':row['id']={'outside':'private unanchored fact'}
                elif mutation=='bool_id':row['id']=True
                elif mutation=='conflicting_id':row['id']=2
                else:
                    row['snippetSelector']={'tool':'graph_source','project':'Example.Api','filePath':'src/api.cs','startLine':1}
                    field={'snippet_line':'startLine','snippet_project':'project','snippet_path':'filePath'}[mutation]
                    row['snippetSelector'][field]={'outside':'private unanchored fact'}
                value=envelope(data={'results':[row]})
                with self.assertRaises(ns.ScopeViolation):self.accept(value)
                self.assertFalse(self.scope.nodes);self.assertIsNone(self.scope.revision)

    def test_no_hit_source_free_status_is_safely_rendered(self):
        value=envelope(data={'results':[],'totalCount':0,'shownCount':0},projects=[])
        self.assertEqual(self.accept(value)['result']['structuredContent']['result']['results'],[])
        self.assertFalse(self.scope.nodes)

    def test_moedex_snapshot_and_physical_roster_checked(self):
        self.scope=ns.NativeScope(policy('moedex-index-v1'))
        value={'jsonrpc':'2.0','id':1,'result':{'structuredContent':{'repos':[{'name':'example/api'},{'name':'example/library'}]},
               '_meta':{'dev.moedex/snapshot':{'cacheable':True,'corpus_fingerprint':'f'*64}}}}
        self.accept(value,{'name':'list_repos','arguments':{}})
        value['result']['structuredContent']['repos'].append({'name':'outside'})
        with self.assertRaises(ns.ScopeViolation):self.accept(value,{'name':'list_repos','arguments':{}})
        value['result']['_meta']['dev.moedex/snapshot']['corpus_fingerprint']='e'*64
        with self.assertRaises(ns.ScopeViolation):self.accept(value,{'name':'list_repos','arguments':{}})

    def test_moedex_source_blob_and_alias_binding(self):
        self.scope=ns.NativeScope(policy('moedex-index-v1'))
        value={'jsonrpc':'2.0','id':1,'result':{'structuredContent':{'repo':'example/api','path':'src/api.cs','blob_sha':'b'*40,'content':'class Api {}'},
               '_meta':{'dev.moedex/snapshot':{'cacheable':True,'corpus_fingerprint':'f'*64}}}}
        self.accept(value,{'name':'read_source','arguments':{'repo':'Example.Api'}})
        value['result']['structuredContent']['blob_sha']='e'*40
        with self.assertRaises(ns.ScopeViolation):self.accept(value,{'name':'read_source','arguments':{'repo':'Example.Api'}})


class FakeNative:
    timeout=30
    def __init__(self,value,sse=False,malformed=False):
        self.value,self.sse,self.malformed=value,sse,malformed
        self.requests=[]
    def exchange(self,request):
        self.requests.append(request)
        value=deepcopy(self.value);value['id']=request['id']
        body=rr.canonical(value)
        if self.sse:body=b'event: message\ndata: '+body+b'\n\n'
        if self.malformed:body=b'not-json'
        return json.dumps(request,separators=(',',':')).encode(),body,{'status':200,'transport_complete':True,
               'content_type':'text/event-stream' if self.sse else 'application/json','body_bytes_observed':len(body)}
    def decode(self,body,receipt,identity):
        return rpc_response(body,receipt['content_type'],identity)


class BrokerScopeTests(unittest.TestCase):
    def setUp(self):
        self.fixture=record_fixtures.Records();self.fixture.setUp();self.addCleanup(self.fixture.tmp.cleanup)
        self.fixture.budgets={'calls':8,'response_bytes':1<<20,'assignment_seconds':30,'display_bytes':8192}
        path=self.fixture.root/'scope.json';path.write_bytes(rr.canonical(policy()))
        self.fixture.manifest.update(source_scope_policy=rr.reference(self.fixture.root,path),source_scope_review=self.fixture.proof)
        self.record=self.fixture.create()
        self.scope=ns.NativeScope(policy())

    def broker(self,value=None,**options):
        client=FakeNative(value or envelope(),**options)
        return solver.NativeBroker(client,self.record,policy()['allowed_tools'],source_scope=self.scope),client

    def handle(self,broker,params=None):
        return broker.handle({'jsonrpc':'2.0','id':1,'method':'tools/call','params':params or request()})

    def test_json_and_sse_accepted_raw_and_policy_receipts_bound(self):
        for sse in (False,True):
            broker,client=self.broker(sse=sse)
            self.assertNotIn('isError',self.handle(broker)['result'])
        report=self.fixture.report()
        self.assertFalse(report['validation_errors'])
        self.assertEqual(report['calls'],2)
        self.assertTrue(all(row['accepted'] for row in report['scope_policy_decisions']))
        self.assertGreater(report['response_bytes_observed'],0)

    def test_outside_reply_retained_charged_but_not_displayed(self):
        value=envelope(data={'results':[node(repository='outside-private')]})
        broker,client=self.broker(value)
        response=self.handle(broker)
        self.assertTrue(response['result']['isError']);self.assertNotIn('outside-private',json.dumps(response))
        report=self.fixture.report();self.assertFalse(report['validation_errors'])
        self.assertEqual(report['calls'],1);self.assertFalse(report['scope_policy_decisions'][0]['accepted'])
        self.assertTrue(report['scope_policy_decisions'][0]['native_dispatched'])
        self.assertGreater(report['response_bytes_observed'],0);self.assertFalse(self.scope.nodes)

    def test_denied_request_charges_intent_without_network_or_native_bytes(self):
        broker,client=self.broker()
        response=self.handle(broker,{'name':'rag_search','arguments':{'query':'private'}})
        self.assertTrue(response['result']['isError']);self.assertFalse(client.requests)
        report=self.fixture.report();self.assertFalse(report['validation_errors'])
        self.assertEqual(report['calls'],1);self.assertEqual(report['response_bytes_observed'],0)
        self.assertFalse(report['scope_policy_decisions'][0]['native_dispatched'])

    def test_malformed_complete_response_is_recoverable_and_retained(self):
        broker,client=self.broker(malformed=True)
        self.assertTrue(self.handle(broker)['result']['isError'])
        report=self.fixture.report();self.assertEqual(report['response_bytes_observed'],8)
        self.assertFalse(report['validation_errors'])

    def test_late_reply_retained_without_display_or_selector_registration(self):
        broker,client=self.broker()
        original=client.exchange
        def late(request):
            result=original(request);self.fixture.clock.value+=31;return result
        client.exchange=late
        with self.assertRaises(RuntimeError):self.handle(broker)
        report=self.fixture.report();self.assertGreater(report['response_bytes_observed'],0)
        self.assertFalse(self.scope.nodes);self.assertNotIn('scope_policy_decisions',report)

    def test_scope_audit_receipt_tamper_is_detected(self):
        broker,_=self.broker();self.handle(broker)
        path=next((self.record.directory/'blobs').glob('*.scope-policy'))
        value=json.loads(path.read_bytes());value['accepted']=False;path.write_bytes(rr.canonical(value))
        self.assertTrue(self.fixture.report()['validation_errors'])

    def execution(self):
        root=self.fixture.root
        (root/'prompt.txt').write_bytes(b'synthetic task')
        prompt=rr.reference(root,root/'prompt.txt')
        contract={'schema':'native-pair-v2','corpus':{'repositories':[
            {'repository':row['repository'],'commit':row['commit'],'manifest':self.fixture.proof}
            for row in policy()['projects']]},'rubric':self.fixture.proof,'protocol':self.fixture.proof,
            'tasks':[{'id':'synthetic','prompt':prompt,'atoms':['whole-task'],
                      'repositories':[row['repository'] for row in policy()['projects']]}],
            'budgets':self.fixture.budgets}
        (root/'contract.json').write_bytes(rr.canonical(contract))
        provider={'model':'synthetic','reasoning':{'effort':'high'},'tool_choice':'auto',
                  'parallel_tool_calls':False,'text':{'verbosity':'low'},'store':False,
                  'stream':True,'include':['reasoning.encrypted_content']}
        frozen={'contract':rr.reference(root,root/'contract.json'),'arm':'A',
                'isolation':{'image_sha256':'sha256:'+'a'*64},'model':{'requested_alias':'synthetic',
                'settings':{'reasoning_effort':'high','reasoning_summary':'none','provider_fields':provider}},
                'native_allowed_tools':policy()['allowed_tools'],'source_scope_policy':self.fixture.manifest['source_scope_policy'],
                'runners':{name:solver.digest(Path(solver.__file__).with_name(name).read_bytes()) for name in
                           ('isolated_solver.py','run_record.py','native_http.py','journey_clock.py','contract.py','compare.py','native_scope.py')}}
        (root/'execution-freeze.json').write_bytes(rr.canonical(frozen))
        config={'freeze':rr.reference(root,root/'execution-freeze.json'),'identity':{'task':'synthetic','arm':'A',
                'contract_sha256':solver.digest(rr.canonical(contract)),'prompt_sha256':prompt['sha256']},
                'model':'synthetic','reasoning_effort':'high','image':frozen['isolation']['image_sha256'],
                'prompt':'synthetic task','budgets':self.fixture.budgets,'enabled_tools':policy()['allowed_tools'],
                'timeout_seconds':self.fixture.budgets['assignment_seconds'],'source_scope_policy':frozen['source_scope_policy']}
        return config,frozen

    def test_execution_policy_is_frozen_and_assignment_context_is_fresh(self):
        config,frozen=self.execution();solver.validate_execution_config(self.fixture.root,config)
        first=config['_source_scope'];first.accept(envelope(),first.prepare(request()))
        solver.validate_execution_config(self.fixture.root,config)
        self.assertIsNot(config['_source_scope'],first);self.assertFalse(config['_source_scope'].nodes)
        del config['source_scope_policy']
        with self.assertRaises(ValueError):solver.validate_execution_config(self.fixture.root,config)

    def test_execution_scope_cannot_widen_pins_roster_or_tools(self):
        config,frozen=self.execution()
        for mutation in ('pin','roster','tools'):
            value=policy()
            if mutation=='pin':value['projects'][0]['commit']='e'*40
            elif mutation=='roster':value['projects'].pop()
            else:value['allowed_tools'].pop()
            path=self.fixture.root/('mutated-'+mutation+'.json');path.write_bytes(rr.canonical(value))
            altered=deepcopy(frozen);altered['source_scope_policy']=rr.reference(self.fixture.root,path)
            freeze=self.fixture.root/('freeze-mutated-'+mutation+'.json');freeze.write_bytes(rr.canonical(altered))
            changed=deepcopy(config);changed['freeze']=rr.reference(self.fixture.root,freeze)
            changed['source_scope_policy']=altered['source_scope_policy']
            with self.assertRaises(ValueError):solver.validate_execution_config(self.fixture.root,changed)

    def test_execution_requires_exact_guard_runner_hash(self):
        config,frozen=self.execution();frozen['runners']['native_scope.py']='e'*64
        path=self.fixture.root/'execution-freeze.json';path.write_bytes(rr.canonical(frozen))
        config['freeze']=rr.reference(self.fixture.root,path)
        with self.assertRaisesRegex(ValueError,'native_scope.py'):solver.validate_execution_config(self.fixture.root,config)

    def test_full_background_scope_remains_available_to_subset_target_task(self):
        config,frozen=self.execution()
        contract_path=self.fixture.root/'contract.json'
        contract=json.loads(contract_path.read_bytes())
        contract['tasks'][0]['repositories']=['example/api']
        contract_path.write_bytes(rr.canonical(contract))
        frozen['contract']=rr.reference(self.fixture.root,contract_path)
        freeze_path=self.fixture.root/'execution-freeze.json';freeze_path.write_bytes(rr.canonical(frozen))
        config['freeze']=rr.reference(self.fixture.root,freeze_path)
        config['identity']['contract_sha256']=solver.digest(rr.canonical(contract))
        solver.validate_execution_config(self.fixture.root,config)
        self.assertEqual(set(config['_source_scope'].projects), {'example/api','example/library'})


if __name__=='__main__':unittest.main()
