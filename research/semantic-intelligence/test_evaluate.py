import tempfile
import unittest
from pathlib import Path
import evaluate


class GoldEvaluationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root/'a.cs').write_text('// 😀 café\nclass Thing {}\nThing value;\n')
        self.target = {'file':'a.cs','line':2,'text':'Thing'}
        self.source = {'file':'a.cs','line':3,'text':'Thing'}
        self.gold = {'references':[{'id':'ref','project':'A','source':self.source,'status':'resolved',
            'target':{'project':'A','anchor':self.target}}], 'identity_groups':[]}

    def observation(self, anchor, kind, symbol):
        start,length = evaluate.anchor_span(self.root,anchor)
        return {'file':'a.cs','byte_offset':start,'byte_length':length,'kind':kind,'symbol':symbol,'status':'resolved'}

    def test_missing_both_sides_is_not_a_success(self):
        self.assertFalse(evaluate.evaluate(self.root,self.gold,{})['references'][0]['pass'])

    def test_reference_must_match_target_exactly(self):
        definition = self.observation(self.target,'declaration','right')
        reference = self.observation(self.source,'reference','wrong')
        self.assertFalse(evaluate.evaluate(self.root,self.gold,{'A':[definition,reference]})['references'][0]['pass'])
        reference['symbol']='right'
        self.assertTrue(evaluate.evaluate(self.root,self.gold,{'A':[definition,reference]})['references'][0]['pass'])

    def test_utf8_anchor_is_byte_offset(self):
        offset,length=evaluate.anchor_span(self.root,self.target)
        self.assertEqual((self.root/'a.cs').read_bytes()[offset:offset+length],b'Thing')
        self.assertEqual(offset,len('// 😀 café\nclass '.encode()))

    def test_missing_diagnostic_not_unresolved_evidence(self):
        self.gold['references'][0]['status']='unresolved'
        self.assertFalse(evaluate.evaluate(self.root,self.gold,{})['references'][0]['pass'])
        observation=self.observation(self.source,'reference','')
        observation['status']='unresolved'
        self.assertTrue(evaluate.evaluate(self.root,self.gold,{'A':[observation]})['references'][0]['pass'])

    def test_missing_project_is_not_inactive_evidence(self):
        self.gold['references'][0]['status']='inactive'
        self.assertFalse(evaluate.evaluate(self.root,self.gold,{})['references'][0]['pass'])

    def test_same_symbol_group_requires_every_declaration(self):
        self.gold['identity_groups']=[{'id':'same','relation':'same_symbol','declarations':[
            {'project':'A','anchor':self.target},{'project':'B','anchor':self.target}]}]
        result=evaluate.evaluate(self.root,self.gold,{'A':[self.observation(self.target,'declaration','id')]})
        self.assertFalse(result['identities'][0]['pass'])

if __name__ == '__main__':
    unittest.main()
