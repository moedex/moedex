import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock

import journey_clock


class ClockTests(unittest.TestCase):
    def test_clock_origin_is_shared_with_a_fresh_process(self):
        before = journey_clock.monotonic()
        child = float(subprocess.check_output([
            sys.executable, '-B', '-c',
            'import sys;sys.path.insert(0,sys.argv[1]);from journey_clock import monotonic;print(monotonic())',
            str(Path(journey_clock.__file__).parent),
        ], text=True))
        after = journey_clock.monotonic()
        self.assertLessEqual(before, child)
        self.assertLessEqual(child, after)

    def test_legacy_macos_uses_the_system_uptime_clock(self):
        with mock.patch.object(journey_clock.sys, 'platform', 'darwin'), \
             mock.patch.object(journey_clock.sys, 'version_info', (3, 9)), \
             mock.patch.object(journey_clock.time, 'CLOCK_UPTIME_RAW', 8, create=True), \
             mock.patch.object(journey_clock.time, 'clock_gettime', return_value=1234) as read, \
             mock.patch.object(journey_clock.time, 'monotonic') as relative:
            self.assertEqual(journey_clock.monotonic(), 1234)
            read.assert_called_once_with(8)
            relative.assert_not_called()

    def test_unsupported_legacy_clock_fails_closed(self):
        with mock.patch.object(journey_clock.sys, 'platform', 'darwin'), \
             mock.patch.object(journey_clock.sys, 'version_info', (3, 9)), \
             mock.patch.object(journey_clock.time, 'CLOCK_UPTIME_RAW', None, create=True):
            with self.assertRaisesRegex(RuntimeError, 'Python 3.10'):
                journey_clock.monotonic()

    def test_modern_python_uses_its_system_wide_monotonic_clock(self):
        with mock.patch.object(journey_clock.sys, 'version_info', (3, 14)), \
             mock.patch.object(journey_clock.time, 'monotonic', return_value=5678):
            self.assertEqual(journey_clock.monotonic(), 5678)


if __name__ == '__main__':
    unittest.main()
