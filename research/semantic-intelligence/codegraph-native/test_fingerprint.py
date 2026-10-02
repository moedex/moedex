import copy,tempfile,unittest
from pathlib import Path
from fingerprint import roster,verify

class FingerprintTests(unittest.TestCase):
 def test_changes_additions_missing_and_generated_exclusion(self):
  with tempfile.TemporaryDirectory() as d:
   root=Path(d);(root/'src/P/obj').mkdir(parents=True);p=root/'src/P/A.cs';p.write_text('class A {}');(root/'src/P/obj/generated.cs').write_text('generated');frozen=roster(root)
   self.assertEqual(len(frozen['files']),1);self.assertTrue(verify(frozen,roster(root))['matches'])
   p.write_text('class B {}');self.assertEqual(verify(frozen,roster(root))['changed'],['src/P/A.cs'])
   p.unlink();(root/'src/P/B.cs').write_text('class B {}');r=verify(frozen,roster(root));self.assertEqual(r['missing'],['src/P/A.cs']);self.assertEqual(r['added'],['src/P/B.cs'])
 def test_symlink_not_followed(self):
  with tempfile.TemporaryDirectory() as d:
   root=Path(d);(root/'src').mkdir();(root/'elsewhere').write_text('x');(root/'src/x').symlink_to(root/'elsewhere')
   with self.assertRaises(ValueError):roster(root)
 def test_rejects_scope_change_and_duplicate(self):
  a={'schema':'x','scope':'x','files':[{'path':'x'}]};b=copy.deepcopy(a);b['scope']='y'
  with self.assertRaises(ValueError):verify(a,b)
  b=copy.deepcopy(a);b['files']*=2
  with self.assertRaises(ValueError):verify(a,b)
if __name__=='__main__':unittest.main()
