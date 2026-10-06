#!/usr/bin/env python3
"""Strict offline projection of closed independent reviews; never scores answers.

No prompts, rubric, gold, provider/native bodies, or source are interpreted.
References to such evidence are hash-checked as opaque bytes only. Immutable
original review files remain authoritative; this tool never edits input files.
"""
import argparse
import base64
import copy
from datetime import datetime
import hashlib
import json
import math
import os
from pathlib import Path, PurePosixPath
import re
import sys
import types

sys.dont_write_bytecode = True
SHA = re.compile(r'^[0-9a-f]{64}$')
UTC = re.compile(r'^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$')
NAME = re.compile(r'^[a-zA-Z0-9][a-zA-Z0-9_.-]*$')
SCHEMA_PATH = 'evidence/research/semantic-intelligence/agent-journeys/broader_claim.py'
RAW_FIELDS = {'reviewer_id', 'independent', 'complete', 'task_sha256', 'final_sha256',
              'capture_integrity', 'classification_confirmed'}
SOURCE_FIELDS = {'reviewer_id', 'independent', 'complete', 'task_sha256', 'final_sha256', 'criteria'}
TERMINAL_REVIEW_STATUSES = {'closed', 'complete', 'completed', 'reviewed', 'final', 'finalized', 'sealed'}
HISTORY_FLAGS = {'no_task_authorship', 'no_prospective_source_or_gold_review', 'no_feedback_to_solver'}
OMISSION_FIELDS = {'repository', 'path', 'source_line', 'body_field', 'native_ordinal', 'utf8_bytes', 'sha256'}
MANUAL_DIAGNOSTICS = {'sensitive_safe_representation', 'sensitive_literal_values_directly_read',
                      'all_retained_provider_native_bodies_read_basis', 'sensitive_literal_omissions',
                      'omitted_context_lines', 'raw_body_review_representation',
                      'sensitive_literal_display_omissions', 'sensitive_literal_representation_limitation',
                      'nonterminal_client_error', 'diagnostics'}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def load(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, 'duplicate JSON key: ' + key)
            result[key] = value
        return result
    def constant(value):
        raise ValueError('nonfinite JSON constant: ' + value)
    return json.loads(raw, object_pairs_hook=pairs, parse_constant=constant)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False,
                      allow_nan=False).encode()


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def nonempty(value, label):
    require(isinstance(value, str) and bool(value.strip()), label + ': nonempty text required')


def local(root, name):
    require(type(name) is str and name and '\\' not in name, 'invalid local path')
    path = PurePosixPath(name)
    require(not path.is_absolute() and '..' not in path.parts and name == path.as_posix() and
            name != '.', 'confined normalized relative path required: ' + name)
    cursor = root
    for part in path.parts:
        cursor = cursor / part
        require(not cursor.is_symlink(), 'symlink rejected: ' + name)
    return cursor


def read_ref(root, ref):
    require(type(ref) is dict and set(ref) == {'path', 'sha256'}, 'exact path/sha256 reference required')
    require(type(ref['sha256']) is str and SHA.fullmatch(ref['sha256']), 'invalid reference SHA256')
    path = local(root, ref['path'])
    require(path.is_file(), 'missing referenced file: ' + ref['path'])
    raw = path.read_bytes()
    require(sha(raw) == ref['sha256'], 'reference hash mismatch: ' + ref['path'])
    return raw


def ref_for(root, name):
    path = local(root, name)
    require(path.is_file(), 'missing artifact: ' + name)
    ref = {'path': name, 'sha256': sha(path.read_bytes())}
    read_ref(root, ref)
    return ref


def selected_body(root, value):
    """Verify a journal selector as opaque bytes, with an explicit hash domain."""
    require(type(value) is dict and set(value) == {'path', 'sha256', 'field', 'line'},
            'exact journal body selector required')
    require(type(value['path']) is str, 'body selector path must be text')
    parts = PurePosixPath(value['path']).parts
    require(len(parts) == 3 and parts[0] == 'captures' and NAME.fullmatch(parts[1]) and
            parts[2] == 'events.jsonl', 'body selector must name an owned capture journal')
    require(value['field'] == 'body_base64' and type(value['line']) is int and value['line'] > 0,
            'positive journal line and body_base64 selector required')
    require(type(value['sha256']) is str and SHA.fullmatch(value['sha256']), 'invalid body SHA256')
    artifact = ref_for(root, value['path'])
    lines = read_ref(root, artifact).splitlines()
    require(value['line'] <= len(lines), 'selected journal line is absent')
    event = load(lines[value['line'] - 1])
    require(type(event) is dict and type(event.get('body_base64')) is str, 'selected body is absent')
    try:
        raw = base64.b64decode(event['body_base64'], validate=True)
    except (ValueError, TypeError):
        raise ValueError('invalid selected body encoding') from None
    require(sha(raw) == value['sha256'], 'selected body hash mismatch')
    return raw, artifact, event.get('channel')


def references(root, value, own_path=None):
    """Validate exact file references recursively; retain findings without rewriting."""
    refs = []
    if type(value) is dict:
        if OMISSION_FIELDS <= set(value):
            # This exact typed descriptor hashes omitted source text, not a
            # root-relative file. The independent raw receipt owns its meaning.
            require(set(value) in (OMISSION_FIELDS, OMISSION_FIELDS | {'display_line_offset'}),
                    'unknown source omission descriptor fields')
            for key in ('repository', 'path', 'body_field'):
                nonempty(value[key], 'source omission ' + key)
            path = PurePosixPath(value['path'])
            require(not path.is_absolute() and '..' not in path.parts and
                    path.as_posix() == value['path'] and '\\' not in value['path'],
                    'normalized repository source omission path required')
            require(type(value['sha256']) is str and SHA.fullmatch(value['sha256']),
                    'source omission hash required')
            for key in ('source_line', 'native_ordinal', 'utf8_bytes'):
                require(type(value[key]) is int and value[key] > 0, 'positive source omission ' + key)
            if 'display_line_offset' in value:
                require(type(value['display_line_offset']) is int and value['display_line_offset'] >= 0,
                        'nonnegative source omission display offset required')
            return refs
        if 'path' in value and 'sha256' in value:
            require(value['path'] != own_path, 'review artifact must not reference/hash itself')
            if set(value) == {'path', 'sha256', 'field', 'line'}:
                _, artifact, _ = selected_body(root, value)
                refs.append(artifact)
            else:
                require(set(value) in ({'path', 'sha256'}, {'path', 'sha256', 'bytes'}),
                        'file reference has extra fields')
                artifact = {key: value[key] for key in ('path', 'sha256')}
                raw = read_ref(root, artifact)
                if 'bytes' in value:
                    require(type(value['bytes']) is int and value['bytes'] == len(raw),
                            'file reference byte length mismatch')
                refs.append(artifact)
        else:
            for item in value.values():
                refs.extend(references(root, item, own_path))
    elif type(value) is list:
        for item in value:
            refs.extend(references(root, item, own_path))
    return refs


def has_ref(refs, ref, label):
    require(ref in refs, 'missing exact ' + label + ' evidence reference')


def closed(review, label):
    require(type(review) is dict, label + ': object required')
    require('original_decision' in review and bool(review['original_decision']), label + ': original decision required')
    def terminal_statuses(value):
        for key in ('status', 'review_status', 'manual_review', 'semantic_source_review'):
            if key not in value:
                continue
            status = value[key]
            if key in ('manual_review', 'semantic_source_review') and type(status) is dict:
                terminal_statuses(status)
            else:
                require(type(status) is str and status.strip().lower() in TERMINAL_REVIEW_STATUSES,
                        label + ': pending review or nonterminal review status cannot be assembled: ' + key)
    terminal_statuses(review)


def declaration(root, ref, kind, packet_ref, packet, solvers):
    value = load(read_ref(root, ref))
    closed(value, kind)
    expected = {'schema', 'packet', 'plan', 'sample', 'reviewer_id', 'independent', 'complete',
                'evidence_refs', 'reasoning', 'original_decision', 'observed_utc'}
    expected |= {'established'} if kind == 'equal-access' else {'confirmed', 'reviewed_causes'}
    require(set(value) == expected, kind + ': unexpected or missing fields')
    require(value['schema'] == 'broader-pilot-postrun-' + kind + '-review-v1', kind + ': unknown schema')
    nonempty(value['reviewer_id'], kind + ' reviewer')
    require(value['reviewer_id'] not in solvers and value['independent'] is True and value['complete'] is True,
            kind + ': closed independent review required')
    require(value['packet'] == packet_ref and value['plan'] == packet['plan'] and value['sample'] == packet['sample'],
            kind + ': immutable packet/plan/sample join differs')
    require(type(value['observed_utc']) is str and UTC.fullmatch(value['observed_utc']), kind + ': UTC required')
    nonempty(value['reasoning'], kind + ' reasoning')
    flag = 'established' if kind == 'equal-access' else 'confirmed'
    require(type(value[flag]) is bool, kind + ': explicit boolean required')
    require(type(value['evidence_refs']) is list and value['evidence_refs'], kind + ': evidence required')
    refs = references(root, value, ref['path'])
    if kind == 'shared-harness':
        require(type(value['reviewed_causes']) is list, 'shared-harness: explicit cause roster required')
        require(value['confirmed'] or not value['reviewed_causes'], 'unconfirmed harness cannot assert shared causes')
    return value, refs


def raw_review_selections(root, selection_ref, packet_ref, packet, slots, solvers):
    """Validate explicitly approved mechanical corrections, never select by age."""
    if selection_ref is None:
        return {}
    manifest = load(read_ref(root, selection_ref))
    require(type(manifest) is dict and set(manifest) == {'schema', 'packet', 'plan', 'sample', 'selections'} and
            manifest['schema'] == 'broader-pilot-raw-review-selection-v1', 'exact raw-review selection manifest required')
    require(manifest['packet'] == packet_ref and manifest['plan'] == packet['plan'] and manifest['sample'] == packet['sample'],
            'raw-review selection packet/plan/sample binding differs')
    require(type(manifest['selections']) is list and manifest['selections'], 'explicit nonempty raw-review selection list required')
    references(root, manifest, selection_ref['path'])
    known = {slot['assignment']: slot for slot in slots}
    selections = {}
    for entry in manifest['selections']:
        require(type(entry) is dict and set(entry) == {'assignment', 'original', 'selected', 'reason', 'independent_review'},
                'exact raw-review selection entry required')
        name = entry['assignment']
        require(type(name) is str and name in known and name not in selections,
                'unknown or duplicate raw-review selection assignment')
        nonempty(entry['reason'], name + ' raw-review selection reason')
        original_ref, selected_ref = entry['original'], entry['selected']
        require(original_ref['path'] == 'reviews/attempt-raw/' + name + '.json',
                name + ': selection original must be the immutable canonical raw review')
        require(selected_ref['path'].startswith('reviews/attempt-raw-corrections/' + name + '.') and
                selected_ref['path'] != original_ref['path'], name + ': explicit separate correction artifact required')
        original = load(read_ref(root, original_ref))
        selected = load(read_ref(root, selected_ref))
        closed(original, name + ' original raw review')
        closed(selected, name + ' selected raw review')
        require(original.get('schema') == 'broader-pilot-assignment-raw-review-v1' and
                original.get('assignment') == name, name + ': selection original raw identity differs')
        # Reconstruct the only allowed value changes. Canonical JSON comparison
        # preserves JSON types (True cannot silently become 1) and every field.
        expected = copy.deepcopy(original)
        basis = original.get('classification_basis')
        if type(basis) is dict:
            nonempty(basis.get('basis'), name + ' original structured classification basis')
            require('classification_details' not in original, name + ': existing classification details cannot be replaced')
            expected['classification_basis'] = basis['basis']
            expected['classification_details'] = copy.deepcopy(basis)
        else:
            nonempty(basis, name + ' original classification basis')
        manual = original.get('manual_review')
        require(type(manual) is dict and 'notes' in manual, name + ': original manual review notes required')
        notes = manual['notes']
        if type(notes) is list:
            require(notes and all(type(item) is str for item in notes) and ' '.join(notes).strip(),
                    name + ': original notes must be a nonempty string list')
            expected['manual_review']['notes'] = ' '.join(notes)
        else:
            nonempty(notes, name + ' original manual notes')
        old_refs, new_refs = original.get('evidence_refs'), selected.get('evidence_refs')
        require(type(old_refs) is list and type(new_refs) is list and len(new_refs) >= len(old_refs) and
                canonical(new_refs[:len(old_refs)]) == canonical(old_refs),
                name + ': original evidence-ref prefix must be retained exactly')
        additions = new_refs[len(old_refs):]
        allowed = [packet['plan'], packet['sample'], original_ref]
        require(all(item in allowed and item not in old_refs for item in additions) and
                len({canonical(item) for item in additions}) == len(additions) and all(item in new_refs for item in allowed),
                name + ': correction may append only missing exact plan/sample/original bindings')
        expected['evidence_refs'] = copy.deepcopy(new_refs)
        require('formatting_correction' not in original, name + ': original correction provenance cannot be replaced')
        correction = selected.get('formatting_correction')
        require(type(correction) is dict and set(correction) == {'schema', 'original', 'observed_utc', 'reason', 'preserved_prior_corrections'} and
                correction['schema'] == 'raw-review-formatting-correction-v1' and correction['original'] == original_ref,
                name + ': exact bounded mechanical correction provenance required')
        nonempty(correction['reason'], name + ' formatting correction reason')
        require(type(correction['observed_utc']) is str and UTC.fullmatch(correction['observed_utc']),
                name + ': formatting correction UTC required')
        prior = correction['preserved_prior_corrections']
        require(type(prior) is list and len({canonical(item) for item in prior}) == len(prior),
                name + ': unique preserved prior correction refs required')
        for prior_ref in prior:
            read_ref(root, prior_ref)
            require(prior_ref['path'].startswith('reviews/attempt-raw-corrections/' + name + '.') and
                    prior_ref != selected_ref and prior_ref != original_ref,
                    name + ': prior correction must be an exact separate own-assignment artifact')
        expected['formatting_correction'] = copy.deepcopy(correction)
        require(canonical(selected) == canonical(expected),
                name + ': selected raw review changes a decision, primitive field, original decision, or unrelated value')
        original_evidence = references(root, original, original_ref['path'])
        references(root, selected, selected_ref['path'])
        approval_ref = entry['independent_review']
        approval = load(read_ref(root, approval_ref))
        closed(approval, name + ' mechanical amendment approval')
        approval_fields = {'schema', 'packet', 'plan', 'sample', 'assignment', 'original', 'selected', 'reason',
                           'reviewer_id', 'independent', 'complete', 'approved', 'reasoning', 'original_decision', 'observed_utc'}
        require(set(approval) == approval_fields and approval['schema'] == 'broader-pilot-raw-review-mechanical-amendment-review-v1',
                name + ': exact independent mechanical amendment approval required')
        require(approval['packet'] == packet_ref and approval['plan'] == packet['plan'] and approval['sample'] == packet['sample'] and
                all(canonical(approval[key]) == canonical(entry[key]) for key in ('assignment', 'original', 'selected', 'reason')),
                name + ': independent correction approval binding differs')
        nonempty(approval['reviewer_id'], name + ' amendment reviewer')
        require(approval['reviewer_id'] not in solvers and approval['reviewer_id'] != original.get('reviewer_id') and
                approval['independent'] is True and approval['complete'] is True and approval['approved'] is True,
                name + ': closed independent approved mechanical amendment required')
        nonempty(approval['reasoning'], name + ' amendment approval reasoning')
        require(type(approval['observed_utc']) is str and UTC.fullmatch(approval['observed_utc']), name + ': amendment approval UTC required')
        references(root, approval, approval_ref['path'])
        selections[name] = dict(copy.deepcopy(entry), original_evidence_refs=original_evidence)
    return selections


def review_artifact(root, name, kind, slot, solvers, binding_refs, expected_final, selected_ref=None):
    require(selected_ref is None or kind == 'raw', 'only raw mechanical corrections can be selected')
    ref = selected_ref if selected_ref is not None else ref_for(root, 'reviews/attempt-' + kind + '/' + name + '.json')
    value = load(read_ref(root, ref))
    closed(value, name + ' ' + kind)
    require(value.get('schema') == 'broader-pilot-assignment-' + kind + '-review-v1', name + ': wrong review schema')
    for field in ('assignment', 'solver_id', 'task_sha256'):
        expected = name if field == 'assignment' else slot[field]
        require(value.get(field) == expected, name + ': review ' + field + ' join differs')
    reviewer = value.get('reviewer_id')
    nonempty(reviewer, name + ' reviewer')
    require(reviewer not in solvers, name + ': reviewer is a cohort solver')
    field_name = 'raw_review_fields' if kind == 'raw' else 'source_review_fields'
    fields = value.get(field_name)
    require(type(fields) is dict and set(fields) == (RAW_FIELDS if kind == 'raw' else SOURCE_FIELDS),
            name + ': schema-ready review fields required; artifact self hash is forbidden')
    require(fields['reviewer_id'] == reviewer and fields['independent'] is True,
            name + ': review identity/independence differs')
    require(type(fields['complete']) is bool, name + ': evidence completeness must be explicit')
    final_sha = expected_final['sha256'] if expected_final else None
    supplied_final = value.get('final')
    final_matches = supplied_final == expected_final
    if kind == 'source' and expected_final and type(supplied_final) is dict and set(supplied_final) == {'path', 'sha256', 'field', 'line'}:
        body, _, channel = selected_body(root, supplied_final)
        final_matches = (supplied_final['path'] == slot['capture'] + '/events.jsonl' and channel == 'answer' and
                         body == read_ref(root, expected_final))
    require(fields['task_sha256'] == slot['task_sha256'] and fields['final_sha256'] == final_sha and
            final_matches, name + ': exact task/final review join differs')
    require(type(value.get('evidence_refs')) in (list, dict), name + ': exact review evidence refs required')
    refs = references(root, value, ref['path'])
    if expected_final and supplied_final != expected_final:
        refs.append(copy.deepcopy(expected_final))  # Exact byte equality above; never a replacement answer.
    for label, required_ref in binding_refs.items():
        has_ref(refs, required_ref, label)
    if expected_final:
        has_ref(refs, expected_final, 'own final')
    result = copy.deepcopy(fields)
    result['artifact'] = ref
    return value, result, refs


def terminal_evidence(root, slot):
    """Join terminal driver records and retained answer ledger, without answer text."""
    name = slot['assignment']
    intent_ref = ref_for(root, 'execution/' + name + '.intent.json')
    result_ref = ref_for(root, 'execution/' + name + '.result.json')
    intent, result = load(read_ref(root, intent_ref)), load(read_ref(root, result_ref))
    require(intent.get('assignment') == name and result.get('assignment') == name, name + ': driver join differs')
    require(type(intent.get('wave')) is int and intent['wave'] > 0 and
            type(intent.get('launched_unix')) in (int, float) and math.isfinite(intent['launched_unix']),
            name + ': valid launch intent required')
    if result.get('exit_code') is None:
        require(set(result) == {'assignment', 'exit_code', 'launch_failed_errno'} and
                (result['launch_failed_errno'] is None or type(result['launch_failed_errno']) is int),
                name + ': closed independently classifiable launch failure required')
    else:
        require(set(result) == {'assignment', 'exit_code', 'finished_unix'} and
                type(result['exit_code']) is int and type(result['finished_unix']) in (int, float) and
                math.isfinite(result['finished_unix']) and result['finished_unix'] >= intent['launched_unix'],
                name + ': terminal driver result required')
    refs = {'driver intent': intent_ref, 'driver result': result_ref}
    diagnostics = {'driver_intent': intent_ref, 'driver_result': result_ref, 'event_refs': []}
    assignment_path = local(root, name + '/assignment.json')
    final = None
    if assignment_path.exists():
        assignment_ref = ref_for(root, name + '/assignment.json')
        assignment = load(read_ref(root, assignment_ref))
        identity = assignment.get('identity', {})
        require(assignment.get('assignment') == name and identity.get('task') == slot['task_id'] and
                identity.get('arm') == slot['arm'] and identity.get('solver_id') == slot['solver_id'],
                name + ': recorded assignment identity differs')
        refs['assignment'] = assignment_ref
        refs['freeze'] = assignment['freeze']
        read_ref(root, assignment['freeze'])
        diagnostics['assignment'] = assignment_ref
        diagnostics['freeze'] = assignment['freeze']
    events = local(root, name + '/events')
    if events.exists():
        require(events.is_dir(), name + ': events must be a directory')
        for event_path in sorted(events.iterdir()):
            require(re.fullmatch(r'\d{6}\.json', event_path.name), name + ': unexpected event artifact')
            event_ref = ref_for(root, name + '/events/' + event_path.name)
            event = load(read_ref(root, event_ref))
            diagnostics['event_refs'].append(event_ref)
            if event.get('event') == 'answer':
                answer = event.get('answer')
                require(type(answer) is dict and set(answer) == {'path', 'sha256'}, name + ': invalid own answer reference')
                # Recorders may retain root-relative or ledger-local references.
                # Both forms must stay within this assignment's blob directory.
                path = answer['path']
                require(type(path) is str, name + ': invalid answer path')
                path = path if path.startswith(name + '/') else name + '/' + path
                local(root, path)
                require(path.startswith(name + '/blobs/'), name + ': answer is outside own blobs')
                final = {'path': path, 'sha256': answer['sha256']}
                read_ref(root, final)
                refs['final answer event'] = event_ref
    inventory_path = local(root, slot['capture'] + '/inventory.json')
    if inventory_path.exists():
        refs['capture inventory'] = ref_for(root, slot['capture'] + '/inventory.json')
        diagnostics['capture_inventory'] = refs['capture inventory']
    diagnostics['recorded_final'] = final
    return final, refs, diagnostics


def source_history_attestations(root, manifest_ref, packet_ref, slots, solvers):
    """Bind explicit author declarations to original files; never infer history."""
    if manifest_ref is None:
        return {}
    manifest = load(read_ref(root, manifest_ref))
    require(type(manifest) is dict and set(manifest) == {'schema', 'packet', 'attestations'} and
            manifest['schema'] == 'broader-pilot-source-history-manifest-v1' and
            manifest['packet'] == packet_ref, 'exact packet-bound source history manifest required')
    refs = manifest['attestations']
    require(type(refs) is list and refs, 'explicit nonempty source history attestations required')
    result, authors = {}, set()
    originals = {}
    for slot in slots:
        ref = ref_for(root, 'reviews/attempt-source/' + slot['assignment'] + '.json')
        value = load(read_ref(root, ref))
        originals[ref['path']] = (ref, value.get('reviewer_id'))
    for ref in refs:
        value = load(read_ref(root, ref))
        require(type(value) is dict and set(value) == {'schema', 'reviewer_id', 'packet', 'review_history',
                                                     'original_review_refs', 'disclosure', 'original_decision', 'observed_utc'} and
                value['schema'] == 'fresh-pilot-semantic-reviewer-history-attestation-v1',
                'exact semantic reviewer history attestation required')
        author = value['reviewer_id']
        nonempty(author, 'history attestation author')
        require(author not in authors and author not in solvers and value['packet'] == packet_ref,
                'duplicate/solver author or foreign history packet')
        history = value['review_history']
        require(type(history) is dict and set(history) == HISTORY_FLAGS and
                all(history[key] is True for key in HISTORY_FLAGS), 'explicit fresh history flags required')
        nonempty(value['disclosure'], 'history disclosure')
        require(bool(value['original_decision']), 'original history decision required')
        timestamp = value['observed_utc']
        require(type(timestamp) is str and 'T' in timestamp and timestamp.endswith(('Z', '+00:00')),
                'history attestation UTC required')
        observed = datetime.fromisoformat(timestamp.replace('Z', '+00:00'))
        require(observed.tzinfo is not None and observed.utcoffset().total_seconds() == 0,
                'history attestation UTC required')
        declared = value['original_review_refs']
        require(type(declared) is list and declared, 'explicit original review refs required')
        paths = set()
        for original in declared:
            read_ref(root, original)
            path = original['path']
            require(path not in paths and path in originals and
                    originals[path] == (original, author), 'duplicate/foreign/changed history original review')
            paths.add(path)
        require(paths == {path for path, (_, reviewer) in originals.items() if reviewer == author},
                'history attestation must cover exactly the author original review roster')
        for path in paths:
            result[path] = copy.deepcopy(ref)
        authors.add(author)
    return result


def assemble(root, packet_ref, access_ref, harness_ref, schema_sha256, raw_selection_ref=None, history_manifest_ref=None):
    root = Path(root).resolve()
    schema_ref = {'path': SCHEMA_PATH, 'sha256': schema_sha256}
    schema_raw = read_ref(root, schema_ref)
    schema = types.ModuleType('neutral_results_schema')
    exec(compile(schema_raw, str(local(root, SCHEMA_PATH)), 'exec'), schema.__dict__)
    packet = load(read_ref(root, packet_ref))
    require(packet.get('schema') == 'broader-pilot-launch-packet-v1' and packet.get('phase') == 'pilot',
            'actual immutable pilot packet required')
    plan = load(read_ref(root, packet['plan']))
    selected = load(read_ref(root, packet['sample']))
    schema.validate_plan(plan)
    require(selected.get('schema') == 'broader-claim-sample-v1' and selected.get('sampling_method') == schema.SAMPLING and
            selected.get('plan_sha256') == schema.digest(plan) and selected.get('frame_sha256') == plan['frame_sha256'],
            'sample/plan/frame binding differs')
    require(plan['phase'] == 'pilot' and plan['arms'] == ['A', 'B'] and plan['repetitions'] == 3,
            'exact pilot arms/repetitions required')
    require(type(selected.get('tasks')) is list and len(selected['tasks']) == 60, 'exact actual 60-task sample required')
    tasks = {}
    for task in selected['tasks']:
        schema.validate_task(task)
        require(task['id'] not in tasks, 'duplicate sampled task')
        tasks[task['id']] = task
    planned = {(task, arm, rep) for task in tasks for arm in plan['arms'] for rep in range(1, 4)}
    slots = packet.get('attempts')
    require(type(slots) is list and len(slots) == 360, 'all360 actual packet assignments required')
    slot_keys = set()
    names, solvers = set(), set()
    for slot in slots:
        require(type(slot) is dict and set(slot) == {'assignment', 'capture', 'config', 'task_id', 'arm',
                                                   'repetition', 'task_sha256', 'solver_id'}, 'exact packet slot fields required')
        name = slot['assignment']
        require(type(name) is str and NAME.fullmatch(name) and name not in names, 'unique normalized assignment required')
        require(type(slot['repetition']) is int, name + ': integer repetition required')
        key = (slot['task_id'], slot['arm'], slot['repetition'])
        require(key in planned and key not in slot_keys, name + ': unknown/duplicate planned slot')
        require(name == f"{slot['arm']}-{slot['task_id']}-r{slot['repetition']:02d}" and
                slot['capture'] == 'captures/' + name and slot['config']['path'] == 'configs/' + name + '.json',
                name + ': frozen assignment paths differ')
        require(slot['task_sha256'] == schema.digest(tasks[slot['task_id']]), name + ': task fingerprint differs')
        nonempty(slot['solver_id'], name + ' solver')
        require(slot['solver_id'] not in solvers, 'each slot requires a distinct solver')
        read_ref(root, slot['config'])  # opaque hashing only; never parse prompt-bearing config
        names.add(name); solvers.add(slot['solver_id']); slot_keys.add(key)
    require(slot_keys == planned, 'missing planned slots; no fabricated failures')
    raw_selections = raw_review_selections(root, raw_selection_ref, packet_ref, packet, slots, solvers)
    history_attestations = source_history_attestations(root, history_manifest_ref, packet_ref, slots, solvers)
    access, access_evidence = declaration(root, access_ref, 'equal-access', packet_ref, packet, solvers)
    harness, harness_evidence = declaration(root, harness_ref, 'shared-harness', packet_ref, packet, solvers)
    results = {'schema': 'broader-claim-results-v1', 'plan_sha256': schema.digest(plan),
               'sample_sha256': schema.digest(selected), 'equal_access': {'established': access['established'], 'review': access_ref},
               'shared_harness_defect': {'confirmed': harness['confirmed'], 'review': harness_ref if harness['confirmed'] else None},
               'attempts': []}
    audit = []
    for slot in slots:
        name = slot['assignment']
        final, terminal_refs, diagnostic = terminal_evidence(root, slot)
        bindings = {'packet': packet_ref, 'plan': packet['plan'], 'sample': packet['sample'], 'config': slot['config']}
        raw, raw_fields, raw_refs = review_artifact(root, name, 'raw', slot, solvers,
                                                   dict(bindings, **terminal_refs), final,
                                                   raw_selections[name]['selected'] if name in raw_selections else None)
        for field in ('task_id', 'arm', 'repetition'):
            require(raw.get(field) == slot[field], name + ': raw slot ' + field + ' join differs')
        require(raw.get('semantic_scoring') == 'excluded_raw_review_only', name + ': raw reviewer may not score semantics')
        require(type(raw.get('classification_basis')) is str and raw['classification_basis'].strip(),
                name + ': independently reviewed classification basis required')
        for field in ('capture_integrity', 'classification_confirmed'):
            require(type(raw_fields[field]) is bool, name + ': raw boolean required')
        for field in ('access_loss_confirmed', 'scope_exposure_confirmed', 'shared_harness_defect_suspected'):
            require(type(raw.get(field)) is bool, name + ': explicit raw cause declaration required')
        manual = raw.get('manual_review')
        require(type(manual) is dict and manual.get('no_feedback_to_solver') is True,
                name + ': independent raw review must exclude solver feedback')
        manual_flags = {'all_retained_provider_native_bodies_read', 'all_terminal_failed_partial_late_bodies_read',
                        'identity_image_settings_isolation_verified', 'no_feedback_to_solver',
                        'raw_scope_display_and_selector_replay_verified', 'resource_and_relay_decisions_verified'}
        require(manual_flags | {'notes'} <= set(manual) <= manual_flags | {'notes'} | MANUAL_DIAGNOSTICS and
                all(type(manual[field]) is bool for field in manual_flags),
                name + ': complete explicit raw manual declarations required')
        for key in MANUAL_DIAGNOSTICS & set(manual):
            if key == 'sensitive_literal_values_directly_read':
                require(type(manual[key]) is bool, name + ': explicit literal-read declaration required')
            elif key in {'sensitive_literal_omissions', 'omitted_context_lines', 'sensitive_literal_display_omissions'}:
                require(type(manual[key]) is list, name + ': original omission list required')
            elif key == 'sensitive_safe_representation':
                safe = manual[key]
                require(type(safe) is dict and set(safe) == {'authorization', 'helper', 'omitted_line_displays',
                    'original_bytes_mechanically_verified', 'sensitive_literal_values_directly_read', 'dependent_raw_fact_limit'},
                    name + ': exact sensitive-safe representation required')
                read_ref(root, safe['authorization']); read_ref(root, safe['helper'])
                require(type(safe['omitted_line_displays']) is list and
                        type(safe['sensitive_literal_values_directly_read']) is bool and
                        type(safe['original_bytes_mechanically_verified']) is bool,
                        name + ': explicit safe representation declarations required')
                require(not raw_fields['complete'] or safe['original_bytes_mechanically_verified'] is True,
                        name + ': complete safe review requires original byte verification')
                nonempty(safe['dependent_raw_fact_limit'], name + ' safe representation limitation')
            elif key == 'nonterminal_client_error':
                error = manual[key]
                require(type(error) is dict and set(error) == {'captured_host_native_call_present', 'exact_cause',
                    'preceding_provider_ordinal', 'provider_following_request', 'terminal_stop'},
                    name + ': exact nonterminal client error diagnostic required')
                require(type(error['captured_host_native_call_present']) is bool and error['terminal_stop'] is False and
                        type(error['preceding_provider_ordinal']) is int and error['preceding_provider_ordinal'] > 0,
                        name + ': explicit nonterminal client error metadata required')
                nonempty(error['exact_cause'], name + ' original client error cause')
                read_ref(root, error['provider_following_request'])
            elif key == 'diagnostics':
                require(type(manual[key]) is dict, name + ': original diagnostic object required')
            else:
                nonempty(manual[key], name + ' original manual diagnostic')
        require(not raw_fields['complete'] or all(manual[field] for field in manual_flags),
                name + ': complete raw review contradicts an unverified mandatory manual check')
        nonempty(manual['notes'], name + ' raw manual limitations')
        machine = raw.get('machine_review')
        require(type(machine) is dict and set(machine) == {'artifact', 'assignment'} and machine['assignment'] == name,
                name + ': exact own-assignment machine-review selector required')
        read_ref(root, machine['artifact'])
        require(machine['artifact']['path'] != raw_fields['artifact']['path'],
                name + ': machine review cannot reference the raw review itself')
        if raw['access_loss_confirmed']:
            require(not access['established'], name + ': confirmed access loss contradicts equal access')
            has_ref(access_evidence, raw_fields['artifact'], 'access-loss raw review')
        if raw.get('failure_class') == 'access':
            require(raw['access_loss_confirmed'] and raw_fields['classification_confirmed'] and not access['established'],
                    name + ': access cause lacks confirmed loss')
        if raw.get('failure_class') == 'scope':
            require(raw['scope_exposure_confirmed'] and raw_fields['classification_confirmed'],
                    name + ': scope cause lacks confirmed exposure')
        if raw.get('failure_class') == 'shared_harness':
            require(raw_fields['classification_confirmed'], name + ': shared cause must be independently classified')
        if raw['shared_harness_defect_suspected']:
            has_ref(harness_evidence, raw_fields['artifact'], 'suspected harness raw review')
        source, source_fields, source_refs = review_artifact(root, name, 'source', slot, solvers, bindings, final)
        require(source['reviewer_id'] != raw['reviewer_id'], name + ': raw and semantic reviewers must be independent')
        history = source.get('review_history')
        attestation = history_attestations.get(source_fields['artifact']['path'])
        if type(history) is dict:
            require(all(history.get(key) is True for key in HISTORY_FLAGS),
                    name + ': source history contradicts fresh semantic reviewer history requirements')
        require((type(history) is dict and all(history.get(key) is True for key in HISTORY_FLAGS)) or
                attestation is not None,
                name + ': explicit fresh semantic reviewer history required')
        applicability = 'final_reviewed' if final else 'no_final'
        require(source.get('applicability') == applicability, name + ': source applicability differs from recorded final')
        require(type(source.get('required_claims')) is list and type(source.get('material_claims_and_citations')) in (list, dict),
                name + ': original per-claim source findings must be retained')
        nonempty(source.get('limitations'), name + ' source limitations')
        require(type(source_fields['criteria']) is dict and set(source_fields['criteria']) == schema.CRITERIA and
                all(type(x) is bool for x in source_fields['criteria'].values()), name + ': exact declared source criteria required')
        if final is None:
            require(not any(source_fields['criteria'].values()), name + ': no-final source review cannot assert success')
        if 'assignment' not in terminal_refs:
            require(raw.get('outcome') != 'answered' and raw_fields['capture_integrity'] is False and
                    raw_fields['classification_confirmed'] is True,
                    name + ': absent assignment needs closed independent non-success classification')
        if 'capture inventory' not in terminal_refs:
            require(raw_fields['capture_integrity'] is False,
                    name + ': absent capture cannot assert capture integrity')
        row = {key: copy.deepcopy(slot[key]) for key in ('task_id', 'arm', 'repetition', 'task_sha256', 'solver_id')}
        row.update(outcome=raw.get('outcome'), failure_class=raw.get('failure_class'), final=final,
                   raw_review=raw_fields, source_review=source_fields)
        results['attempts'].append(row)
        diagnostic.update(assignment=name, config=slot['config'], raw_review=raw_fields['artifact'],
                          source_review=source_fields['artifact'],
                          raw_original_decision=copy.deepcopy(raw['original_decision']),
                          source_original_decision=copy.deepcopy(source['original_decision']),
                          raw_evidence_refs=raw_refs, source_evidence_refs=source_refs)
        if attestation is not None:
            diagnostic['source_history_attestation'] = attestation
        if name in raw_selections:
            diagnostic['raw_review_selection'] = copy.deepcopy(raw_selections[name])
        audit.append(diagnostic)
    shared = {slot['assignment']: row['raw_review']['artifact'] for slot, row in zip(slots, results['attempts'])
              if row['failure_class'] == 'shared_harness'}
    reviewed = {}
    for cause in harness['reviewed_causes']:
        require(type(cause) is dict and set(cause) == {'assignment', 'cause', 'raw_review', 'reasoning'},
                'exact independently reviewed harness cause fields required')
        name = cause['assignment']
        require(name in shared and name not in reviewed and cause['cause'] == 'shared_harness' and
                cause['raw_review'] == shared[name], 'shared-harness cause/assignment/review join differs')
        nonempty(cause['reasoning'], 'shared-harness cause reasoning')
        reviewed[name] = cause['raw_review']
    require(reviewed == shared, 'shared-harness causes lack exact independent adjudication')
    # Validation only. The returned numeric values are intentionally discarded;
    # no criteria are derived, and no analysis/winner is produced here.
    _, readiness, counts = schema.task_values(plan, selected, results)
    manifest = {'schema': 'broader-pilot-results-assembly-manifest-v1', 'packet': packet_ref,
                'plan': packet['plan'], 'sample': packet['sample'], 'schema_code': schema_ref,
                'assembler_code_sha256': sha(Path(__file__).read_bytes()),
                'equal_access_declaration': access_ref, 'shared_harness_declaration': harness_ref,
                'raw_review_selection': raw_selection_ref,
                'source_history_manifest': history_manifest_ref,
                'planned_attempts': 360, 'assembled_attempts': len(results['attempts']),
                'review_readiness_reasons': sorted(set(readiness)), 'attempt_outcomes': counts,
                'attempts': audit,
                'limitations': 'Projection validates declared bindings, not reviewer authenticity, source semantics, or capture truth. Original decisions, disagreements, incomplete evidence and input artifacts are retained; no solver retries, scoring, or product/provider traffic.'}
    return results, manifest


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', default=str(Path(__file__).resolve().parents[1]))
    for label in ('packet', 'equal-access', 'shared-harness'):
        parser.add_argument('--' + label, required=True)
        parser.add_argument('--' + label + '-sha256', required=True)
    parser.add_argument('--schema-sha256', required=True)
    parser.add_argument('--raw-review-selection', help='explicit independently approved mechanical correction manifest')
    parser.add_argument('--raw-review-selection-sha256', help='exact selection manifest file SHA256')
    parser.add_argument('--source-history-manifest', help='explicit author attestations bound to original source reviews')
    parser.add_argument('--source-history-manifest-sha256', help='exact source history manifest file SHA256')
    parser.add_argument('--output', required=True, help='new root-relative results path')
    parser.add_argument('--manifest-output', required=True, help='new root-relative preservation manifest path')
    args = parser.parse_args(argv)
    try:
        require((args.raw_review_selection is None) == (args.raw_review_selection_sha256 is None),
                'raw-review selection path and SHA256 must be supplied together')
        require((args.source_history_manifest is None) == (args.source_history_manifest_sha256 is None),
                'source history manifest path and SHA256 must be supplied together')
        root = Path(args.root).resolve()
        output, manifest_path = local(root, args.output), local(root, args.manifest_output)
        require(output != manifest_path and not output.exists() and not manifest_path.exists(),
                'distinct new output files required; existing diagnostics are never overwritten')
        result, manifest = assemble(root, {'path': args.packet, 'sha256': args.packet_sha256},
                                    {'path': args.equal_access, 'sha256': args.equal_access_sha256},
                                    {'path': args.shared_harness, 'sha256': args.shared_harness_sha256}, args.schema_sha256,
                                    {'path': args.raw_review_selection, 'sha256': args.raw_review_selection_sha256}
                                    if args.raw_review_selection is not None else None,
                                    {'path': args.source_history_manifest, 'sha256': args.source_history_manifest_sha256}
                                    if args.source_history_manifest is not None else None)
        payload = json.dumps(result, sort_keys=True, indent=2, ensure_ascii=False, allow_nan=False).encode() + b'\n'
        manifest['results'] = {'path': args.output, 'sha256': sha(payload)}
        manifest_payload = json.dumps(manifest, sort_keys=True, indent=2, ensure_ascii=False, allow_nan=False).encode() + b'\n'
        # All validation finishes before either exclusive-create output write.
        for path, raw in ((output, payload), (manifest_path, manifest_payload)):
            fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, 'wb') as stream:
                stream.write(raw)
                stream.flush()
                os.fsync(stream.fileno())
        print(json.dumps({'results': manifest['results'], 'manifest': ref_for(root, args.manifest_output),
                          'attempts': len(result['attempts']), 'scored': False}, sort_keys=True))
        return 0
    except (ValueError, TypeError, KeyError, OSError, ImportError) as error:
        parser.exit(2, 'pilot results assembly failed: ' + str(error) + '\n')


if __name__ == '__main__':
    sys.exit(main())
