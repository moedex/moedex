"""Synthetic shared contract validation; no corpus or product calls."""
from copy import deepcopy
import json
from pathlib import Path
import tempfile
import unittest

import contract
from run_record import read_ref, reference


def fixture(root, schema='native-pair-v2'):
    serial = 0
    def retain(value):
        nonlocal serial
        serial += 1
        path = root / ('contract-fixture-%d.json' % serial)
        path.write_text(value if isinstance(value, str) else json.dumps(value))
        return reference(root, path)
    source = {'repository': 'https://example.test/api', 'commit': 'a' * 40,
              'manifest': retain({'files': []})}
    other = {'repository': 'https://example.test/client', 'commit': 'b' * 64,
             'manifest': retain({'files': []})}
    task = {'id': 'example', 'prompt': retain('fixture prompt\n'), 'atoms': ['example.1']}
    if schema == 'native-pair-v2':
        task['repositories'] = [source['repository'], other['repository']]
    return {'schema': schema,
            'corpus': {'repositories': [source, other]} if schema == 'native-pair-v2' else source,
            'rubric': retain({'reviewed': True}), 'protocol': retain({'native': True}),
            'tasks': [task], 'budgets': {'calls': 24, 'response_bytes': 262144,
                                       'assignment_seconds': 600, 'display_bytes': 8192}}


class ContractTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.value = fixture(self.root)

    def validate(self, value=None):
        return contract.validate(self.value if value is None else value,
                                 lambda ref: read_ref(self.root, ref))

    def test_v1_compatibility_and_v2_scopes_verify_all_references(self):
        self.assertEqual(set(self.validate(fixture(self.root, 'native-pair-v1'))), {'example'})
        self.value = fixture(self.root)
        visited = []
        def read(ref):
            visited.append(ref)
            return read_ref(self.root, ref)
        self.assertEqual(set(contract.validate(self.value, read)), {'example'})
        self.assertEqual(visited, [s['manifest'] for s in self.value['corpus']['repositories']] +
                         [self.value['rubric'], self.value['protocol'], self.value['tasks'][0]['prompt']])
        self.value['tasks'][0]['repositories'] = [self.value['corpus']['repositories'][1]['repository']]
        self.validate()

    def test_repository_roster_rejects_missing_duplicate_empty_and_unpinned(self):
        sources = self.value['corpus']['repositories']
        invalid = [[], None, [sources[0], sources[0]], [{}], [dict(sources[0], repository='')],
                   [dict(sources[0], repository=' api ')], [dict(sources[0], repository=42)],
                   [dict(sources[0], commit='main')], [dict(sources[0], commit='a' * 39)],
                   [dict(sources[0], commit='A' * 40)], [dict(sources[0], commit=None)],
                   [{k: v for k, v in sources[0].items() if k != 'manifest'}]]
        for roster in invalid:
            with self.subTest(roster=roster), self.assertRaises(ValueError):
                value = deepcopy(self.value)
                value['corpus']['repositories'] = roster
                self.validate(value)
        for corpus in ({}, sources[0], dict(self.value['corpus'], commit='a' * 40)):
            with self.subTest(corpus=corpus), self.assertRaises(ValueError):
                self.validate(dict(self.value, corpus=corpus))

    def test_task_scope_rejects_missing_duplicate_unknown_and_invalid(self):
        for scope in (None, [], ['https://example.test/api'] * 2, ['https://example.test/unknown'],
                      [''], [' https://example.test/api'], [17], 'https://example.test/api'):
            with self.subTest(scope=scope), self.assertRaises(ValueError):
                value = deepcopy(self.value)
                value['tasks'][0]['repositories'] = scope
                self.validate(value)
        del self.value['tasks'][0]['repositories']
        with self.assertRaises(ValueError):
            self.validate()

    def test_every_manifest_and_prompt_are_hash_checked(self):
        refs = [s['manifest'] for s in self.value['corpus']['repositories']] + [self.value['tasks'][0]['prompt']]
        for ref in refs:
            path = self.root / ref['path']
            original = path.read_bytes()
            path.write_bytes(original + b' ')
            with self.subTest(ref=ref), self.assertRaisesRegex(ValueError, 'digest mismatch'):
                self.validate()
            path.write_bytes(original)

    def test_invalid_tasks_atoms_budgets_and_schema(self):
        for key, invalid in (('tasks', []), ('tasks', [self.value['tasks'][0]] * 2),
                             ('tasks', [None]), ('schema', 'native-pair-v3')):
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.validate(dict(self.value, **{key: invalid}))
        for atoms in ([], ['x', 'x'], [''], [None], 'x'):
            value = deepcopy(self.value)
            value['tasks'][0]['atoms'] = atoms
            with self.subTest(atoms=atoms), self.assertRaises(ValueError):
                self.validate(value)
        for key, number in (('calls', True), ('calls', 1.5), ('response_bytes', 0),
                            ('assignment_seconds', float('inf')), ('display_bytes', -1)):
            value = deepcopy(self.value)
            value['budgets'][key] = number
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.validate(value)


if __name__ == '__main__':
    unittest.main()
