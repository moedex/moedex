"""Explicit observed-service provenance; no claim of immutable attestation.

Hash checks bind supplied evidence bytes, not the truth of user authorization or
service identities. Independent review remains required by the run recorder.
"""
import datetime
import hashlib
import json
import re


SHA = re.compile(r'^[0-9a-f]{64}$')
OBSERVED = 'observed-service-v1'
IMMUTABLE = 'immutable-v1'


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def require(condition, reason):
    if not condition:
        raise ValueError(reason)


def mode(frozen):
    require(isinstance(frozen, dict), 'freeze must be an object')
    if 'provenance' not in frozen:
        return IMMUTABLE
    policy = frozen['provenance']
    require(isinstance(policy, dict), 'provenance must be an object')
    value = policy.get('mode')
    require(value in (IMMUTABLE, OBSERVED), 'unknown provenance mode')
    return value


def response_objects(body, strict=False):
    """Response objects in JSON or SSE; encrypted reasoning is never decoded."""
    try:
        value = json.loads(body)
        if isinstance(value, dict):
            yield value
        elif strict:
            raise ValueError('response must be a JSON object')
        return
    except (ValueError, UnicodeDecodeError):
        pass
    for frame in re.split(br'\r?\n\r?\n', body):
        data = b'\n'.join(line[5:].lstrip(b' ') for line in frame.splitlines()
                          if line.startswith(b'data:'))
        if not data or data == b'[DONE]':
            continue
        try:
            value = json.loads(data)
        except (ValueError, UnicodeDecodeError):
            if strict:
                raise ValueError('malformed observed SSE data')
            continue
        if isinstance(value, dict):
            yield value
        elif strict:
            raise ValueError('observed SSE data must be an object')


def response_models(body, strict=False):
    models = set()
    for value in response_objects(body, strict=strict):
        for response in (value, value.get('response')):
            if isinstance(response, dict) and 'model' in response:
                valid = isinstance(response['model'], str) and bool(response['model'].strip())
                if strict:
                    require(valid, 'observed response model identity invalid')
                if valid:
                    models.add(response['model'])
    return sorted(models)


def native_result(body, request_id):
    results = [v['result'] for v in response_objects(body, strict=True)
               if v.get('id') == request_id and 'result' in v and 'error' not in v]
    require(len(results) == 1 and isinstance(results[0], dict), 'native observation result missing or ambiguous')
    return results[0]


def _json(root, ref, read_ref):
    value = json.loads(read_ref(root, ref))
    require(isinstance(value, dict), 'provenance evidence must be an object')
    return value


def _timestamp(value):
    require(isinstance(value, str), 'observation timestamp missing')
    stamp = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
    require(stamp.tzinfo is not None and stamp.utcoffset() == datetime.timedelta(0),
            'observation timestamp must be UTC')


def _exchange(root, exchange, read_ref, notification=False):
    require(isinstance(exchange, dict) and set(exchange) == {'request', 'response', 'receipt'},
            'observation exchange requires request, response and receipt')
    request = _json(root, exchange['request'], read_ref)
    body = read_ref(root, exchange['response'])
    receipt = _json(root, exchange['receipt'], read_ref)
    require(type(receipt.get('body_bytes_observed')) is int and receipt['body_bytes_observed'] == len(body),
            'observation receipt byte count mismatch')
    require(receipt.get('transport_complete') is True and type(receipt.get('status')) is int and
            200 <= receipt['status'] < 300, 'observation transport incomplete or unsuccessful')
    return request, body


def _validate_observed(root, frozen, read_ref):
    policy = frozen['provenance']
    limits = policy.get('reproducibility_limits')
    require(isinstance(limits, list) and bool(limits) and
            all(isinstance(v, str) and bool(v.strip()) for v in limits) and len(set(limits)) == len(limits),
            'explicit reproducibility limits missing')
    authorization = _json(root, policy.get('authorization'), read_ref)
    require(authorization.get('schema') == 'observed-authorization-v1' and
            authorization.get('mode') == OBSERVED and authorization.get('authorized_by') == 'user' and
            isinstance(authorization.get('decision'), str) and bool(authorization['decision'].strip()),
            'observed-service user authorization invalid')
    requirements = authorization.get('requirements')
    require(isinstance(requirements, list) and
            all(isinstance(v, str) for v in requirements) and
            {'isolation', 'budgets', 'raw-capture', 'independent-scoring'} <= set(requirements),
            'observed-service authorization must retain execution and review requirements')
    require(authorization.get('reproducibility_limits') == limits, 'authorization limits differ from frozen disclosure')
    model, product = frozen.get('model'), frozen.get('product')
    require(isinstance(model, dict) and isinstance(product, dict), 'observed model and product must be objects')
    mo = _json(root, model.get('observation'), read_ref)
    po = _json(root, product.get('observation'), read_ref)
    require(mo.get('schema') == 'observed-model-v1' and po.get('schema') == 'observed-product-v1',
            'unknown observation schema')
    _timestamp(mo.get('observed_utc'))
    _timestamp(po.get('observed_utc'))
    for actual, expected, label in (
            (mo.get('requested_alias'), model.get('requested_alias'), 'requested model'),
            (mo.get('provider_base_url_sha256'), model.get('provider_base_url_sha256'), 'provider endpoint'),
            (mo.get('settings_sha256'), digest(canonical(model.get('settings'))), 'model settings'),
            (po.get('endpoint_sha256'), product.get('endpoint_sha256'), 'product endpoint')):
        require(isinstance(actual, str) and bool(actual) and actual == expected, 'observation ' + label + ' mismatch')
    for value in (mo['provider_base_url_sha256'], mo['settings_sha256'], po['endpoint_sha256'], po.get('catalog_sha256')):
        require(isinstance(value, str) and bool(SHA.fullmatch(value)), 'observation identity digest invalid')
    require(mo.get('immutable_revision_verified') is False and
            po.get('immutable_serving_identity_verified') is False and
            po.get('runtime_dependency_closure_verified') is False,
            'observed-service evidence must disclose unverified immutable identities and runtime closure')
    models = mo.get('returned_models')
    require(isinstance(models, list) and bool(models) and
            all(isinstance(v, str) and bool(v.strip()) for v in models) and models == sorted(set(models)),
            'observed returned model identities missing or invalid')
    request, body = _exchange(root, {k: mo[k] for k in ('request', 'response', 'receipt')}, read_ref)
    fields = model.get('settings', {}).get('provider_fields')
    require(isinstance(fields, dict) and bool(fields) and fields.get('model') == model['requested_alias'] and
            all(request.get(k) == v for k, v in fields.items()) and
            not (set(request) - set(fields) - {'input', 'prompt_cache_key', 'client_metadata'}),
            'model observation request settings mismatch')
    require(response_models(body, strict=True) == models, 'model observation response identities mismatch')
    completed = [value.get('response') if value.get('type') == 'response.completed' else value
                 for value in response_objects(body, strict=True)]
    require(any(isinstance(value, dict) and value.get('status') == 'completed' and
                value.get('model') in models for value in completed),
            'model observation completed response identity missing')
    exchanges = po.get('native_exchanges')
    require(isinstance(exchanges, list) and len(exchanges) == 3, 'complete native observation exchanges missing')
    results = {}
    for exchange, method in zip(exchanges, ('initialize', 'notifications/initialized', 'tools/list')):
        request, body = _exchange(root, exchange, read_ref)
        require(request.get('jsonrpc') == '2.0' and request.get('method') == method,
                'native observation request mismatch')
        if method == 'notifications/initialized':
            require('id' not in request, 'initialization notification must not carry an ID')
        else:
            require('id' in request, 'native observation request ID missing')
            results[method] = native_result(body, request['id'])
    require(isinstance(po.get('server_info'), dict) and bool(po['server_info']) and
            results['initialize'].get('serverInfo') == po['server_info'], 'observed server identity mismatch')
    catalog = results['tools/list']
    require(isinstance(catalog.get('tools'), list) and not catalog.get('nextCursor'),
            'observed native catalog incomplete')
    require(digest(canonical(catalog)) == po['catalog_sha256'], 'observed catalog identity mismatch')
    return {'model': mo, 'product': po}


def validate(root, frozen, read_ref):
    """Return blockers for malformed policy or observed evidence; strict is unchanged."""
    try:
        if mode(frozen) == OBSERVED:
            _validate_observed(root, frozen, read_ref)
        return []
    except (ValueError, OSError, TypeError, KeyError, AttributeError) as exc:
        return ['provenance: ' + str(exc)]


def observations(root, frozen, read_ref):
    require(mode(frozen) == OBSERVED, 'observed-service policy required')
    return _validate_observed(root, frozen, read_ref)


def identity_binding(root, frozen, read_ref):
    """Exact seal/accounting binding derived from hash-checked frozen evidence."""
    policy = mode(frozen)
    if policy == IMMUTABLE:
        return {'policy': policy}
    observations(root, frozen, read_ref)
    return {'policy': policy,
            'authorization_sha256': frozen['provenance']['authorization']['sha256'],
            'model_observation_sha256': frozen['model']['observation']['sha256'],
            'product_observation_sha256': frozen['product']['observation']['sha256'],
            'reproducibility_limits': frozen['provenance']['reproducibility_limits']}
