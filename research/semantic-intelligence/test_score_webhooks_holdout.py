import importlib.util,json,pathlib,unittest
s=importlib.util.spec_from_file_location('holdout',pathlib.Path(__file__).with_name('score-webhooks-holdout.py'));m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
class GoldMapping(unittest.TestCase):
 def test_frozen_labels_keep_distinct_denominators(self):
  p=pathlib.Path(__file__).parent/'results/eshop-webhooks-holdout-20261001/source-gold-v2.json';raw=p.read_bytes();self.assertEqual(m.native.digest(raw),m.GOLD_SHA);g=json.loads(raw);q=m.protocol_for(g)
  self.assertEqual(len(q['positive_cases']),9);self.assertEqual(len(q['negative_cases']),0)
  self.assertEqual(sum(c['classification']=='unsupported' for c in g['cases']),5)
  self.assertEqual(sum(c['classification']=='negative' for c in g['cases']),2)
  self.assertEqual(sum(c['family']=='method_implementation' for c in q['positive_cases']),3)
  self.assertEqual(sum(c['family']=='resolved_call' for c in q['positive_cases']),2)
  self.assertTrue(all(set(c['source'])=={'project','path','byte_offset','byte_length','source_sha256'} for c in q['positive_cases']))
 def test_precapture_successor_changes_only_owner_case_field(self):
  base=pathlib.Path(__file__).parent/'results/eshop-webhooks-holdout-20261001'
  raw=(base/'source-gold.json').read_bytes();self.assertEqual(m.native.digest(raw),'4e3973be640d9a0d126f5156a8127d9253d5e980eca7bfc1a2bcfe8f6f28f455')
  old=json.loads(raw);new=json.loads((base/'source-gold-v2.json').read_bytes())
  self.assertEqual(new['predecessor_sha256'],m.native.digest(raw));self.assertEqual(old['reviewed_closure'],new['reviewed_closure'])
  for c in old['cases']:
   if c['id']=='subscriptions-entity':c['owner']['descriptor']='T:Webhooks.API.Infrastructure.WebhooksContext'
  self.assertEqual(old['cases'],new['cases'])
 def test_exact_owner_required_for_di(self):
  c={'id':'di','family':'domain','classification':'supported','kind':'di_registration','rule':'csharp-framework-v1','source':{'project':'P','path':'A','byte_offset':1,'byte_length':2,'source_sha256':'sha','anchor':'xx'},'owner':{'descriptor':'M:Owner'},'targets':[],'lifetime':'transient'}
  gold={'classification':'holdout','cases':[c],'reviewed_closure':{'source_sha256':{'A':'sha'}}};want=m.protocol_for(gold)['positive_cases'][0]
  r={'source':want['source'],'status':'resolved','owner':{'descriptor':'M:Wrong'},'domain_facts':[m.native.expected_fact(want)[1]]}
  self.assertFalse(m.native.matches(want,r))
if __name__=='__main__':unittest.main()
