"""Close exact host-captured Docker IDs after runner termination.

Local process-group cleanup does not stop a daemon-managed container. The caller
must control the capture tree and bind the code that writes its CID before use.
"""
import json
import os
from pathlib import Path
import re
import stat
import subprocess

NAME=re.compile(r'^[A-Za-z0-9][A-Za-z0-9_.-]*$')
CID=re.compile(r'^[0-9a-f]{64}$')


class CapturedContainerCleanup:
    def __init__(self,archive_root,*,run=subprocess.run,env=None,timeout_seconds=10):
        if not callable(run) or type(timeout_seconds) not in (int,float) or not 0<timeout_seconds<=60:
            raise ValueError('bounded container cleanup required')
        selected=Path(os.path.abspath(archive_root))
        self.root=selected.parent.resolve()/selected.name
        self.run,self.env,self.timeout=run,env,timeout_seconds
        # Anchor every directory component without following symlinks.
        fd=self._directory(())
        os.close(fd)

    def _directory(self,parts,*,create=False):
        flags=os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW
        fd=os.open('/',flags)
        try:
            components=(*self.root.parts[1:],*parts)
            for index,component in enumerate(components):
                if create and index>=len(self.root.parts)-1:
                    try:os.mkdir(component,0o700,dir_fd=fd)
                    except FileExistsError:pass
                child=os.open(component,flags,dir_fd=fd)
                os.close(fd);fd=child
            return fd
        except BaseException:
            os.close(fd)
            raise

    def _call(self,args):
        result=self.run(['docker','container',*args],env=self.env,capture_output=True,timeout=self.timeout)
        if type(result.returncode) is not int or len(result.stdout)>4096:
            raise ValueError('invalid container status')
        return result

    def _exists(self,cid):
        result=self._call(['ls','-a','--no-trunc','--filter','id='+cid,'--format','{{.ID}}'])
        if result.returncode!=0:raise ValueError('container status unknown')
        ids=result.stdout.decode('ascii').splitlines()
        if ids not in ([],[cid]):raise ValueError('ambiguous container status')
        return bool(ids)

    def close(self,assignment):
        if type(assignment) is not str or not NAME.fullmatch(assignment):
            raise ValueError('confined container assignment required')
        cid=None;closed=False;reason='container_status_unknown';removed=None
        try:
            directory=self._directory(('captures',assignment))
            try:
                fd=os.open('container.id',os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=directory)
            finally:os.close(directory)
            try:
                info=os.fstat(fd)
                if not stat.S_ISREG(info.st_mode) or info.st_size>65:raise ValueError('container identity invalid')
                raw=os.read(fd,66)
            finally:os.close(fd)
            cid=raw.decode('ascii').strip()
            if not CID.fullmatch(cid):raise ValueError('container identity invalid')
            if self._exists(cid):
                removed=self._call(['rm','--force',cid]).returncode
            closed=not self._exists(cid)
            reason='container_absent' if closed else 'container_still_present'
        except (OSError,ValueError,UnicodeError,subprocess.TimeoutExpired):
            reason='container_status_unknown';closed=False
        # Physical cleanup precedes receipt I/O. No argv/env/stderr is persisted.
        receipt={'schema':'owned-captured-container-closure-v1','assignment':assignment,
                 'container_id':cid,'closed':closed,'reason':reason,'remove_exit_code':removed,
                 'limits':'CID provenance relies on trusted host capture writer; no global Docker cleanup. Missing CID after command intent is unknown, never inferred closed.'}
        directory=self._directory(('runtime','containers'),create=True)
        try:fd=os.open(assignment+'.json',os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=directory)
        finally:os.close(directory)
        with os.fdopen(fd,'wb') as output:
            output.write(json.dumps(receipt,sort_keys=True,separators=(',',':')).encode()+b'\n');output.flush();os.fsync(output.fileno())
        return closed
