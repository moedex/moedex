"""Shared frozen native comparison contract validation; no execution or I/O policy.

The caller supplies its hash-checking evidence reader. Repository identities are
exact strings: aliases and access boundaries require independent source review.
"""
import math
import re


GIT_OBJECT = re.compile(r'[0-9a-f]{40}|[0-9a-f]{64}')


def require(ok, message):
    if not ok:
        raise ValueError(message)


def identities(values, label):
    require(isinstance(values, list) and values, label + ' must be a nonempty list')
    require(all(isinstance(value, str) and value.strip() == value and value
                for value in values), label + ' contains an invalid identity')
    require(len(set(values)) == len(values), label + ' contains a duplicate identity')
    return set(values)


def validate(contract, read_bytes):
    """Verify every contract reference and return tasks indexed by exact identity.

    v1 retains its single-repository shape. v2 explicitly pins every repository
    and declares each task's nonempty subset; no synthetic aggregate commit is
    accepted. Neither schema proves source manifest completeness or task truth.
    """
    require(isinstance(contract, dict), 'contract must be an object')
    schema = contract.get('schema')
    require(schema in ('native-pair-v1', 'native-pair-v2'), 'unknown contract schema')
    corpus = contract.get('corpus')
    require(isinstance(corpus, dict), 'corpus must be an object')
    if schema == 'native-pair-v2':
        require(set(corpus) == {'repositories'}, 'v2 corpus must contain only repositories')
        sources = corpus['repositories']
        require(isinstance(sources, list) and sources, 'empty repository roster')
    else:
        sources = [corpus]
    repositories = []
    for source in sources:
        require(isinstance(source, dict), 'repository pin must be an object')
        if schema == 'native-pair-v2':
            require(set(source) == {'repository', 'commit', 'manifest'},
                    'v2 repository pin must contain repository, commit and manifest')
        repository, commit = source.get('repository'), source.get('commit')
        require(isinstance(repository, str) and repository and repository.strip() == repository and
                isinstance(commit, str) and GIT_OBJECT.fullmatch(commit), 'unpinned corpus')
        repositories.append(repository)
        require('manifest' in source, 'missing source manifest')
        read_bytes(source['manifest'])
    known = identities(repositories, 'repository roster')
    for key in ('rubric', 'protocol'):
        require(key in contract, 'missing ' + key)
        read_bytes(contract[key])
    rows = contract.get('tasks')
    require(isinstance(rows, list) and rows, 'empty task roster')
    require(all(isinstance(row, dict) for row in rows), 'task must be an object')
    identities([row.get('id') for row in rows], 'contract tasks')
    tasks = {row['id']: row for row in rows}
    for task in tasks.values():
        require('prompt' in task, 'missing task prompt')
        read_bytes(task['prompt'])
        identities(task.get('atoms'), 'atom roster')
        if schema == 'native-pair-v2':
            scope = identities(task.get('repositories'), 'task repository scope')
            require(scope <= known, 'task repository scope contains an unknown repository')
    budget = contract.get('budgets')
    require(isinstance(budget, dict), 'budgets must be an object')
    for key in ('calls', 'response_bytes', 'assignment_seconds', 'display_bytes'):
        value = budget.get(key)
        require(type(value) in (int, float) and math.isfinite(value) and value > 0,
                key + ' must be a finite positive number')
        require(key == 'assignment_seconds' or type(value) is int, key + ' must be an integer')
    return tasks
