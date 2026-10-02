"""Behavior checks for the disposable corpus runner; no external corpus needed."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest


RUNNER = Path(__file__).with_name("graph-corpus-trial.py")


@unittest.skipUnless(sys.platform == "darwin" or sys.platform.startswith("linux"),
                     "runner requires a supported memory metric")
class TrialTests(unittest.TestCase):
    def run_trial(self, code, *options):
        with tempfile.TemporaryDirectory() as directory:
            run = subprocess.run([sys.executable, str(RUNNER), "--output-dir", directory,
                                  "--name", "case", *options, "--", sys.executable,
                                  "-c", code], capture_output=True, timeout=15)
            manifest = json.loads((Path(directory) / "case.json").read_text())
            return run, manifest, (Path(directory) / "case.stdout").read_bytes()

    def test_success_and_live_logs(self):
        with tempfile.TemporaryDirectory() as directory:
            command = [sys.executable, str(RUNNER), "--output-dir", directory,
                       "--name", "live", "--", sys.executable, "-c",
                       "import time; print('progress', flush=True); time.sleep(2)"]
            process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                log = Path(directory) / "live.stdout"
                deadline = time.monotonic() + 1.5
                while time.monotonic() < deadline:
                    if log.exists() and log.read_bytes() == b"progress\n":
                        break
                    time.sleep(0.02)
                self.assertEqual(log.read_bytes(), b"progress\n")
                self.assertIsNone(process.poll(), "log must be visible before completion")
                stdout, stderr = process.communicate(timeout=10)
                self.assertEqual(process.returncode, 0, stderr)
                result = json.loads(stdout)
                self.assertEqual(result["stop_reasons"], [])
                self.assertGreater(result["memory_samples"], 0)
                duplicate = subprocess.run(command, capture_output=True, timeout=5)
                self.assertNotEqual(duplicate.returncode, 0)
                self.assertIn(b"refusing to overwrite", duplicate.stderr)
            finally:
                if process.poll() is None:
                    process.kill()
                process.communicate()

    def test_deadline(self):
        run, manifest, _ = self.run_trial("import time; time.sleep(5)", "--timeout-seconds", "1")
        self.assertNotEqual(run.returncode, 0)
        self.assertIn("deadline exceeded", manifest["stop_reasons"])

    def test_log_limit(self):
        run, manifest, output = self.run_trial("print('x'*10000)", "--max-log-bytes", "64")
        self.assertNotEqual(run.returncode, 0)
        self.assertEqual(len(output), 64)
        self.assertIn("log budget exceeded: .stdout", manifest["stop_reasons"])

    def test_memory_limit(self):
        run, manifest, _ = self.run_trial(
            "import time; x=bytearray(128*1024*1024); time.sleep(5)", "--max-memory-mib", "64")
        self.assertNotEqual(run.returncode, 0)
        self.assertIn("sampled memory budget exceeded", manifest["stop_reasons"])
        self.assertGreater(manifest["sampled_peak_memory_bytes"], 64 << 20)


if __name__ == "__main__":
    unittest.main()
