import copy
import hashlib
import importlib.util
import pathlib
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('scorer',pathlib.Path(__file__).with_name('score-codegraph-component.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class Scoring(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=pathlib.Path(self.temp.name)
  (self.root/'A.cs').write_text('class A {}\n')
  source={'project':'P.csproj','path':'A.cs','byte_offset':0,'byte_length':5,'source_sha256':m.sha(self.root/'A.cs')}
  self.source=source
  self.key=lambda desc:{'descriptor':desc}
  self.case={'id':'call','family':'resolved_call','source':source,'owner':self.key('M:N.A.Call(System.String)'),'target':self.key('M:N.I.Send(System.String)')}
  self.protocol={'reviewed_source_closure':{'source_sha256':{'A.cs':source['source_sha256']}},'positive_cases':[self.case],'negative_cases':[]}
  self.node={'qualifiedName':'N.A.Call(string)','dotnetProject':'P','filePath':'A.cs','startLine':1,'endLine':1,'label':'Method'}
  self.edge={'sourceQN':'N.A.Call(string)','targetQN':'N.I.Send(string)','type':'CALLS','properties':{'confidence':1.0}}
  self.raw=[{'nodes':[self.node],'edges':[self.edge]}]
 def result(self):return m.score(self.protocol,self.raw,self.root)['rows'][0]
 def test_exact_identity_is_only_owner_level(self):
  self.assertEqual(self.result()['outcome'],'supported_owner_level')
  self.assertIn('no_native_edge_callsite',self.result()['provenance'])
 def test_overload_and_namespace_never_erased(self):
  for value in ['N.I.Send(decimal)','Other.I.Send(string)']:
   self.edge['targetQN']=value;self.assertEqual(self.result()['outcome'],'not_represented')
 def test_wrong_project_not_repaired(self):
  self.node['dotnetProject']='Other';self.assertEqual(self.result()['outcome'],'mapping_unavailable')
 def test_target_project_collision_stays_ambiguous(self):
  self.raw[0]['nodes'] += [{'qualifiedName':'N.I.Send(string)','dotnetProject':'First'},{'qualifiedName':'N.I.Send(string)','dotnetProject':'Second'}]
  self.assertEqual(self.result()['outcome'],'ambiguous_identity')
 def test_unresolved_call_not_promoted(self):
  self.raw[0]['edges']=[];self.raw[0]['unresolvedCalls']=[{'callerQN':'N.A.Call(string)','calleeName':'Send','confidence':0.8}]
  self.assertEqual(self.result()['outcome'],'not_represented')
 def test_type_implementation_is_partial(self):
  self.protocol['positive_cases']=[{'id':'implementation','family':'method_implementation','source':self.source,'interface_member':self.key('M:N.I.Send(System.String)'),'implementing_type':self.key('T:N.A'),'implementation_method':self.key('M:N.A.Send(System.String)')}]
  self.node['qualifiedName']='N.A';self.edge.update(sourceQN='N.A',targetQN='N.I',type='IMPLEMENTS')
  self.assertEqual(self.result()['outcome'],'partial_native_evidence')
 def test_node_range_is_not_exact_callsite(self):
  self.protocol['negative_cases']=[{'id':'log-call','source':self.source}]
  r=m.score(self.protocol,self.raw,self.root);self.assertEqual(r['negative_controls'][0]['outcome'],'attribution_unavailable')
 def test_same_owner_wrong_message_is_not_negative_violation(self):
  self.protocol['negative_cases']=[{'id':'wrapper','source':self.source,'caller_owner':self.case['owner'],'message':self.key('T:N.Message')}]
  self.edge.update(type='PUBLISHES',targetQN='Other.Message')
  r=m.score(self.protocol,self.raw,self.root);self.assertEqual(r['negative_controls'][0]['outcome'],'no_forbidden_assertion_observed')
 def test_scoped_service_assertion_retains_line_only(self):
  self.protocol['positive_cases']=[{'id':'di','family':'domain_observation','source':self.source,'expected_kind':'di_registration','expected_targets':[{'role':'service','symbol':self.key('T:N.I')},{'role':'implementation','symbol':self.key('T:N.A')}],'expected_lifetime':'scoped'}]
  self.node.update(label='Service',properties={'interface':'N.I','implementation':'N.A','lifetime':'Scoped'})
  self.assertEqual(self.result()['outcome'],'supported_owner_level')
  self.node['properties']['lifetime']='Singleton';self.assertEqual(self.result()['outcome'],'wrong_assertion')
if __name__=='__main__':unittest.main()
