import importlib.util,pathlib,tempfile,unittest
s=importlib.util.spec_from_file_location('v2',pathlib.Path(__file__).with_name('score-codegraph-component-v2.py'));m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
class EFPartial(unittest.TestCase):
 def test_only_attributable_exact_api_is_partial(self):
  with tempfile.TemporaryDirectory() as d:
   root=pathlib.Path(d);(root/'A.cs').write_text('class A {}')
   source={'project':'P.csproj','path':'A.cs','byte_offset':0,'byte_length':1}
   case={'id':'ef','family':'domain_observation','expected_kind':'storage_entity_use','owner':{'descriptor':'M:N.A.Run'},'source':source,'expected_targets':[{'symbol':{'descriptor':'T:N.Entity'}}]}
   protocol={'reviewed_source_closure':{'source_sha256':{'A.cs':m.initial.sha(root/'A.cs')}},'positive_cases':[case],'negative_cases':[]}
   n={'qualifiedName':'N.A.Run','dotnetProject':'P','filePath':'A.cs'};e={'sourceQN':'N.A.Run','targetQN':'Microsoft.EntityFrameworkCore.DbContext.Set<N.Entity>()','type':'CALLS','properties':{'confidence':1}}
   raw=[{'nodes':[n],'edges':[e]}]
   self.assertEqual(m.score(protocol,raw,root)['rows'][0]['outcome'],'partial_native_evidence')
   e['sourceQN']='N.Other.Run';self.assertEqual(m.score(protocol,raw,root)['rows'][0]['outcome'],'not_represented')
   e['sourceQN']='N.A.Run';e['targetQN']='Other.DbContext.Set<N.Entity>()';self.assertEqual(m.score(protocol,raw,root)['rows'][0]['outcome'],'not_represented')
if __name__=='__main__':unittest.main()
