"""Neutral launcher limits, filesystem scope and no-follow failure behavior."""
from collections import namedtuple
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import execution_guard as guard


Usage = namedtuple('Usage', 'total used free')


class Clock:
    def __init__(self, now=100):
        self.now = now

    def __call__(self):
        return self.now


@unittest.skipUnless(os.name == 'posix', 'POSIX descriptor traversal required')
class ExecutionGuardTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='execution-guard-neutral-')
        self.root = Path(self.tmp.name) / 'archive'
        self.root.mkdir()
        self.clock = Clock()
        self.free = 10000
        self.disk_calls = []

    def tearDown(self):
        self.tmp.cleanup()

    def disk_usage(self, fd):
        self.assertIs(type(fd), int)
        self.disk_calls.append(os.fstat(fd).st_ino)
        return Usage(20000, 20000 - self.free, self.free)

    def create(self, **kwargs):
        options = dict(max_wall_seconds=20, max_archive_bytes=1000,
                       min_free_bytes=100, scan_interval_seconds=5,
                       clock=self.clock, disk_usage=self.disk_usage)
        options.update(kwargs)
        return guard.ExecutionGuard(self.root, **options)

    def write_size(self, path, size):
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open('wb') as stream:
            stream.truncate(size)

    def stopped(self, instance, reason):
        with self.assertRaises(guard.RunLimitExceeded) as raised:
            instance.check()
        self.assertEqual(raised.exception.reason, reason)
        self.assertEqual(str(raised.exception), reason)
        self.assertEqual(instance.failure_reason, reason)
        return raised.exception

    def test_deadline_includes_startup_and_is_reused_across_checks(self):
        instance = self.create()
        self.assertEqual((instance.started_at, instance.deadline), (100, 120))
        self.clock.now = 119
        instance.check()
        self.clock.now = 120
        self.stopped(instance, 'wall_limit')
        self.assertEqual(instance.deadline, 120)
        self.assertEqual(len(self.disk_calls), 1)

    def test_wall_stop_precedes_first_filesystem_poll_after_long_startup(self):
        instance = self.create()
        self.clock.now = 121
        self.stopped(instance, 'wall_limit')
        self.assertEqual(self.disk_calls, [])
        self.assertIsNone(instance.archive_bytes)

    def test_wall_is_checked_on_calls_between_periodic_scans(self):
        instance = self.create(scan_interval_seconds=100)
        instance.check()
        self.clock.now = 119
        instance.check()
        self.clock.now = 120
        self.stopped(instance, 'wall_limit')
        self.assertEqual(len(self.disk_calls), 1)

    def test_wall_is_checked_again_after_filesystem_work(self):
        def slow_disk(fd):
            self.clock.now = 120
            return self.disk_usage(fd)
        instance = self.create(disk_usage=slow_disk)
        self.stopped(instance, 'wall_limit')

    def test_cumulative_growth_across_controlled_trees_reaches_exact_threshold(self):
        self.write_size(self.root/'captures'/'one'/'body', 200)
        self.write_size(self.root/'execution'/'intent', 200)
        self.write_size(self.root/'runtime'/'daemon.log', 200)
        self.write_size(self.root/'assignments'/'one'/'receipt', 200)
        instance = self.create()
        instance.check()
        self.assertEqual(instance.archive_bytes, 800)
        self.write_size(self.root/'captures'/'two'/'body', 200)
        self.clock.now = 104
        instance.check()
        self.assertEqual(instance.archive_bytes, 800)
        self.clock.now = 105
        self.stopped(instance, 'archive_limit')
        self.assertEqual(instance.archive_bytes, 1000)

    def test_legacy_assignment_marker_counts_all_retained_files(self):
        run = self.root/'independent-run'
        self.write_size(run/'assignment.json', 4)
        self.write_size(run/'blobs'/'body', 100)
        instance = self.create()
        instance.check()
        self.assertEqual(instance.archive_bytes, 104)

    def test_scan_scope_never_traverses_corpus_index_or_dependencies(self):
        names = ['corpus', 'shared-corpus', 'source', 'sources', 'dependencies',
                 'vendor', 'node_modules', 'code', 'runners', 'tools', 'evidence',
                 'index-large', 'unmarked-background']
        for name in names:
            self.write_size(self.root/name/'nested'/'ignored', 10000)
            if name != 'unmarked-background':
                self.write_size(self.root/name/'assignment.json', 1)
        self.write_size(self.root/'ignored-root-file', 10000)
        self.write_size(self.root/'captures'/'retained', 10)
        real_scandir = os.scandir
        opened = []
        def observed_scandir(fd):
            opened.append(os.fstat(fd).st_ino)
            return real_scandir(fd)
        with patch.object(guard.os, 'scandir', side_effect=observed_scandir):
            instance = self.create()
            instance.check()
        self.assertEqual(instance.archive_bytes, 10)
        self.assertEqual(set(opened), {self.root.stat().st_ino,
                                     (self.root/'captures').stat().st_ino})

    def test_free_floor_accepts_equality_then_stops_below_on_next_poll(self):
        self.free = 100
        instance = self.create()
        instance.check()
        self.assertEqual(instance.free_bytes, 100)
        self.free = 99
        self.clock.now = 104
        instance.check()
        self.clock.now = 105
        self.stopped(instance, 'free_space_floor')
        self.assertEqual(instance.free_bytes, 99)

    def test_zero_scan_interval_checks_growth_every_call(self):
        instance = self.create(scan_interval_seconds=0)
        instance.check()
        self.write_size(self.root/'execution'/'new', 1000)
        self.stopped(instance, 'archive_limit')

    def test_stdlib_disk_usage_accepts_the_open_directory_descriptor(self):
        instance = guard.ExecutionGuard(self.root, 20, 1000, 0, clock=self.clock)
        instance.check()
        self.assertIs(type(instance.free_bytes), int)
        self.assertGreaterEqual(instance.free_bytes, 0)

    def test_symlink_files_directories_and_assignment_markers_are_not_followed(self):
        outside = Path(self.tmp.name)/'outside'
        self.write_size(outside/'huge', 10000)
        captures = self.root/'captures'
        captures.mkdir()
        (captures/'linked-file').symlink_to(outside/'huge')
        (captures/'linked-directory').symlink_to(outside, target_is_directory=True)
        (self.root/'runtime').symlink_to(outside, target_is_directory=True)
        unmarked = self.root/'not-an-assignment'
        unmarked.mkdir()
        (unmarked/'assignment.json').symlink_to(outside/'huge')
        self.write_size(unmarked/'ignored', 10000)
        self.write_size(captures/'regular', 7)
        instance = self.create()
        instance.check()
        self.assertEqual(instance.archive_bytes, 7)

    def test_root_symlink_is_rejected_and_missing_root_fails_closed(self):
        alias = Path(self.tmp.name)/'alias'
        alias.symlink_to(self.root, target_is_directory=True)
        instance = guard.ExecutionGuard(alias, 20, 1000, 100,
                                        clock=self.clock, disk_usage=self.disk_usage)
        self.stopped(instance, 'archive_scan_failed')
        self.root.rmdir()
        self.stopped(self.create(), 'archive_scan_failed')

    def test_directory_replaced_by_symlink_during_scan_fails_without_traversal(self):
        captures = self.root/'captures'
        child = captures/'nested'
        child.mkdir(parents=True)
        outside = Path(self.tmp.name)/'outside'
        self.write_size(outside/'never-read', 10000)
        real_open = os.open
        def swapped_open(path, flags, *args, **kwargs):
            if path == 'nested':
                child.rmdir()
                child.symlink_to(outside, target_is_directory=True)
            return real_open(path, flags, *args, **kwargs)
        with patch.object(guard.os, 'open', side_effect=swapped_open):
            self.stopped(self.create(), 'archive_scan_failed')

    def test_replaced_archive_root_identity_fails_closed(self):
        instance = self.create()
        instance.check()
        self.root.rename(Path(self.tmp.name)/'original')
        self.root.mkdir()
        self.clock.now = 105
        self.stopped(instance, 'archive_scan_failed')

    def test_scan_failure_uses_only_fixed_safe_reason(self):
        with patch.object(guard.os, 'scandir', side_effect=OSError('private path secret')):
            error = self.stopped(self.create(), 'archive_scan_failed')
        self.assertTrue(error.__suppress_context__)
        self.assertNotIn('secret', str(error))

    def test_scan_entry_stat_failure_is_not_silently_omitted(self):
        class BrokenEntry:
            def stat(self, **kwargs):
                raise FileNotFoundError('racing writer private name')
        class Entries:
            def __enter__(self):
                return iter([BrokenEntry()])
            def __exit__(self, *args):
                pass
        with patch.object(guard.os, 'scandir', return_value=Entries()):
            self.stopped(self.create(), 'archive_scan_failed')

    def test_disk_usage_error_or_invalid_free_value_fails_closed(self):
        def failed(fd):
            raise OSError('private mount detail')
        self.stopped(self.create(disk_usage=failed), 'disk_usage_failed')
        for free in [None, True, -1, float('nan'), 1.5]:
            with self.subTest(free=free):
                self.stopped(self.create(disk_usage=lambda fd: Usage(1, 0, free)),
                             'disk_usage_failed')

    def test_cleanup_grace_and_recovery_never_reopen_a_failed_guard(self):
        body = self.root/'captures'/'body'
        self.write_size(body, 1000)
        instance = self.create()
        self.stopped(instance, 'archive_limit')
        body.unlink()
        self.free = 20000
        self.clock.now = 130  # Launcher cleanup grace is outside admission.
        self.stopped(instance, 'archive_limit')
        self.assertEqual(instance.deadline, 120)

    def test_logical_sizes_count_sparse_files_and_hard_links_per_path(self):
        first = self.root/'captures'/'sparse'
        self.write_size(first, 600)
        os.link(first, first.with_name('duplicate'))
        instance = self.create()
        self.stopped(instance, 'archive_limit')
        self.assertEqual(instance.archive_bytes, 1200)

    def test_invalid_clock_samples_and_regression_latch_safely(self):
        for value in [float('nan'), float('inf'), -1, True, None]:
            with self.subTest(value=value):
                self.clock.now = 100
                instance = self.create()
                self.clock.now = value
                self.stopped(instance, 'clock_failed')
        self.clock.now = 100
        instance = self.create()
        instance.check()
        self.clock.now = 99
        self.stopped(instance, 'clock_failed')

    def test_clock_exception_is_fixed_and_constructor_clock_failure_is_safe(self):
        def failed():
            raise OSError('private clock details')
        with self.assertRaisesRegex(guard.RunLimitExceeded, '^clock_failed$'):
            self.create(clock=failed)

    def test_invalid_policy_values_are_rejected_without_echoing_input(self):
        cases = [dict(max_wall_seconds=0), dict(max_wall_seconds=float('inf')),
                 dict(max_wall_seconds=True), dict(max_archive_bytes=0),
                 dict(max_archive_bytes=True), dict(min_free_bytes=-1),
                 dict(scan_interval_seconds=-1), dict(clock=None), dict(disk_usage=None)]
        for options in cases:
            with self.subTest(options=options), self.assertRaises(ValueError):
                self.create(**options)
        with self.assertRaisesRegex(ValueError, '^unknown execution guard reason$'):
            guard.RunLimitExceeded('private arbitrary text')


if __name__ == '__main__':
    unittest.main()
