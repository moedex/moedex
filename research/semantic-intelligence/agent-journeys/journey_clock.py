"""A process-independent clock for persisted native-journey deadlines."""
import sys
import time


def monotonic():
    # Before Python 3.10, macOS time.monotonic() subtracts a process-local
    # origin. Persisting that value makes a fresh CLI process reset the budget.
    if sys.platform == 'darwin' and sys.version_info < (3, 10):
        clock_id = getattr(time, 'CLOCK_UPTIME_RAW', None)
        if clock_id is None:
            raise RuntimeError('journey deadlines require CLOCK_UPTIME_RAW or Python 3.10+ on macOS')
        return time.clock_gettime(clock_id)
    return time.monotonic()
