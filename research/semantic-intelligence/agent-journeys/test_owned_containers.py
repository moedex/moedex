"""Neutral exact-CID closure, including an optional real isolated Docker probe."""
import json,os,subprocess,tempfile,time,unittest,uuid
from pathlib import Path
from types import SimpleNamespace
from owned_containers import CapturedContainerCleanup

class ContainerTests(unittest.TestCase):
    cid='a'*64
    def setup_root(self,root,cid=None):
        p=root/'captures/slot';p.mkdir(parents=True)
        if cid is not None:(p/'container.id').write_text(cid)
        return p
    def fake(self,outputs,calls):
        def run(args,**kwargs):
            calls.append(args);code,body=outputs.pop(0);return SimpleNamespace(returncode=code,stdout=body)
        return run
    def test_exact_owned_running_container_removed_and_verified(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);self.setup_root(root,self.cid);calls=[]
            cleanup=CapturedContainerCleanup(root,run=self.fake([(0,self.cid.encode()+b'\n'),(0,b''),(0,b'')],calls))
            self.assertTrue(cleanup.close('slot'))
            self.assertEqual(calls[1],['docker','container','rm','--force',self.cid])
            self.assertEqual(calls[0][6],'id='+self.cid)
    def test_already_absent_never_removes_any_container(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);self.setup_root(root,self.cid);calls=[]
            self.assertTrue(CapturedContainerCleanup(root,run=self.fake([(0,b''),(0,b'')],calls)).close('slot'))
            self.assertTrue(all('rm' not in call for call in calls))
    def test_missing_id_after_command_intent_is_unknown(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);p=self.setup_root(root);(p/'container-command.json').write_text('[]')
            self.assertFalse(CapturedContainerCleanup(root,run=lambda *a,**k:self.fail('Docker must not be guessed')).close('slot'))
    def test_missing_capture_without_id_is_still_unknown(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);self.setup_root(root)
            self.assertFalse(CapturedContainerCleanup(root,run=lambda *a,**k:self.fail('No CID')).close('slot'))
    def test_invalid_symlink_and_fifo_ids_never_signal(self):
        for kind in ('invalid','symlink','fifo'):
            with self.subTest(kind=kind),tempfile.TemporaryDirectory() as d:
                root=Path(d);p=self.setup_root(root);file=p/'container.id'
                if kind=='invalid':file.write_text('other-container')
                elif kind=='fifo':os.mkfifo(file)
                else:
                    (root/'unrelated').write_text(self.cid);file.symlink_to(root/'unrelated')
                self.assertFalse(CapturedContainerCleanup(root,run=lambda *a,**k:self.fail('Invalid ownership')).close('slot'))
    def test_ambiguous_or_unavailable_docker_status_cannot_close(self):
        for code,body in ((1,b''),(0,b'other-id\n'),(0,(self.cid+'\n'+self.cid+'\n').encode())):
            with self.subTest(code=code),tempfile.TemporaryDirectory() as d:
                root=Path(d);self.setup_root(root,self.cid);calls=[]
                self.assertFalse(CapturedContainerCleanup(root,run=self.fake([(code,body)],calls)).close('slot'))
                self.assertEqual(len(calls),1)
    def test_physical_cleanup_precedes_failed_receipt_storage(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);self.setup_root(root,self.cid);p=root/'runtime/containers';p.mkdir(parents=True);(p/'slot.json').write_text('existing')
            calls=[];cleanup=CapturedContainerCleanup(root,run=self.fake([(0,self.cid.encode()),(0,b''),(0,b'')],calls))
            with self.assertRaises(FileExistsError):cleanup.close('slot')
            self.assertIn(['docker','container','rm','--force',self.cid],calls)

    def test_symlink_archive_root_is_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);(root/'actual').mkdir();(root/'alias').symlink_to(root/'actual')
            with self.assertRaises(OSError):CapturedContainerCleanup(root/'alias')

    def test_symlink_receipt_parent_never_writes_outside_archive(self):
        for parent in ('runtime','containers'):
            with self.subTest(parent=parent),tempfile.TemporaryDirectory() as d:
                root=Path(d);self.setup_root(root,self.cid);outside=root/'outside';outside.mkdir()
                if parent=='runtime':(root/'runtime').symlink_to(outside)
                else:
                    (root/'runtime').mkdir();(root/'runtime/containers').symlink_to(outside)
                calls=[];cleanup=CapturedContainerCleanup(root,run=self.fake([(0,self.cid.encode()),(0,b''),(0,b'')],calls))
                with self.assertRaises(OSError):cleanup.close('slot')
                self.assertEqual(list(outside.iterdir()),[])
                self.assertIn(['docker','container','rm','--force',self.cid],calls)

    @unittest.skipUnless(os.environ.get('MOEDEX_TEST_DOCKER_IMAGE'),'exact neutral Docker image not configured')
    def test_docker_container_survives_cli_death_then_exact_id_cleanup(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);capture=self.setup_root(root);(capture/'container-command.json').write_text('[]')
            name='moedex-neutral-'+uuid.uuid4().hex
            process=subprocess.Popen(['docker','run','--name',name,'--rm','--cidfile',str(capture/'container.id'),'--network','none','--read-only','--entrypoint','node',os.environ['MOEDEX_TEST_DOCKER_IMAGE'],'-e','setInterval(()=>{},1000)'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,start_new_session=True)
            cleanup=CapturedContainerCleanup(root)
            try:
                deadline=time.monotonic()+10
                while time.monotonic()<deadline:
                    if (capture/'container.id').exists() and len((capture/'container.id').read_text().strip())==64:break
                    time.sleep(.02)
                self.assertTrue((capture/'container.id').exists())
                process.kill();process.wait(timeout=5)
                self.assertTrue(cleanup.close('slot'))
                receipt=json.loads((root/'runtime/containers/slot.json').read_bytes());self.assertTrue(receipt['closed'])
            finally:
                if process.poll() is None:process.kill();process.wait(timeout=5)
                if not (root/'runtime/containers/slot.json').exists():cleanup.close('slot')
                subprocess.run(['docker','container','rm','--force',name],capture_output=True,timeout=10)

if __name__=='__main__':unittest.main()
