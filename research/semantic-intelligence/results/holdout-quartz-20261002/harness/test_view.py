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

class OnboardingTests(unittest.TestCase):
 def test_instruction_pages_are_logged_and_lossless(self):
  with tempfile.TemporaryDirectory() as tmp:
   run=Path(tmp)
   instructions='Read all source evidence before citing it. '*800
   (run/'initialize.json').write_text(json.dumps({'result':{'instructions':instructions}}))
   (run/'assignment.json').write_text(json.dumps({'deadline_monotonic':time.monotonic()+60}))
   offset=0; pieces=[]; ordinal=0
   while True:
    result=subprocess.run([sys.executable,'-B',str(VIEW),'--run-dir',str(run),'--offset',str(offset),'instructions'],capture_output=True)
    self.assertEqual(result.returncode,0,result.stderr)
    ordinal+=1
    self.assertEqual(result.stdout,(run/f'views/{ordinal:03}.stdout').read_bytes())
    self.assertLessEqual(len(result.stdout),8192)
    page=json.loads(result.stdout)
    pieces.append(page['data'])
    offset=page['next_offset']
    if offset is None:break
   self.assertEqual(json.loads(''.join(pieces))['instructions'],instructions)
   self.assertGreater(ordinal,1)
 def test_abbreviated_cap_override_is_rejected(self):
  with tempfile.TemporaryDirectory() as tmp:
   run=Path(tmp);(run/'assignment.json').write_text(json.dumps({'deadline_monotonic':time.monotonic()+60}))
   for flag in ('--max','--max-b=16384'):
    result=subprocess.run([sys.executable,'-B',str(VIEW),'--run-dir',str(run),flag,'catalog'],capture_output=True)
    self.assertEqual(result.returncode,2)
    self.assertIn(b'cap is frozen',result.stderr)

if __name__=='__main__':unittest.main()
