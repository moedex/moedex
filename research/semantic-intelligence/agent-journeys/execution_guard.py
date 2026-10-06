"""Read-only, best-effort wall/disk stopping for one launcher schedule.

This is filesystem polling, not a hard quota or a token/billing cap. The caller
owns admission, termination, cleanup grace and retention of actual outcomes.
"""
import math
import os
from pathlib import Path
import shutil
import stat
import time


REASONS = frozenset({'wall_limit', 'archive_limit', 'free_space_floor',
                     'archive_scan_failed', 'disk_usage_failed', 'clock_failed'})
ARCHIVE_TREES = frozenset({'execution', 'captures', 'runtime', 'assignments'})
# Never inspect assignment markers in source/dependency trees. Other top-level
# directories are traversed only when a regular assignment.json identifies them.
EXCLUDED_TREES = frozenset({'corpus', 'shared-corpus', 'source', 'sources',
                            'dependencies', 'vendor', 'node_modules', 'code',
                            'runners', 'tools', 'evidence'})


class RunLimitExceeded(RuntimeError):
    """Only fixed, safe categories; no paths or underlying exception messages."""
    def __init__(self, reason):
        if reason not in REASONS:
            raise ValueError('unknown execution guard reason')
        self.reason = reason
        super().__init__(reason)


class ExecutionGuard:
    """One owner thread, one immutable start/deadline, no retry or outcome I/O.

    Construct before startup and call check before admission and in the active
    monitoring loop. Filesystem checks run initially and then periodically;
    wall time is checked on every call and again after each filesystem poll.
    A failure is permanent even if capacity later recovers. The trusted archive
    root must exist. Traversal requires POSIX descriptor-relative no-follow I/O.
    """
    def __init__(self, archive_root, max_wall_seconds, max_archive_bytes,
                 min_free_bytes, scan_interval_seconds=5, *, clock=time.monotonic,
                 disk_usage=shutil.disk_usage):
        for value in (max_wall_seconds, scan_interval_seconds):
            if (type(value) not in (int, float) or not math.isfinite(value) or
                    value < 0):
                raise ValueError('finite nonnegative execution intervals required')
        if max_wall_seconds == 0:
            raise ValueError('positive wall limit required')
        if type(max_archive_bytes) is not int or max_archive_bytes <= 0:
            raise ValueError('positive integer archive threshold required')
        if type(min_free_bytes) is not int or min_free_bytes < 0:
            raise ValueError('nonnegative integer free-space floor required')
        if not callable(clock) or not callable(disk_usage):
            raise ValueError('execution guard callbacks required')
        # abspath does not resolve symlinks; opening the root uses O_NOFOLLOW.
        self._root = Path(os.path.abspath(os.fspath(archive_root)))
        self._clock, self._disk_usage = clock, disk_usage
        self._max_archive_bytes, self._min_free_bytes = max_archive_bytes, min_free_bytes
        self._scan_interval = scan_interval_seconds
        self._failure_reason = None
        self._archive_bytes = self._free_bytes = None
        self._last_time = self._last_scan = self._root_identity = None
        self._started_at = self._now()
        self._deadline = self._started_at + max_wall_seconds
        if not math.isfinite(self._deadline):
            raise ValueError('finite wall deadline required')

    @property
    def started_at(self):
        return self._started_at

    @property
    def deadline(self):
        return self._deadline

    @property
    def failure_reason(self):
        return self._failure_reason

    @property
    def archive_bytes(self):
        """Last scan's logical bytes, or the partial total at a size stop."""
        return self._archive_bytes

    @property
    def free_bytes(self):
        return self._free_bytes

    def _fail(self, reason):
        if self._failure_reason is None:
            self._failure_reason = reason
        raise RunLimitExceeded(self._failure_reason) from None

    def _now(self):
        try:
            value = self._clock()
            valid = (type(value) in (int, float) and math.isfinite(value) and
                     value >= 0 and (self._last_time is None or value >= self._last_time))
        except Exception:
            self._fail('clock_failed')
        if not valid:
            self._fail('clock_failed')
        self._last_time = value
        return value

    def _check_wall(self, now):
        if now >= self._deadline:
            self._fail('wall_limit')

    def check(self):
        """Return None while open; otherwise raise the latched fixed category.

        No work is dispatched, retried, stopped, deleted or classified here.
        The launcher must respond to a raised category and retain its captures.
        """
        if self._failure_reason is not None:
            self._fail(self._failure_reason)
        now = self._now()
        self._check_wall(now)
        if self._last_scan is None or now - self._last_scan >= self._scan_interval:
            self._poll_filesystem()
            now = self._now()
            self._check_wall(now)
            self._last_scan = now

    def _poll_filesystem(self):
        try:
            flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
            root_fd = os.open(self._root, flags)
            try:
                root_stat = os.fstat(root_fd)
                identity = (root_stat.st_dev, root_stat.st_ino)
                if self._root_identity is not None and self._root_identity != identity:
                    self._fail('archive_scan_failed')
                self._root_identity = identity
                self._archive_bytes = 0
                self._scan_root(root_fd, flags)
                self._check_free_space(root_fd)
            finally:
                os.close(root_fd)
        except RunLimitExceeded:
            raise
        except Exception:
            self._fail('archive_scan_failed')

    def _check_free_space(self, root_fd):
        try:
            # shutil.disk_usage accepts a directory descriptor on POSIX. Keep
            # the measured filesystem pinned even if the root path is renamed.
            free = self._disk_usage(root_fd).free
            if type(free) is not int or free < 0:
                self._fail('disk_usage_failed')
            self._free_bytes = free
        except RunLimitExceeded:
            raise
        except Exception:
            self._fail('disk_usage_failed')
        if free < self._min_free_bytes:
            self._fail('free_space_floor')

    def _scan_root(self, root_fd, flags):
        with os.scandir(root_fd) as entries:
            for entry in entries:
                mode = entry.stat(follow_symlinks=False).st_mode
                if stat.S_ISLNK(mode):
                    continue
                if entry.name in ARCHIVE_TREES:
                    if not stat.S_ISDIR(mode):
                        self._fail('archive_scan_failed')
                elif (not stat.S_ISDIR(mode) or entry.name in EXCLUDED_TREES or
                      entry.name.startswith('index')):
                    continue
                else:
                    # Probe the marker relative to an opened no-follow directory;
                    # do not read any file body or traverse an unselected tree.
                    fd = os.open(entry.name, flags, dir_fd=root_fd)
                    try:
                        try:
                            marker = os.stat('assignment.json', dir_fd=fd,
                                             follow_symlinks=False)
                        except FileNotFoundError:
                            continue
                        if stat.S_ISREG(marker.st_mode):
                            self._scan_directory(fd, flags)
                    finally:
                        os.close(fd)
                    continue
                fd = os.open(entry.name, flags, dir_fd=root_fd)
                try:
                    self._scan_directory(fd, flags)
                finally:
                    os.close(fd)

    def _scan_directory(self, directory_fd, flags):
        with os.scandir(directory_fd) as entries:
            for entry in entries:
                info = entry.stat(follow_symlinks=False)
                if stat.S_ISREG(info.st_mode):
                    self._archive_bytes += info.st_size
                    if self._archive_bytes >= self._max_archive_bytes:
                        self._fail('archive_limit')
                elif stat.S_ISDIR(info.st_mode):
                    fd = os.open(entry.name, flags, dir_fd=directory_fd)
                    try:
                        self._scan_directory(fd, flags)
                    finally:
                        os.close(fd)
                # Symlinks, sockets and other special files have no file-body
                # accounting and are never opened. Regular sizes are logical,
                # so sparse files count in full and hard links count per path.
