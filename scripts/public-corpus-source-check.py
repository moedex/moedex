#!/usr/bin/env python3
"""Read-only raw-source and CSV evidence checks for the pinned public gate."""
import csv
import hashlib
import os
from pathlib import Path
import stat
import subprocess
import sys


def check_source(root):
    def git(*args):
        return subprocess.check_output(['git', '-C', str(root), *args],
                                       env=dict(os.environ, GIT_NO_REPLACE_OBJECTS='1'))

    tracked = git('ls-files', '-v', '-z').split(b'\0')
    paths = set()
    for entry in filter(None, tracked):
        if not entry.startswith(b'H '):
            raise ValueError('hidden or unsupported index flags: ' + os.fsdecode(entry))
        paths.add(entry[2:])
    tree = git('ls-tree', '-rz', '--full-tree', 'HEAD').split(b'\0')
    roster = hashlib.sha256()
    tree_paths = set()
    for entry in filter(None, tree):
        metadata, name = entry.split(b'\t', 1)
        mode, kind, oid = metadata.split()
        if mode not in (b'100644', b'100755') or kind != b'blob':
            raise ValueError('nonregular tracked entry: ' + os.fsdecode(name))
        tree_paths.add(name)
        path = root / os.fsdecode(name)
        if not path.resolve().is_relative_to(root.resolve()) or not stat.S_ISREG(path.lstat().st_mode):
            raise ValueError('nonregular or escaping source: ' + os.fsdecode(name))
        raw = path.read_bytes()
        # Hash Git's raw blob representation directly, without invoking any
        # repository clean/smudge filter or trusting stat/index caches.
        algorithm = 'sha1' if len(oid) == 40 else 'sha256' if len(oid) == 64 else None
        if algorithm is None:
            raise ValueError('unsupported Git object ID')
        actual = hashlib.new(algorithm, b'blob ' + str(len(raw)).encode() + b'\0' + raw).hexdigest().encode()
        if actual != oid:
            raise ValueError('raw source differs from HEAD: ' + os.fsdecode(name))
        roster.update(name + b'\0' + hashlib.sha256(raw).digest())
    if not tree_paths or paths != tree_paths:
        raise ValueError('tracked index scope differs from HEAD tree')
    print('source_roster_sha256=' + roster.hexdigest())


def check_latency(path):
    header = 'query_id bucket literal ignorecase nmoe nanos candidate_kind candidate_blobs candidate_bytes candidate_lines all_candidates query_all line_filter_kind lines_entering_re2 verify_workers pattern'.split()
    with path.open(newline='') as stream:
        rows = list(csv.reader(stream, strict=True))
    if not rows or rows[0] != header or len(rows) != 1053:
        raise ValueError('latency CSV must have the expected header and 1052 data rows')
    if any(len(row) != len(header) for row in rows[1:]):
        raise ValueError('incomplete latency CSV row')
    if {int(row[0]) for row in rows[1:]} != set(range(1052)):
        raise ValueError('latency CSV query IDs must cover 0..1051 exactly')
    if any(int(row[5]) < 0 for row in rows[1:]):
        raise ValueError('invalid latency duration')
    print('latency_rows=1052')


if __name__ == '__main__':
    try:
        if len(sys.argv) == 3 and sys.argv[1] == '--latency':
            check_latency(Path(sys.argv[2]))
        elif len(sys.argv) == 2:
            check_source(Path(sys.argv[1]))
        else:
            raise ValueError('expected CHECKOUT or --latency CSV')
    except (OSError, ValueError, csv.Error, subprocess.CalledProcessError) as error:
        print('public-corpus-evidence: ' + str(error), file=sys.stderr)
        sys.exit(2)
