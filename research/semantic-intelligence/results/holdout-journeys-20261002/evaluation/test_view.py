import hashlib,json,subprocess,sys,tempfile,time,unittest
from pathlib import Path
VIEW=Path(__file__).with_name('view.py').resolve()
class Tests(unittest.TestCase):
 def invoke(self,run,args):return subprocess.run([sys.executable,'-B',str(VIEW),'--run-dir',str(run),*args],capture_output=True)
 def test_recording_and_deadline(self):
  with tempfile.TemporaryDirectory() as tmp:
   p=Path(tmp);(p/'catalog.json').write_text('{"tools":[{"name":"x"}]}');(p/'assignment.json').write_text(json.dumps({'deadline_monotonic':time.monotonic()+60}))
   r=self.invoke(p,['catalog']);self.assertEqual(r.returncode,0);self.assertLessEqual(len(r.stdout),8192);self.assertEqual(r.stdout,(p/'views/001.stdout').read_bytes());d=json.loads((p/'views/001.json').read_text());self.assertEqual(d['stdout_sha256'],hashlib.sha256(r.stdout).hexdigest())
   (p/'assignment.json').write_text(json.dumps({'deadline_monotonic':time.monotonic()-1}))
   r=self.invoke(p,['catalog']);self.assertEqual(r.returncode,2);self.assertIn(b'deadline',r.stderr);self.assertEqual(r.stderr,(p/'views/002.stderr').read_bytes())
 def test_invalid_selection_and_cap(self):
  with tempfile.TemporaryDirectory() as tmp:
   p=Path(tmp);(p/'catalog.json').write_text('{"tools":[]}');(p/'assignment.json').write_text(json.dumps({'deadline_monotonic':time.monotonic()+60}))
   r=self.invoke(p,['catalog','--tool','missing']);self.assertEqual(r.returncode,2);self.assertIn(b'view_error',r.stderr)
   r=self.invoke(p,['--max-bytes','16384','catalog']);self.assertEqual(r.returncode,2);self.assertIn(b'frozen',r.stderr)
if __name__=='__main__':unittest.main()
