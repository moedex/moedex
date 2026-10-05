#!/usr/bin/env python3
"""Validate a frozen benchmark, sample source-only tasks, and analyze paired claims.

This checks declared review/binding consistency, not reviewer authenticity,
source semantics, or raw-capture truth. It never contacts a product or provider.
"""
import argparse
from collections import Counter, defaultdict
import hashlib
import json
import math
from pathlib import Path, PurePosixPath
import random
import re
import sys


METHOD = 'shared-exp1-cluster-weighted-percentile-v1'
SAMPLING = 'sha256-priority-within-source-stratum-v1'
LIMITATION = ('Intervals approximate cluster-weighted uncertainty for the declared balanced benchmark; '
              'they do not guarantee finite-sample 95% coverage or represent product usage frequencies. '
              'A degenerate observed cluster distribution cannot establish unseen-tail uncertainty. '
              'JSON review declarations and hashes do not authenticate reviewers, raw evidence, or source semantics.')
CRITERIA = {'all_required_correct', 'physical_provenance', 'claim_citation_accuracy',
            'no_critical_unsupported_claims', 'source_scope_complete'}
TASK_FIELDS = {'id', 'stratum', 'source_family', 'system', 'domain', 'language', 'task_kind',
               'source_scope', 'prompt_sha256', 'rubric_sha256', 'witness_sha256s'}
FLOORS = {'families': 30, 'systems': 20, 'families_per_kind': 10,
          'systems_per_kind': 8, 'family_effective_per_kind': 10,
          'system_effective_per_kind': 8}
OUTCOMES = {'answered', 'infrastructure_stop', 'budget_stop', 'product_stop', 'scope_stop', 'no_final'}
HEX = re.compile(r'^[0-9a-f]{64}$')
PIN = re.compile(r'^[0-9a-f]{40}$')


def require(condition, message):
    if not condition:
        raise ValueError(message)


def keys(value, expected, label):
    require(type(value) is dict and set(value) == set(expected), label + ': unexpected or missing fields')


def text(value, label):
    require(type(value) is str and bool(value.strip()), label + ': nonempty string required')


def boolean(value, label):
    require(type(value) is bool, label + ': boolean required')


def number(value, label, lower=0, upper=None):
    require(type(value) in (int, float) and math.isfinite(value) and value > lower and
            (upper is None or value <= upper), label + ': invalid finite numeric value')


def integer(value, label, lower=1, upper=None):
    require(type(value) is int and value >= lower and (upper is None or value <= upper),
            label + ': invalid integer')


def sha(value, label):
    require(type(value) is str and HEX.fullmatch(value), label + ': immutable SHA-256 required')


def reference(value, label):
    keys(value, {'path', 'sha256'}, label)
    text(value['path'], label)
    path = PurePosixPath(value['path'])
    require(path.parts and not path.is_absolute() and '..' not in path.parts and '\\' not in value['path']
            and not value['path'].endswith('/'),
            label + ': confined relative path required')
    sha(value['sha256'], label)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False, allow_nan=False).encode()


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def unique_strings(values, label, hashes=False):
    require(type(values) is list, label + ': list required')
    for value in values:
        (sha if hashes else text)(value, label)
    require(len(set(values)) == len(values), label + ': unique list required')


def task_fingerprint(task):
    return digest([task['prompt_sha256'], task['rubric_sha256'],
                   sorted(task['source_scope'], key=lambda row: row['repository'])])


def validate_task(task):
    keys(task, TASK_FIELDS, 'candidate task')
    for name in ('id', 'stratum', 'source_family', 'system', 'domain', 'language', 'task_kind'):
        text(task[name], name)
    sha(task['prompt_sha256'], 'prompt'); sha(task['rubric_sha256'], 'rubric')
    unique_strings(task['witness_sha256s'], 'source witnesses', hashes=True)
    require(task['witness_sha256s'], 'source witnesses required')
    require(type(task['source_scope']) is list and task['source_scope'], 'explicit pinned task scope required')
    repositories = set()
    for repo in task['source_scope']:
        keys(repo, {'repository', 'commit', 'manifest_sha256'}, 'task source')
        text(repo['repository'], 'task repository')
        require(repo['repository'] not in repositories, 'duplicate task repository')
        repositories.add(repo['repository'])
        require(type(repo['commit']) is str and PIN.fullmatch(repo['commit']), 'immutable task source commit required')
        sha(repo['manifest_sha256'], 'task source manifest')


def validate_prior(prior):
    keys(prior, {'plan_id', 'plan_sha256', 'sample_sha256', 'sample'}, 'prior pilot')
    text(prior['plan_id'], 'prior pilot')
    sha(prior['plan_sha256'], 'prior pilot plan'); sha(prior['sample_sha256'], 'prior pilot sample')
    selected = prior['sample']
    keys(selected, {'schema', 'sampling_method', 'plan_sha256', 'frame_sha256', 'tasks'}, 'prior pilot sample')
    require(selected['schema'] == 'broader-claim-sample-v1' and selected['sampling_method'] == SAMPLING,
            'invalid prior pilot sample schema or sampling method')
    require(selected['plan_sha256'] == prior['plan_sha256'] and digest(selected) == prior['sample_sha256'],
            'prior pilot sample/plan binding differs')
    sha(selected['frame_sha256'], 'prior pilot frame')
    require(type(selected['tasks']) is list and selected['tasks'], 'actual nonempty prior pilot sample required')
    identities, fingerprints, systems = set(), set(), {}
    for task in selected['tasks']:
        validate_task(task)
        fingerprint = task_fingerprint(task)
        require(task['id'] not in identities and fingerprint not in fingerprints, 'duplicate prior pilot task')
        identities.add(task['id']); fingerprints.add(fingerprint)
        family = task['source_family']
        require(family not in systems or systems[family] == task['system'], 'prior family crosses coarser systems')
        systems[family] = task['system']


def validate_plan(plan):
    keys(plan, {'schema', 'plan_id', 'phase', 'arms', 'repetitions', 'sample_seed', 'frame_sha256',
                'corpus', 'strata', 'task_kind_weights', 'excluded', 'governance', 'readiness',
                'bootstrap', 'coverage'}, 'plan')
    require(plan['schema'] == 'broader-claim-plan-v1', 'unknown plan schema')
    text(plan['plan_id'], 'plan_id')
    require(plan['phase'] in ('pilot', 'confirmation'), 'invalid phase')
    require(plan['arms'] == ['A', 'B'], 'blinded arms must be exactly A and B')
    integer(plan['repetitions'], 'repetitions', upper=100)
    integer(plan['sample_seed'], 'sample_seed', lower=0, upper=(1 << 64) - 1)
    sha(plan['frame_sha256'], 'frame')
    require(type(plan['corpus']) is list and plan['corpus'], 'pinned corpus roster required')
    repos = set()
    for row in plan['corpus']:
        keys(row, {'repository', 'commit', 'manifest_sha256'}, 'repository')
        text(row['repository'], 'repository')
        require(row['repository'] not in repos, 'duplicate repository')
        repos.add(row['repository'])
        require(type(row['commit']) is str and PIN.fullmatch(row['commit']), 'immutable source commit required')
        sha(row['manifest_sha256'], 'source manifest')
    weights = plan['task_kind_weights']
    require(type(weights) is dict and len(weights) == 5, 'five frozen task-kind weights required')
    for kind, weight in weights.items():
        text(kind, 'task kind')
        number(weight, 'task-kind weight', upper=1)
        require(weight == 0.20, 'each task kind must retain weight 0.20')
    require(type(plan['strata']) is list and plan['strata'], 'sampling strata required')
    strata, cells, kinds = set(), set(), set()
    for row in plan['strata']:
        keys(row, {'id', 'domain', 'language', 'task_kind', 'count'}, 'stratum')
        for name in ('id', 'domain', 'language', 'task_kind'):
            text(row[name], name)
        integer(row['count'], 'stratum count')
        cell = (row['domain'], row['language'], row['task_kind'])
        require(row['id'] not in strata and cell not in cells, 'duplicate sampling stratum')
        strata.add(row['id']); cells.add(cell); kinds.add(row['task_kind'])
    require(kinds == set(weights), 'sampling strata and frozen task kinds differ')
    keys(plan['excluded'], {'source_families', 'witness_sha256s'}, 'excluded')
    unique_strings(plan['excluded']['source_families'], 'excluded families')
    unique_strings(plan['excluded']['witness_sha256s'], 'prior witnesses', hashes=True)
    governance = plan['governance']
    keys(governance, {'frozen_before_answers', 'sample_size_final', 'fixed_schedule', 'prior_pilot'}, 'governance')
    for name in ('frozen_before_answers', 'sample_size_final', 'fixed_schedule'):
        boolean(governance[name], name)
    prior = governance['prior_pilot']
    if plan['phase'] == 'pilot':
        require(prior is None, 'pilot cannot pool an earlier phase')
    else:
        validate_prior(prior)
        require(prior['plan_id'] != plan['plan_id'], 'pilot and confirmation must be separate plans')
    readiness = plan['readiness']
    keys(readiness, {'setup_ready', 'source_only_frame_review', 'source_pin_alignment_review',
                     'raw_capture_policy', 'independent_scoring_policy', 'blinding_review',
                     'comparability_policy'}, 'readiness')
    boolean(readiness['setup_ready'], 'setup_ready')
    for name in set(readiness) - {'setup_ready'}:
        if readiness[name] is not None:
            reference(readiness[name], name)
    bootstrap = plan['bootstrap']
    keys(bootstrap, {'method', 'seed', 'replicates', 'confidence', 'practical_margin'}, 'bootstrap')
    require(bootstrap['method'] == METHOD, 'unknown frozen bootstrap method')
    integer(bootstrap['seed'], 'bootstrap seed', lower=0, upper=(1 << 64) - 1)
    integer(bootstrap['replicates'], 'bootstrap replicates', lower=1000, upper=100000)
    require(type(bootstrap['confidence']) in (int, float) and bootstrap['confidence'] == 0.95,
            'confidence must be frozen at 0.95')
    require(type(bootstrap['practical_margin']) in (int, float) and bootstrap['practical_margin'] == 0.05,
            'practical margin must be frozen at 0.05')
    coverage = plan['coverage']
    keys(coverage, set(FLOORS) | {'max_cluster_share_per_kind'}, 'coverage')
    for name, floor in FLOORS.items():
        number(coverage[name], name)
        require(coverage[name] >= floor, 'broad-claim coverage guard cannot be weakened: ' + name)
    number(coverage['max_cluster_share_per_kind'], 'cluster dominance', upper=0.20)
    reasons = [name for name in ('frozen_before_answers', 'sample_size_final', 'fixed_schedule')
               if not governance[name]]
    if not readiness['setup_ready']:
        reasons.append('setup_not_ready')
    reasons.extend('missing_' + name for name in set(readiness) - {'setup_ready'} if readiness[name] is None)
    return {'schema': 'broader-claim-plan-validation-v1', 'valid': True,
            'plan_sha256': digest(plan), 'run_readiness': not reasons,
            'readiness_reasons': sorted(reasons), 'planned_unique_tasks': sum(s['count'] for s in plan['strata']),
            'planned_attempts': 2 * plan['repetitions'] * sum(s['count'] for s in plan['strata']),
            'limitations': LIMITATION}


def validate_frame(plan, frame):
    validate_plan(plan)
    keys(frame, {'schema', 'provenance', 'tasks'}, 'frame')
    require(frame['schema'] == 'broader-claim-frame-v1' and frame['provenance'] == 'source-only',
            'sampling frame must be source-only, independent of native retrieval hits')
    require(digest(frame) == plan['frame_sha256'], 'frozen frame binding differs')
    require(type(frame['tasks']) is list, 'candidate task list required')
    roster = {r['repository']: r for r in plan['corpus']}
    strata = {r['id']: r for r in plan['strata']}
    seen, fingerprints, families = set(), set(), {}
    for row in frame['tasks']:
        validate_task(row)
        require(row['id'] not in seen, 'duplicate task ID')
        seen.add(row['id'])
        fingerprint = task_fingerprint(row)
        require(fingerprint not in fingerprints, 'duplicate task prompt/rubric/source fingerprint')
        fingerprints.add(fingerprint)
        require(row['stratum'] in strata, 'unknown task stratum')
        target = strata[row['stratum']]
        require(all(row[k] == target[k] for k in ('domain', 'language', 'task_kind')), 'task stratum differs')
        family = row['source_family']
        require(family not in families or families[family] == row['system'], 'source family crosses coarser systems')
        families[family] = row['system']
        scoped = set()
        for repo in row['source_scope']:
            keys(repo, {'repository', 'commit', 'manifest_sha256'}, 'task source')
            identity = repo['repository']
            require(type(identity) is str and identity in roster and identity not in scoped,
                    'unknown or duplicate task repository')
            require(repo == roster[identity], 'task source pin or manifest differs from frozen roster')
            scoped.add(identity)


def sample(plan, frame):
    validate_frame(plan, frame)
    excluded = plan['excluded']
    prior = plan['governance']['prior_pilot']
    prior_tasks = prior['sample']['tasks'] if prior is not None else []
    prior_ids = {task['id'] for task in prior_tasks}
    prior_fingerprints = {task_fingerprint(task) for task in prior_tasks}
    families = set(excluded['source_families']) | {task['source_family'] for task in prior_tasks}
    witnesses = set(excluded['witness_sha256s']) | {w for task in prior_tasks for w in task['witness_sha256s']}
    eligible = [row for row in frame['tasks']
                if row['id'] not in prior_ids and task_fingerprint(row) not in prior_fingerprints and
                row['source_family'] not in families and not set(row['witness_sha256s']) & witnesses]
    chosen = []
    for stratum in sorted(plan['strata'], key=lambda row: row['id']):
        candidates = [row for row in eligible if row['stratum'] == stratum['id']]
        require(len(candidates) >= stratum['count'], 'insufficient unseen source-only candidates: ' + stratum['id'])
        priority = lambda row: (digest([plan['sample_seed'], stratum['id'], row['id']]), row['id'])
        chosen.extend(sorted(candidates, key=priority)[:stratum['count']])
    return {'schema': 'broader-claim-sample-v1', 'sampling_method': SAMPLING,
            'plan_sha256': digest(plan), 'frame_sha256': digest(frame), 'tasks': chosen}


def review(value, task_sha, final_sha, solver_ids, source=False):
    if value is None:
        return False
    expected = {'reviewer_id', 'independent', 'complete', 'task_sha256', 'final_sha256', 'artifact'}
    expected |= {'criteria'} if source else {'capture_integrity', 'classification_confirmed'}
    keys(value, expected, 'independent review')
    text(value['reviewer_id'], 'reviewer identity')
    require(value['task_sha256'] == task_sha and value['final_sha256'] == final_sha,
            'review task/final binding differs')
    reference(value['artifact'], 'review artifact')
    for name in ('independent', 'complete'):
        boolean(value[name], name)
    if source:
        keys(value['criteria'], CRITERIA, 'source correctness and claim-to-citation criteria')
        for item in value['criteria'].values():
            boolean(item, 'source criterion')
    else:
        boolean(value['capture_integrity'], 'capture integrity')
        boolean(value['classification_confirmed'], 'failure classification')
    return value['independent'] and value['complete'] and value['reviewer_id'] not in solver_ids


def task_values(plan, selected, results):
    keys(results, {'schema', 'plan_sha256', 'sample_sha256', 'shared_harness_defect', 'equal_access', 'attempts'}, 'results')
    require(results['schema'] == 'broader-claim-results-v1', 'unknown results schema')
    require(results['plan_sha256'] == digest(plan) and results['sample_sha256'] == digest(selected),
            'results do not bind frozen plan and sample')
    defect = results['shared_harness_defect']
    keys(defect, {'confirmed', 'review'}, 'shared harness defect')
    boolean(defect['confirmed'], 'shared harness defect')
    if defect['confirmed']:
        reference(defect['review'], 'shared harness defect review')
    else:
        require(defect['review'] is None, 'unconfirmed harness defect cannot assert review approval')
    access = results['equal_access']
    keys(access, {'established', 'review'}, 'equal access')
    boolean(access['established'], 'equal access established')
    if access['established'] or access['review'] is not None:
        reference(access['review'], 'equal access review')
    tasks = {t['id']: t for t in selected['tasks']}
    planned = {(task, arm, repeat) for task in tasks for arm in plan['arms']
               for repeat in range(1, plan['repetitions'] + 1)}
    require(type(results['attempts']) is list, 'explicit planned attempt list required')
    solver_ids = []
    for row in results['attempts']:
        require(type(row) is dict and 'solver_id' in row, 'attempt solver identity required')
        text(row['solver_id'], 'solver identity')
        solver_ids.append(row['solver_id'])
    require(len(set(solver_ids)) == len(solver_ids), 'each planned attempt requires a distinct declared solver identity')
    solver_ids = set(solver_ids)
    values, readiness, counts = {}, [], Counter()
    for row in results['attempts']:
        keys(row, {'task_id', 'arm', 'repetition', 'task_sha256', 'solver_id', 'outcome',
                   'failure_class', 'final', 'raw_review', 'source_review'}, 'attempt')
        integer(row['repetition'], 'planned repetition')
        key = (row['task_id'], row['arm'], row['repetition'])
        require(key in planned and key not in values, 'unknown or duplicate planned attempt')
        task = tasks[row['task_id']]
        require(row['task_sha256'] == digest(task), 'attempt prompt/rubric/source binding differs')
        text(row['solver_id'], 'solver identity')
        require(row['outcome'] in OUTCOMES, 'unknown attempt outcome')
        if row['final'] is not None:
            reference(row['final'], 'final artifact')
        final_sha = row['final']['sha256'] if row['final'] is not None else None
        answered = row['outcome'] == 'answered'
        if answered:
            require(final_sha is not None and row['failure_class'] is None, 'answered attempt needs final and no stop class')
        else:
            allowed = {'infrastructure_stop': {'infrastructure', 'shared_harness', 'unclassified'},
                       'budget_stop': {'budget'}, 'product_stop': {'product'},
                       'scope_stop': {'access', 'scope'},
                       'no_final': {'product', 'budget', 'infrastructure', 'shared_harness', 'unclassified', 'access', 'scope'}}
            require(row['failure_class'] in allowed[row['outcome']], 'stop outcome/classification mismatch')
            if row['outcome'] == 'no_final':
                require(final_sha is None, 'no_final is distinct from an existing final artifact')
            if row['failure_class'] == 'shared_harness':
                require(defect['confirmed'], 'shared harness cause lacks confirmed defect guard')
            if row['failure_class'] == 'unclassified':
                readiness.append('unclassified_failure')
            if row['failure_class'] == 'access':
                require(not access['established'], 'post-freeze access loss contradicts equal access declaration')
            if row['failure_class'] == 'scope':
                readiness.append('confirmed_scope_violation')
        raw_complete = review(row['raw_review'], row['task_sha256'], final_sha, solver_ids)
        source_complete = review(row['source_review'], row['task_sha256'], final_sha, solver_ids, source=True)
        raw_ok = raw_complete and row['raw_review']['capture_integrity'] and row['raw_review']['classification_confirmed']
        if not raw_ok:
            readiness.append('missing_or_incomplete_raw_review')
        if answered and not source_complete:
            readiness.append('missing_or_incomplete_source_review')
        verified = answered and raw_ok and source_complete and all(row['source_review']['criteria'].values())
        values[key] = int(verified)
        counts[row['arm'] + ':' + row['outcome']] += 1
    require(set(values) == planned, 'missing planned attempts; absent rows cannot become documented failures')
    if defect['confirmed']:
        readiness.append('confirmed_shared_harness_defect')
    if not access['established']:
        readiness.append('equal_access_not_established')
    aggregated = []
    for task in selected['tasks']:
        rates = {arm: sum(values[(task['id'], arm, repeat)] for repeat in range(1, plan['repetitions'] + 1)) /
                      plan['repetitions'] for arm in plan['arms']}
        aggregated.append(dict(task, success_A=rates['A'], success_B=rates['B'], delta=rates['A'] - rates['B']))
    return aggregated, readiness, dict(sorted(counts.items()))


def weighted_delta(tasks, weights, cluster_key=None, multipliers=None):
    kinds = defaultdict(list)
    if cluster_key is not None:
        require(type(multipliers) is dict and set(multipliers) == {t[cluster_key] for t in tasks},
                'exact shared cluster multiplier roster required')
        for value in multipliers.values():
            number(value, 'positive finite cluster multiplier')
    for task in tasks:
        kinds[task['task_kind']].append(task)
    require(set(kinds) == set(weights), 'task kinds do not match frozen weights')
    value = 0.0
    for kind, members in kinds.items():
        factors = [multipliers[t[cluster_key]] if cluster_key is not None else 1 for t in members]
        value += weights[kind] * math.fsum(factor * task['delta'] for factor, task in zip(factors, members)) / math.fsum(factors)
    require(math.isfinite(value), 'nonfinite weighted delta')
    return value


def percentile(values, probability):
    values = sorted(values)
    position = (len(values) - 1) * probability
    lower = math.floor(position)
    upper = math.ceil(position)
    return values[lower] + (values[upper] - values[lower]) * (position - lower)


def cluster_interval(tasks, weights, cluster_key, settings):
    clusters = sorted({t[cluster_key] for t in tasks})
    seed = int(digest([settings['seed'], cluster_key]), 16)
    generator = random.Random(seed)
    draws = []
    for _ in range(settings['replicates']):
        multipliers = {cluster: -math.log1p(-max(generator.random(), 2 ** -53)) for cluster in clusters}
        # ONE positive weight is shared across both arms, all repetitions, and
        # every kind/stratum represented by this family or system.
        draws.append(weighted_delta(tasks, weights, cluster_key, multipliers))
    tail = (1 - settings['confidence']) / 2
    # Influence is the derivative of the fixed-kind weighted ratio mean at
    # unit multipliers. Zero observed influence cannot identify unseen tails.
    influence = dict.fromkeys(clusters, 0.0)
    for kind in weights:
        members = [task for task in tasks if task['task_kind'] == kind]
        mean = math.fsum(task['delta'] for task in members) / len(members)
        for task in members:
            influence[task[cluster_key]] += weights[kind] * (task['delta'] - mean) / len(members)
    dispersion = math.fsum(value ** 2 for value in influence.values())
    lower, upper = percentile(draws, tail), percentile(draws, 1 - tail)
    return {'cluster_key': cluster_key, 'clusters': len(clusters), 'method': METHOD,
            'confidence': settings['confidence'], 'approximate': True,
            'lower': lower, 'upper': upper, 'observed_influence_sum_squares': dispersion,
            'uncertainty_estimable': dispersion > 1e-24 and upper - lower > 1e-12}


def coverage_checks(tasks, settings):
    reasons, details = [], {}
    for grain, prefix, global_min in [('source_family', 'family', 'families'), ('system', 'system', 'systems')]:
        total = len({t[grain] for t in tasks})
        if total < settings[global_min]:
            reasons.append('insufficient_' + global_min)
        per_kind = {}
        for kind in sorted({t['task_kind'] for t in tasks}):
            members = [t for t in tasks if t['task_kind'] == kind]
            counts = Counter(t[grain] for t in members)
            shares = [count / len(members) for count in counts.values()]
            effective = 1 / math.fsum(share ** 2 for share in shares)
            maximum = max(shares)
            minimum = settings[('families' if prefix == 'family' else 'systems') + '_per_kind']
            if len(counts) < minimum or effective + 1e-12 < settings[prefix + '_effective_per_kind'] or maximum > settings['max_cluster_share_per_kind'] + 1e-12:
                reasons.append('insufficient_or_dominated_' + prefix + '_clusters:' + kind)
            per_kind[kind] = {'clusters': len(counts), 'kish_effective_clusters': effective, 'max_weight_share': maximum}
        details[grain] = {'clusters': total, 'per_kind': per_kind}
    return reasons, details


def analyze(plan, frame, selected, results):
    validation = validate_plan(plan)
    require(selected == sample(plan, frame), 'selected roster differs from deterministic frozen source-only sample')
    tasks, audit_reasons, counts = task_values(plan, selected, results)
    coverage_reasons, coverage = coverage_checks(tasks, plan['coverage'])
    reasons = validation['readiness_reasons'] + audit_reasons + coverage_reasons
    if plan['phase'] == 'pilot':
        reasons.append('pilot_is_exploratory_not_confirmation')
    intervals = [cluster_interval(tasks, plan['task_kind_weights'], grain, plan['bootstrap'])
                 for grain in ('source_family', 'system')]
    reasons.extend('uncertainty_not_estimable:' + interval['cluster_key']
                   for interval in intervals if not interval['uncertainty_estimable'])
    margin = plan['bootstrap']['practical_margin']
    winner = None
    if not reasons:
        if all(interval['lower'] > margin for interval in intervals):
            winner = 'A'
        elif all(interval['upper'] < -margin for interval in intervals):
            winner = 'B'
    kinds = defaultdict(list)
    for task in tasks:
        kinds[task['task_kind']].append(task)
    return {'schema': 'broader-claim-analysis-v1', 'plan_sha256': digest(plan),
            'frame_sha256': digest(frame), 'sample_sha256': digest(selected), 'results_sha256': digest(results),
            'phase': plan['phase'], 'status': 'readiness_not_met' if reasons else ('conditional_practical_lead' if winner else 'inconclusive'),
            'run_readiness': not reasons, 'readiness_reasons': sorted(set(reasons)), 'conditional_leading_arm': winner,
            'primary': 'task-level paired declared verified success; all planned stops are zero',
            'point_delta_A_minus_B': weighted_delta(tasks, plan['task_kind_weights']),
            'unique_tasks': len(tasks), 'planned_attempts': len(tasks) * 2 * plan['repetitions'],
            'repetitions_per_arm_task': plan['repetitions'], 'attempt_outcomes': counts,
            'task_kind_weights': plan['task_kind_weights'], 'coverage_requirements': plan['coverage'], 'coverage': coverage,
            'per_kind': {kind: {'unique_tasks': len(rows), 'A': math.fsum(t['success_A'] for t in rows) / len(rows),
                               'B': math.fsum(t['success_B'] for t in rows) / len(rows)} for kind, rows in sorted(kinds.items())},
            'cluster_intervals': intervals, 'bootstrap': plan['bootstrap'], 'limitations': LIMITATION}


def load(path):
    def pairs(items):
        value = {}
        for key, item in items:
            require(key not in value, 'duplicate JSON key: ' + key)
            value[key] = item
        return value
    def constant(value):
        raise ValueError('nonfinite JSON constant: ' + value)
    with Path(path).open() as stream:
        return json.load(stream, object_pairs_hook=pairs, parse_constant=constant)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    for name in ('validate-plan', 'sample', 'analyze'):
        command = sub.add_parser(name)
        command.add_argument('--plan', required=True)
        command.add_argument('--output')
        if name != 'validate-plan':
            command.add_argument('--frame', required=True)
        if name == 'analyze':
            command.add_argument('--sample', required=True)
            command.add_argument('--results', required=True)
    args = parser.parse_args(argv)
    try:
        plan = load(args.plan)
        if args.command == 'validate-plan':
            result = validate_plan(plan)
        elif args.command == 'sample':
            result = sample(plan, load(args.frame))
        else:
            result = analyze(plan, load(args.frame), load(args.sample), load(args.results))
        output = json.dumps(result, sort_keys=True, indent=2, allow_nan=False) + '\n'
        if args.output:
            with Path(args.output).open('x') as stream:
                stream.write(output)
        else:
            sys.stdout.write(output)
        return 0
    except (ValueError, TypeError, KeyError, OSError) as error:
        parser.exit(2, 'broader claim validation failed: ' + str(error) + '\n')


if __name__ == '__main__':
    sys.exit(main())
