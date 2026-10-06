"""Synthetic offline mechanism checks; no native products or corpus access."""
from copy import deepcopy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location('broader_claim', HERE / 'broader_claim.py')
bc = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bc)


def ref(name):
    return {'path': 'reviews/' + name + '.json', 'sha256': bc.digest(name)}


def fixture(families=30, repetitions=3):
    """Each synthetic family occurs in all five kinds, under one system."""
    repo = {'repository': 'example/api', 'commit': 'a' * 40, 'manifest_sha256': bc.digest('source')}
    tasks = []
    for kind in range(5):
        for family in range(families):
            identity = 'task-%d-%d' % (kind, family)
            tasks.append({'id': identity, 'stratum': 'stratum-%d' % kind,
                          'source_family': 'family-%d' % family, 'system': 'system-%d' % family,
                          'domain': 'example', 'language': 'example', 'task_kind': 'kind-%d' % kind,
                          'source_scope': [deepcopy(repo)], 'prompt_sha256': bc.digest(identity + '-prompt'),
                          'rubric_sha256': bc.digest(identity + '-rubric'),
                          'witness_sha256s': [bc.digest(identity + '-witness')]})
    frame = {'schema': 'broader-claim-frame-v1', 'provenance': 'source-only', 'tasks': tasks}
    prior_task = deepcopy(tasks[0])
    prior_task.update(id='pilot-task', source_family='pilot-family', system='pilot-system',
                      prompt_sha256=bc.digest('pilot-prompt'), rubric_sha256=bc.digest('pilot-rubric'),
                      witness_sha256s=[bc.digest('pilot-witness')])
    prior_sample = {'schema': 'broader-claim-sample-v1', 'sampling_method': bc.SAMPLING,
                    'plan_sha256': bc.digest('pilot'), 'frame_sha256': bc.digest('pilot-frame'), 'tasks': [prior_task]}
    plan = {'schema': 'broader-claim-plan-v1', 'plan_id': 'synthetic-confirmation', 'phase': 'confirmation',
            'arms': ['A', 'B'], 'repetitions': repetitions, 'sample_seed': 7,
            'frame_sha256': bc.digest(frame), 'corpus': [repo],
            'strata': [{'id': 'stratum-%d' % kind, 'domain': 'example', 'language': 'example',
                        'task_kind': 'kind-%d' % kind, 'count': families} for kind in range(5)],
            'task_kind_weights': {'kind-%d' % kind: 0.20 for kind in range(5)},
            'excluded': {'source_families': [], 'witness_sha256s': []},
            'governance': {'frozen_before_answers': True, 'sample_size_final': True, 'fixed_schedule': True,
                           'prior_pilot': {'plan_id': 'synthetic-pilot', 'plan_sha256': bc.digest('pilot'),
                                           'sample_sha256': bc.digest(prior_sample), 'sample': prior_sample}},
            'readiness': {'setup_ready': True, **{name: ref(name) for name in
                          ('source_only_frame_review', 'source_pin_alignment_review', 'raw_capture_policy',
                           'independent_scoring_policy', 'blinding_review', 'comparability_policy')}},
            'bootstrap': {'method': bc.METHOD, 'seed': 13, 'replicates': 1000,
                          'confidence': 0.95, 'practical_margin': 0.05},
            'coverage': dict(bc.FLOORS, max_cluster_share_per_kind=0.20)}
    return plan, frame


def attempts(plan, selected, successful=None):
    if successful is None:
        # Heterogeneous, with an ample advantage and genuine observed influence.
        successful = lambda task, arm, repeat: arm == 'A' and int(task['source_family'].split('-')[-1]) % 5 != 0
    rows = []
    for task in selected['tasks']:
        for arm in plan['arms']:
            for repeat in range(1, plan['repetitions'] + 1):
                task_sha, final = bc.digest(task), ref(task['id'] + arm + str(repeat))
                common = {'reviewer_id': 'reviewer', 'independent': True, 'complete': True,
                          'task_sha256': task_sha, 'final_sha256': final['sha256'], 'artifact': ref('audit')}
                source = dict(common, criteria={name: True for name in bc.CRITERIA})
                source['criteria']['all_required_correct'] = successful(task, arm, repeat)
                rows.append({'task_id': task['id'], 'arm': arm, 'repetition': repeat, 'task_sha256': task_sha,
                             'solver_id': 'solver-' + task['id'] + '-' + arm + '-' + str(repeat),
                             'outcome': 'answered', 'failure_class': None, 'final': final,
                             'raw_review': dict(common, capture_integrity=True, classification_confirmed=True),
                             'source_review': source})
    return {'schema': 'broader-claim-results-v1', 'plan_sha256': bc.digest(plan),
            'sample_sha256': bc.digest(selected), 'shared_harness_defect': {'confirmed': False, 'review': None},
            'equal_access': {'established': True, 'review': ref('access')}, 'attempts': rows}


class BroaderClaimTests(unittest.TestCase):
    def setUp(self):
        self.plan, self.frame = fixture()
        self.selected = bc.sample(self.plan, self.frame)
        self.results = attempts(self.plan, self.selected)

    def values(self):
        return bc.task_values(self.plan, self.selected, self.results)

    def analysis(self):
        return bc.analyze(self.plan, self.frame, self.selected, self.results)

    def rebind(self):
        self.plan['frame_sha256'] = bc.digest(self.frame)
        self.selected = bc.sample(self.plan, self.frame)
        self.results = attempts(self.plan, self.selected)

    def test_ready_confirmation_and_denominators(self):
        result = self.analysis()
        self.assertEqual(result['status'], 'conditional_practical_lead')
        self.assertEqual(result['conditional_leading_arm'], 'A')
        self.assertEqual(result['unique_tasks'], 150)
        self.assertEqual(result['planned_attempts'], 900)
        self.assertAlmostEqual(result['point_delta_A_minus_B'], 0.8)
        self.assertTrue(all(x['approximate'] and x['uncertainty_estimable'] for x in result['cluster_intervals']))
        self.assertIn('do not authenticate', result['limitations'])

    def test_b_lead(self):
        self.results = attempts(self.plan, self.selected, lambda task, arm, repeat:
                                arm == 'B' and int(task['source_family'].split('-')[-1]) % 5 != 0)
        self.assertEqual(self.analysis()['conditional_leading_arm'], 'B')

    def test_task_kind_weights_do_not_pool_task_counts(self):
        tasks = [{'task_kind': 'kind-%d' % kind, 'delta': int(kind == 0)}
                 for kind in range(5) for _ in range(100 if kind == 0 else 1)]
        self.assertAlmostEqual(bc.weighted_delta(tasks, self.plan['task_kind_weights']), 0.20)

    def test_repetitions_are_averaged_before_cluster_analysis(self):
        self.results = attempts(self.plan, self.selected, lambda task, arm, repeat: arm == 'A' and repeat == 1)
        values, _, _ = self.values()
        self.assertEqual(len(values), 150)
        self.assertTrue(all(t['delta'] == 1 / 3 for t in values))

    def test_claim_citation_failure_is_zero_despite_physical_provenance(self):
        row = next(r for r in self.results['attempts'] if r['source_review']['criteria']['all_required_correct'])
        row['source_review']['criteria']['claim_citation_accuracy'] = False
        values, reasons, _ = self.values()
        value = next(t for t in values if t['id'] == row['task_id'])
        self.assertAlmostEqual(value['success_A'], 2 / 3)
        self.assertFalse(reasons)

    def test_each_whole_task_criterion_is_required(self):
        for criterion in bc.CRITERIA:
            with self.subTest(criterion=criterion):
                results = deepcopy(self.results)
                for row in results['attempts']:
                    row['source_review']['criteria'][criterion] = False
                values, _, _ = bc.task_values(self.plan, self.selected, results)
                self.assertTrue(all(t['success_A'] == t['success_B'] == 0 for t in values))

    def test_sampling_order_independent_and_source_only(self):
        original_ids = [t['id'] for t in self.selected['tasks']]
        self.frame['tasks'].reverse()
        self.rebind()
        self.assertEqual(original_ids, [t['id'] for t in self.selected['tasks']])
        self.frame['provenance'] = 'native-hits'
        self.plan['frame_sha256'] = bc.digest(self.frame)
        with self.assertRaisesRegex(ValueError, 'source-only'):
            bc.sample(self.plan, self.frame)

    def test_source_family_and_witness_exclusions(self):
        self.plan, self.frame = fixture(families=32)
        self.plan['excluded']['source_families'] = ['family-0']
        self.plan['excluded']['witness_sha256s'] = [self.frame['tasks'][1]['witness_sha256s'][0]]
        for stratum in self.plan['strata']:
            stratum['count'] = 30
        selected = bc.sample(self.plan, self.frame)
        self.assertFalse(any(t['source_family'] == 'family-0' for t in selected['tasks']))
        self.assertFalse(any(t['id'] == self.frame['tasks'][1]['id'] for t in selected['tasks']))
        self.plan['strata'][0]['count'] = 31
        with self.assertRaisesRegex(ValueError, 'insufficient unseen'):
            bc.sample(self.plan, self.frame)

    def test_prior_sample_automatically_excludes_ids_fingerprints_families_witnesses(self):
        for dimension in ('id', 'fingerprint', 'family', 'witness'):
            with self.subTest(dimension=dimension):
                plan, frame = fixture(families=32)
                for stratum in plan['strata']:
                    stratum['count'] = 30
                prior = plan['governance']['prior_pilot']
                task = prior['sample']['tasks'][0]
                candidate = frame['tasks'][0]
                if dimension == 'id':
                    task['id'] = candidate['id']
                elif dimension == 'fingerprint':
                    for name in ('prompt_sha256', 'rubric_sha256', 'source_scope'):
                        task[name] = deepcopy(candidate[name])
                elif dimension == 'family':
                    task['source_family'] = candidate['source_family']
                    task['system'] = candidate['system']
                else:
                    task['witness_sha256s'] = deepcopy(candidate['witness_sha256s'])
                prior['sample_sha256'] = bc.digest(prior['sample'])
                selected = bc.sample(plan, frame)
                self.assertFalse(any(row['id'] == candidate['id'] for row in selected['tasks']))
                if dimension == 'family':
                    self.assertFalse(any(row['source_family'] == candidate['source_family'] for row in selected['tasks']))

    def test_prior_pilot_sample_binding_and_presence_required(self):
        for mutation in ('hash', 'plan', 'empty'):
            plan = deepcopy(self.plan)
            prior = plan['governance']['prior_pilot']
            if mutation == 'hash':
                prior['sample_sha256'] = bc.digest('incorrect')
            elif mutation == 'plan':
                prior['sample']['plan_sha256'] = bc.digest('incorrect')
                prior['sample_sha256'] = bc.digest(prior['sample'])
            else:
                prior['sample']['tasks'] = []
                prior['sample_sha256'] = bc.digest(prior['sample'])
            with self.assertRaises(ValueError):
                bc.validate_plan(plan)

    def test_duplicate_fingerprint_rejected_even_under_distinct_ids(self):
        duplicate = deepcopy(self.frame['tasks'][0])
        duplicate['id'] = 'different-task-id'
        duplicate['source_family'] = 'different-family'
        duplicate['system'] = 'different-system'
        self.frame['tasks'].append(duplicate)
        self.plan['frame_sha256'] = bc.digest(self.frame)
        with self.assertRaisesRegex(ValueError, 'duplicate task prompt/rubric/source fingerprint'):
            bc.sample(self.plan, self.frame)

    def test_sampling_priority_frozen_seed_and_quotas(self):
        self.plan, self.frame = fixture(families=32)
        for stratum in self.plan['strata']:
            stratum['count'] = 30
        selected = bc.sample(self.plan, self.frame)
        stratum = self.plan['strata'][0]
        candidates = [t for t in self.frame['tasks'] if t['stratum'] == stratum['id']]
        expected = sorted(candidates, key=lambda t: (bc.digest([7, stratum['id'], t['id']]), t['id']))[:30]
        self.assertEqual(expected, selected['tasks'][:30])

    def test_missing_duplicate_unknown_attempts_fail(self):
        for mutation in ('missing', 'duplicate', 'unknown', 'repeat', 'arm'):
            with self.subTest(mutation=mutation):
                results = deepcopy(self.results)
                if mutation == 'missing':
                    results['attempts'].pop()
                elif mutation == 'duplicate':
                    results['attempts'].append(deepcopy(results['attempts'][0]))
                else:
                    row = results['attempts'][0]
                    row[{'unknown': 'task_id', 'repeat': 'repetition', 'arm': 'arm'}[mutation]] = {'unknown': 'unknown', 'repeat': 4, 'arm': 'C'}[mutation]
                with self.assertRaises(ValueError):
                    bc.task_values(self.plan, self.selected, results)

    def test_source_pin_and_manifest_mismatch_fail(self):
        for field, value in [('commit', 'b' * 40), ('manifest_sha256', bc.digest('different'))]:
            frame = deepcopy(self.frame)
            frame['tasks'][0]['source_scope'][0][field] = value
            plan = deepcopy(self.plan)
            plan['frame_sha256'] = bc.digest(frame)
            with self.assertRaisesRegex(ValueError, 'pin or manifest'):
                bc.sample(plan, frame)

    def test_family_cannot_cross_systems(self):
        self.frame['tasks'][30]['system'] = 'other-system'
        self.plan['frame_sha256'] = bc.digest(self.frame)
        with self.assertRaisesRegex(ValueError, 'crosses coarser'):
            bc.sample(self.plan, self.frame)

    def test_task_and_result_bindings_fail(self):
        for field in ('prompt_sha256', 'rubric_sha256', 'source_scope'):
            selected = deepcopy(self.selected)
            selected['tasks'][0][field] = [] if field == 'source_scope' else bc.digest('changed')
            with self.assertRaisesRegex(ValueError, 'selected roster'):
                bc.analyze(self.plan, self.frame, selected, self.results)
        self.results['attempts'][0]['task_sha256'] = bc.digest('changed')
        with self.assertRaisesRegex(ValueError, 'binding differs'):
            self.values()

    def test_atom_counts_and_unknown_fields_rejected(self):
        self.results['attempts'][0]['correct_atoms'] = 9
        with self.assertRaisesRegex(ValueError, 'unexpected or missing'):
            self.values()

    def test_reviewed_stops_are_zero_and_retained(self):
        for outcome, cause in [('infrastructure_stop', 'infrastructure'), ('budget_stop', 'budget'),
                               ('product_stop', 'product'), ('no_final', 'product'), ('no_final', 'budget')]:
            with self.subTest(outcome=outcome):
                results = deepcopy(self.results)
                for row in results['attempts']:
                    row.update(outcome=outcome, failure_class=cause, source_review=None)
                    if outcome == 'no_final':
                        row['final'] = None
                        row['raw_review']['final_sha256'] = None
                values, reasons, counts = bc.task_values(self.plan, self.selected, results)
                self.assertFalse(reasons)
                self.assertTrue(all(t['delta'] == 0 and t['success_A'] == t['success_B'] == 0 for t in values))
                self.assertEqual(sum(counts.values()), 900)

    def test_no_final_cannot_have_final_and_failures_cannot_be_answered(self):
        self.results['attempts'][0].update(outcome='no_final', failure_class='product')
        with self.assertRaisesRegex(ValueError, 'distinct'):
            self.values()
        self.results['attempts'][0].update(outcome='answered', failure_class='product')
        with self.assertRaisesRegex(ValueError, 'answered attempt'):
            self.values()

    def test_shared_harness_defect_blocks_lead(self):
        self.results['shared_harness_defect'] = {'confirmed': True, 'review': ref('defect')}
        result = self.analysis()
        self.assertEqual(result['status'], 'readiness_not_met')
        self.assertIsNone(result['conditional_leading_arm'])
        self.assertIn('confirmed_shared_harness_defect', result['readiness_reasons'])

    def test_scope_stop_and_revoked_access_are_retained(self):
        row = self.results['attempts'][0]
        row.update(outcome='scope_stop', failure_class='access', source_review=None)
        with self.assertRaisesRegex(ValueError, 'contradicts equal access'):
            self.values()
        self.results['equal_access'] = {'established': False, 'review': ref('access-loss')}
        values, reasons, counts = self.values()
        self.assertEqual(len(values), 150)
        self.assertEqual(sum(counts.values()), 900)
        self.assertIn('equal_access_not_established', reasons)
        row['failure_class'] = 'scope'
        self.assertIn('confirmed_scope_violation', self.values()[1])

    def test_unclassified_and_incomplete_reviews_block_readiness(self):
        row = self.results['attempts'][0]
        row.update(outcome='infrastructure_stop', failure_class='unclassified', source_review=None)
        self.assertIn('unclassified_failure', self.values()[1])
        for key in ('raw_review', 'source_review'):
            results = deepcopy(attempts(self.plan, self.selected))
            results['attempts'][0][key]['complete'] = False
            _, reasons, _ = bc.task_values(self.plan, self.selected, results)
            self.assertIn('missing_or_incomplete_' + ('raw' if key == 'raw_review' else 'source') + '_review', reasons)

    def test_solver_cannot_review_self_and_review_binding_is_strict(self):
        row = self.results['attempts'][0]
        row['raw_review']['reviewer_id'] = row['solver_id']
        self.assertIn('missing_or_incomplete_raw_review', self.values()[1])
        row['raw_review']['task_sha256'] = bc.digest('different')
        with self.assertRaisesRegex(ValueError, 'review task/final binding'):
            self.values()

    def test_solver_identity_is_unique_and_reviewers_cannot_be_other_solvers(self):
        self.results['attempts'][1]['solver_id'] = self.results['attempts'][0]['solver_id']
        with self.assertRaisesRegex(ValueError, 'distinct declared solver'):
            self.values()
        self.results = attempts(self.plan, self.selected)
        self.results['attempts'][0]['raw_review']['reviewer_id'] = self.results['attempts'][1]['solver_id']
        self.assertIn('missing_or_incomplete_raw_review', self.values()[1])
        self.results['attempts'][0]['source_review']['reviewer_id'] = self.results['attempts'][1]['solver_id']
        self.assertIn('missing_or_incomplete_source_review', self.values()[1])

    def test_comparability_review_required_for_readiness(self):
        self.plan['readiness']['comparability_policy'] = None
        self.assertIn('missing_comparability_policy', bc.validate_plan(self.plan)['readiness_reasons'])

    def test_false_readiness_and_unfrozen_confirmation_block(self):
        self.plan['readiness']['setup_ready'] = False
        self.plan['readiness']['blinding_review'] = None
        self.plan['governance']['sample_size_final'] = False
        self.selected = bc.sample(self.plan, self.frame)
        self.results = attempts(self.plan, self.selected)
        result = self.analysis()
        self.assertIsNone(result['conditional_leading_arm'])
        self.assertTrue({'setup_not_ready', 'missing_blinding_review', 'sample_size_final'} <= set(result['readiness_reasons']))

    def test_pilot_is_never_confirmation_winner(self):
        self.plan['phase'] = 'pilot'
        self.plan['governance']['prior_pilot'] = None
        self.selected = bc.sample(self.plan, self.frame)
        self.results = attempts(self.plan, self.selected)
        result = self.analysis()
        self.assertIn('pilot_is_exploratory_not_confirmation', result['readiness_reasons'])
        self.assertIsNone(result['conditional_leading_arm'])

    def test_coverage_floors_cannot_be_weakened(self):
        for key in bc.FLOORS:
            plan = deepcopy(self.plan)
            plan['coverage'][key] = bc.FLOORS[key] - 1
            with self.assertRaisesRegex(ValueError, 'cannot be weakened'):
                bc.validate_plan(plan)
        self.plan['coverage']['max_cluster_share_per_kind'] = 0.21
        with self.assertRaises(ValueError):
            bc.validate_plan(self.plan)

    def test_coverage_dominance_effective_counts_at_both_grains(self):
        tasks, _, _ = self.values()
        reasons, coverage = bc.coverage_checks(tasks, self.plan['coverage'])
        self.assertFalse(reasons)
        for grain in ('source_family', 'system'):
            self.assertAlmostEqual(coverage[grain]['per_kind']['kind-0']['kish_effective_clusters'], 30)
            changed = deepcopy(tasks)
            for task in changed:
                if task['task_kind'] == 'kind-0' and int(task['source_family'].split('-')[-1]) < 15:
                    task[grain] = 'dominant'
            reasons, coverage = bc.coverage_checks(changed, self.plan['coverage'])
            self.assertTrue(any('dominated' in reason for reason in reasons))
            self.assertEqual(coverage[grain]['per_kind']['kind-0']['max_weight_share'], 0.5)

    def test_global_and_per_kind_coverage_independently_guarded(self):
        tasks, _, _ = self.values()
        few = [t for t in tasks if int(t['source_family'].split('-')[-1]) < 7]
        reasons, _ = bc.coverage_checks(few, self.plan['coverage'])
        self.assertIn('insufficient_families', reasons)
        self.assertIn('insufficient_systems', reasons)
        self.assertTrue(any(reason.endswith(':kind-0') for reason in reasons))

    def test_shared_cluster_weights_preserve_fixed_kind_weights(self):
        tasks = [{'task_kind': 'kind-%d' % kind, 'source_family': family, 'delta': delta}
                 for kind in range(5) for family, delta in [('one', 1 if kind == 0 else 0), ('two', 0)]]
        self.assertAlmostEqual(bc.weighted_delta(tasks, self.plan['task_kind_weights'], 'source_family',
                                               {'one': 3, 'two': 1}), 0.15)
        for multiplier in (0, -1, float('inf'), float('nan')):
            with self.assertRaises(ValueError):
                bc.weighted_delta(tasks, self.plan['task_kind_weights'], 'source_family', {'one': multiplier, 'two': 1})
        with self.assertRaisesRegex(ValueError, 'roster'):
            bc.weighted_delta(tasks, self.plan['task_kind_weights'], 'source_family', {'one': 1})

    def test_exp_draw_is_shared_across_kinds_and_never_empty(self):
        tasks, _, _ = self.values()
        observed = []
        original = bc.weighted_delta
        def capture(rows, weights, grain, multipliers):
            observed.append(multipliers.copy())
            return original(rows, weights, grain, multipliers)
        with patch.object(bc, 'weighted_delta', side_effect=capture):
            interval = bc.cluster_interval(tasks, self.plan['task_kind_weights'], 'source_family', self.plan['bootstrap'])
        self.assertEqual(len(observed), 1000)
        self.assertTrue(all(len(draw) == 30 and min(draw.values()) > 0 for draw in observed))
        self.assertEqual(interval, bc.cluster_interval(tasks, self.plan['task_kind_weights'], 'source_family', self.plan['bootstrap']))

    def test_degenerate_cluster_distribution_has_no_winner(self):
        self.results = attempts(self.plan, self.selected, lambda task, arm, repeat: arm == 'A')
        result = self.analysis()
        self.assertIsNone(result['conditional_leading_arm'])
        self.assertTrue(all(not x['uncertainty_estimable'] for x in result['cluster_intervals']))
        self.assertIn('uncertainty_not_estimable:system', result['readiness_reasons'])

    def test_coarser_grain_degenerate_blocks_family_uncertainty(self):
        self.plan, self.frame = fixture(families=60)
        for task in self.frame['tasks']:
            task['system'] = 'system-%d' % (int(task['source_family'].split('-')[-1]) // 2)
        self.rebind()
        self.results = attempts(self.plan, self.selected, lambda task, arm, repeat:
                                arm == 'A' and int(task['source_family'].split('-')[-1]) % 2 == 0)
        result = self.analysis()
        self.assertTrue(result['cluster_intervals'][0]['uncertainty_estimable'])
        self.assertFalse(result['cluster_intervals'][1]['uncertainty_estimable'])
        self.assertIn('uncertainty_not_estimable:system', result['readiness_reasons'])
        self.assertIsNone(result['conditional_leading_arm'])

    def test_both_grains_and_strict_margin_are_required(self):
        intervals = [{'lower': 0.2, 'upper': 0.8}, {'lower': 0.05, 'upper': 0.8}]
        def interval(*args):
            return dict(intervals.pop(0), cluster_key=args[2], uncertainty_estimable=True)
        with patch.object(bc, 'cluster_interval', side_effect=interval):
            self.assertEqual(self.analysis()['status'], 'inconclusive')
        intervals.extend([{'lower': 0.2, 'upper': 0.8}, {'lower': -0.8, 'upper': -0.2}])
        with patch.object(bc, 'cluster_interval', side_effect=interval):
            self.assertIsNone(self.analysis()['conditional_leading_arm'])

    def test_invalid_method_weights_duplicate_frame_and_reference_fail(self):
        self.plan['bootstrap']['method'] = 'other'
        with self.assertRaises(ValueError):
            bc.validate_plan(self.plan)
        self.plan['bootstrap']['method'] = bc.METHOD
        self.plan['task_kind_weights']['kind-0'] = 0.2000000000001
        with self.assertRaises(ValueError):
            bc.validate_plan(self.plan)
        for path in ('.', '../outside', '/absolute', 'directory/', 'a\\b'):
            with self.assertRaises(ValueError):
                bc.reference({'path': path, 'sha256': bc.digest('ref')}, 'synthetic')
        with self.assertRaises(ValueError):
            bc.unique_strings([{}], 'synthetic')

    def test_json_duplicates_nonfinite_and_exclusive_cli_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            invalid = root / 'invalid.json'
            for body in ('{"x":1,"x":2}', '{"x":NaN}', '{"x":Infinity}'):
                invalid.write_text(body)
                with self.assertRaises(ValueError):
                    bc.load(invalid)
            plan_path, output = root / 'plan.json', root / 'result.json'
            plan_path.write_text(json.dumps(self.plan))
            command = [sys.executable, '-B', str(HERE / 'broader_claim.py'), 'validate-plan',
                       '--plan', str(plan_path), '--output', str(output)]
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            original = output.read_bytes()
            self.assertTrue(json.loads(original)['run_readiness'])
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(original, output.read_bytes())


if __name__ == '__main__':
    unittest.main()
