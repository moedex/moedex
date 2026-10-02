import hashlib
import unittest

from check_discovery_workflow import raw_position


class RawPositionTests(unittest.TestCase):
    def test_byte_offsets_bom_unicode_and_crlf(self):
        text = '// café\r\nclass Example {}\r\n'
        for prefix in (b'', b'\xef\xbb\xbf'):
            raw = prefix + text.encode()
            self.assertEqual(raw_position(text, hashlib.sha256(raw).hexdigest(), 'class Example', 'Example'),
                             (raw.index(b'Example'), len(prefix)))

    def test_drift_partial_and_ambiguous_fail_closed(self):
        raw = b'class Example {}\n'
        sha = hashlib.sha256(raw).hexdigest()
        for text in ['class Example {}', 'class Example {}\n// changed', 'class Example {}\nclass Example {}\n']:
            with self.assertRaises(ValueError):
                raw_position(text, sha, 'class Example', 'Example')


if __name__ == '__main__':
    unittest.main()
