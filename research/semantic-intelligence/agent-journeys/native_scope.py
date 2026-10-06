"""Opt-in pinned source scope for recorded native brokers (stdlib, no I/O).

Native provenance is checked before model display. This does not authenticate a
server, source manifest, or deployment; the frozen coordinator audit must do so.
"""
from copy import deepcopy
import hashlib
import json
import math
from pathlib import PurePosixPath
import re
from urllib.parse import urlsplit

PIN = re.compile(r'^[0-9a-f]{40}$')
SHA = re.compile(r'^[0-9a-f]{64}$')
CG_TOOLS = {'codegraph_search', 'graph_source', 'graph_trace'}
MOE_TOOLS = {'compiler_binding_at', 'compiler_contract_context', 'compiler_contract_impact',
             'compiler_contract_paths', 'compiler_definitions', 'compiler_evidence_path',
             'compiler_implementations', 'compiler_symbols', 'compiler_trace_calls',
             'file_tree', 'graph_neighbors', 'graph_schema', 'impact_analysis', 'list_clusters',
             'list_repos', 'list_symbols', 'read_source', 'search_context', 'trace_calls',
             'trace_consumers', 'trace_hierarchy', 'trace_queries', 'trace_renders'}
COMPILER_TOOLS = {name for name in MOE_TOOLS if name.startswith('compiler_')}
PROJECT_KEYS = {'project', 'repo', 'repository', 'path_with_namespace', 'projectName', 'repoName'}
PROJECT_LIST_KEYS = {'projects', 'affectedProjects', 'repositories'}
NODE_FIELDS = {'nodeId', 'id', 'name', 'qualifiedName', 'label', 'project', 'dotnetProject',
               'filePath', 'path', 'location', 'startLine', 'endLine', 'doNotTrust', 'trustScore',
               'snippetSelector', 'traceSelector', 'depth', 'edgeType', 'risk', 'riskFactors'}


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


class ScopeViolation(ValueError):
    """Only fixed safe reason codes may cross the model boundary."""


def require(value, reason):
    if not value:
        raise ScopeViolation(reason)


def relative_file(value):
    require(type(value) is str and value and '\\' not in value, 'invalid_source_path')
    path = PurePosixPath(value)
    require(path.parts and not path.is_absolute() and '..' not in path.parts and
            path.as_posix() == value and not value.endswith('/'), 'invalid_source_path')
    return value


class NativeScope:
    """One fresh policy instance per assignment; selectors never cross contexts."""
    def __init__(self, policy, policy_sha256=None, compiler_inputs=None):
        keys = {'schema', 'backend', 'allowed_tools', 'projects', 'corpus_fingerprint', 'graph_revision'}
        require(type(policy) is dict and set(policy) in (keys, keys | {'compiler_admission'}), 'invalid_scope_policy')
        require(policy['schema'] == 'native-source-scope-v1', 'invalid_scope_schema')
        require(policy['backend'] in ('codegraph-v1', 'moedex-index-v1'), 'unknown_scope_backend')
        self.backend = policy['backend']
        tools = policy['allowed_tools']
        require(type(tools) is list and tools and all(type(t) is str for t in tools) and
                len(set(tools)) == len(tools), 'invalid_scope_tools')
        require(set(tools) <= (CG_TOOLS if self.backend == 'codegraph-v1' else MOE_TOOLS), 'unscopable_tool')
        self.allowed = set(tools)
        self.projects, self.aliases, self.urls = {}, {}, {}
        require(type(policy['projects']) is list and policy['projects'], 'missing_scope_projects')
        for row in policy['projects']:
            require(type(row) is dict and set(row) in ({'repository', 'url', 'commit', 'aliases', 'files'},
                    {'repository', 'url', 'commit', 'aliases', 'files', 'metadata_only'}), 'invalid_scope_project')
            repository = row['repository']
            require(type(repository) is str and repository, 'invalid_scope_repository')
            if repository.startswith('https://'):
                require(repository == row['url'], 'repository_url_identity_mismatch')
            else:
                relative_file(repository)
            require(repository not in self.projects, 'duplicate_scope_project')
            require(type(row['commit']) is str and PIN.fullmatch(row['commit']), 'invalid_scope_pin')
            require(type(row['url']) is str, 'invalid_scope_url')
            url = urlsplit(row['url'])
            require(url.scheme == 'https' and url.hostname and not url.username and not url.password and
                    not url.query and not url.fragment and row['url'] not in self.urls, 'invalid_scope_url')
            require(type(row['aliases']) is list and all(type(a) is str and a and a == a.strip()
                    for a in row['aliases']) and len(set(row['aliases'])) == len(row['aliases']), 'invalid_scope_aliases')
            require(type(row['files']) is dict and (bool(row['files']) or row.get('metadata_only') is True),
                    'missing_source_manifest')
            require('metadata_only' not in row or (row['metadata_only'] is True and not row['files']),
                    'invalid_metadata_only_project')
            for path, blob in row['files'].items():
                relative_file(path)
                require(type(blob) is str and PIN.fullmatch(blob), 'invalid_source_blob')
            self.projects[repository] = deepcopy(row)
            self.urls[row['url']] = repository
            for alias in [repository, row['url']] + row['aliases']:
                require(alias not in self.aliases or self.aliases[alias] == repository, 'ambiguous_scope_alias')
                self.aliases[alias] = repository
                if alias.startswith('https://'):
                    parsed = urlsplit(alias)
                    require(parsed.hostname and not parsed.username and not parsed.password and
                            not parsed.query and not parsed.fragment, 'invalid_scope_url_alias')
                    self.urls[alias] = repository
        fingerprint, revision = policy['corpus_fingerprint'], policy['graph_revision']
        if self.backend == 'moedex-index-v1':
            require(type(fingerprint) is str and SHA.fullmatch(fingerprint), 'missing_index_fingerprint')
            require(revision is None, 'unexpected_moedex_graph_revision')
        else:
            require(fingerprint is None, 'unexpected_codegraph_fingerprint')
            require(revision is None or type(revision) is int and revision >= 0, 'invalid_graph_revision')
        self.fingerprint, self.revision = fingerprint, revision
        self.nodes, self.cursors = {}, {}
        self.compiler_symbols, self.compiler_contexts = set(), set()
        self.compiler = None
        if 'compiler_admission' in policy:
            self._load_compiler(policy['compiler_admission'], compiler_inputs)
        else:
            require(compiler_inputs is None, 'unexpected_compiler_inputs')
        require(policy_sha256 is None or type(policy_sha256) is str and SHA.fullmatch(policy_sha256),
                'invalid_policy_file_binding')
        self.policy_sha256 = policy_sha256 if policy_sha256 is not None else digest(policy)

    @staticmethod
    def _schema(value, schema):
        """Validate the deliberately small, frozen native catalog dialect."""
        kind = schema.get('type')
        types = {'object': dict, 'array': list, 'string': str, 'integer': int, 'boolean': bool}
        require(kind in types and type(value) is types[kind], 'compiler_schema_type_mismatch')
        if kind == 'object':
            properties = schema.get('properties', {})
            require(schema.get('additionalProperties') is False and set(value) <= set(properties) and
                    set(schema.get('required', [])) <= set(value), 'compiler_schema_field_mismatch')
            for key, child in value.items():
                NativeScope._schema(child, properties[key])
        elif kind == 'array':
            require(len(value) >= schema.get('minItems', 0) and
                    len(value) <= schema.get('maxItems', len(value)), 'compiler_schema_array_bounds')
            if schema.get('uniqueItems'):
                require(len({canonical(v) for v in value}) == len(value), 'compiler_schema_duplicate_item')
            for child in value:
                NativeScope._schema(child, schema['items'])
        elif kind == 'integer':
            require(value >= schema.get('minimum', 0) and value <= schema.get('maximum', value),
                    'compiler_schema_number_bounds')
        elif kind == 'string' and 'pattern' in schema:
            require(re.fullmatch(schema['pattern'], value), 'compiler_schema_pattern_mismatch')
        if 'enum' in schema:
            require(value in schema['enum'], 'compiler_schema_enum_mismatch')

    def _load_compiler(self, admission, inputs):
        require(self.backend == 'moedex-index-v1' and type(admission) is dict and
                set(admission) == {'schema', 'manifest', 'schemas', 'served_sources'} and
                admission['schema'] == 'frozen-compiler-admission-v1', 'invalid_compiler_admission')
        require(type(inputs) is dict and set(inputs) == {'manifest', 'schemas', 'served_sources'},
                'missing_frozen_compiler_inputs')
        loaded = {}
        for name, raw in inputs.items():
            ref = admission[name]
            require(type(ref) is dict and set(ref) == {'path', 'sha256'} and
                    type(ref['sha256']) is str and SHA.fullmatch(ref['sha256']), 'invalid_compiler_ref')
            relative_file(ref['path'])
            require(type(raw) is bytes and hashlib.sha256(raw).hexdigest() == ref['sha256'],
                    'compiler_input_hash_mismatch')
            try:
                loaded[name] = json.loads(raw)
            except (ValueError, UnicodeError):
                raise ScopeViolation('invalid_compiler_input_json')
        manifest, schemas, served = loaded['manifest'], loaded['schemas'], loaded['served_sources']
        require(type(manifest) is dict and manifest.get('schema') == 'frozen-compiler-artifact-metadata-v1',
                'invalid_compiler_manifest')
        identity = manifest.get('identity')
        require(type(identity) is dict and set(identity) == {'snapshot_id', 'artifact_sha256', 'corpus_fingerprint'} and
                type(identity['snapshot_id']) is str and identity['snapshot_id'] and
                all(type(identity[k]) is str and SHA.fullmatch(identity[k])
                    for k in ('artifact_sha256', 'corpus_fingerprint')), 'invalid_compiler_identity')
        for name in ('snapshots', 'contexts', 'sources', 'symbols', 'occurrences', 'bindings'):
            rows = manifest.get(name)
            require(type(rows) is dict and all(type(v) is dict and v.get('id') == k for k, v in rows.items()),
                    'invalid_compiler_manifest_map')
        require(type(schemas) is dict and set(schemas) == {'tools'} and type(schemas['tools']) is list,
                'invalid_compiler_schemas')
        tools = {t['name']: t for t in schemas['tools'] if type(t) is dict and 'name' in t}
        require(set(tools) == COMPILER_TOOLS and len(tools) == len(schemas['tools']), 'compiler_schema_roster_mismatch')
        require(type(served) is dict and served.get('schema') == 'actual-served-source-map-v1' and
                type(served.get('files')) is list, 'invalid_compiler_served_sources')
        files = {}
        for row in served['files']:
            require(type(row) is dict, 'invalid_compiler_served_file')
            repo = self.project(row.get('repository'))
            path = relative_file(row.get('path'))
            require(path in self.projects[repo]['files'] and self.projects[repo]['files'][path] == row.get('key') and
                    type(row.get('raw_sha256')) is str and SHA.fullmatch(row['raw_sha256']) and
                    (repo, path) not in files, 'compiler_served_file_binding_mismatch')
            files[repo, path] = row['raw_sha256']
        self.compiler = manifest
        self.compiler_tools = tools
        self.compiler_files = files
        self.compiler_bindings = {}
        for binding in manifest['bindings'].values():
            occurrence = binding.get('occurrence_id')
            require(occurrence in manifest['occurrences'] and occurrence not in self.compiler_bindings,
                    'compiler_binding_occurrence_mismatch')
            self.compiler_bindings[occurrence] = binding

    def _compiler_context(self, identity):
        require(type(identity) is str and identity in self.compiler['contexts'], 'unfrozen_compiler_context')
        context = self.compiler['contexts'][identity]
        snapshot = self.compiler['snapshots'].get(context.get('snapshot_id'))
        require(type(snapshot) is dict, 'unfrozen_compiler_snapshot')
        repo = self.project(snapshot.get('repo'))
        require(snapshot.get('commit') == self.projects[repo]['commit'] and
                not self.projects[repo].get('metadata_only') and context.get('status') == 'complete' and
                context.get('project') in self.projects[repo]['files'], 'compiler_context_scope_mismatch')
        return context, snapshot, repo

    def _compiler_source(self, context_id, source_id):
        context, snapshot, repo = self._compiler_context(context_id)
        source = self.compiler['sources'].get(source_id)
        require(type(source) is dict and source_id in context.get('source_ids', []) and
                source.get('snapshot_id') == snapshot['id'] and type(source.get('generated')) is bool and
                type(source.get('raw_sha256')) is str and SHA.fullmatch(source['raw_sha256']),
                'compiler_source_context_mismatch')
        relative_file(source.get('path'))
        if source['generated']:
            # Generated metadata is a recorded derivation. It never adds a file
            # to the lexical/source-body scope or permits requests for its body.
            require(type(context.get('capture')) is str and context.get('extractor') and
                    context.get('extractor_version') and SHA.fullmatch(context.get('input_fingerprint', '')),
                    'missing_generated_compiler_derivation')
            try:
                capture = json.loads(context['capture'])
            except ValueError:
                raise ScopeViolation('invalid_generated_compiler_derivation')
            require(capture.get('repo') == repo and capture.get('project') == context['project'] and
                    any(v.get('generated') is True and v.get('path') == source['path'] and
                        v.get('sha256') == source['raw_sha256'] and v.get('byte_size') == source.get('byte_size')
                        for v in capture.get('sources', []) if type(v) is dict),
                    'generated_compiler_derivation_mismatch')
        else:
            require(self.compiler_files.get((repo, source['path'])) == source['raw_sha256'],
                    'compiler_source_scope_mismatch')
        return context, snapshot, source, repo

    def _compiler_symbol(self, identity):
        require(type(identity) is str and identity in self.compiler['symbols'], 'unfrozen_compiler_symbol')
        row = self.compiler['symbols'][identity]
        require(type(row.get('key')) is dict, 'invalid_compiler_symbol')
        if row['key'].get('namespace_kind') == 'project':
            matches = [context['id'] for context in self.compiler['contexts'].values()
                       for snapshot in [self.compiler['snapshots'].get(context.get('snapshot_id'), {})]
                       if row['key'].get('namespace') == snapshot.get('repo', '') + '/' + context['project']]
            require(matches, 'unfrozen_compiler_symbol_namespace')
            for context_id in matches:
                self._compiler_context(context_id)
        return dict(row['key'], id=identity)

    def _compiler_fact(self, fact):
        value = deepcopy(fact)
        value['targets'] = [{'role': target['role'], 'symbol': self._compiler_symbol(target['symbol_id'])}
                            for target in fact['targets']]
        return value

    @staticmethod
    def _symbol_ids(value):
        if isinstance(value, dict):
            for key, child in value.items():
                if key.endswith('symbol_id') and type(child) is str and child:
                    yield child
                else:
                    yield from NativeScope._symbol_ids(child)
        elif isinstance(value, list):
            for child in value:
                yield from NativeScope._symbol_ids(child)

    def _compiler_binding(self, occurrence_id):
        occurrence = self.compiler['occurrences'].get(occurrence_id)
        binding = self.compiler_bindings.get(occurrence_id)
        require(type(occurrence) is dict and type(binding) is dict, 'unfrozen_compiler_occurrence')
        context, snapshot, source, repo = self._compiler_source(occurrence.get('context_id'), occurrence.get('source_id'))
        value = {'generated': source['generated'], 'occurrence_id': occurrence_id,
                 'binding_status': binding['status'], 'reference_kind': occurrence['kind'], 'role': occurrence['role'],
                 'repo': repo, 'path': source['path'], 'raw_sha256': source['raw_sha256'],
                 'source_id': source['id'], 'source_snapshot_id': snapshot['id'], 'context_id': context['id'],
                 'project': context['project'], 'byte_offset': occurrence['offset'], 'byte_length': occurrence['length'],
                 'candidates': [self._compiler_symbol(v) for v in binding.get('candidate_symbol_ids', [])],
                 'method': binding['method'], 'extractor': binding['extractor'], 'extractor_version': binding['extractor_version']}
        for key in ('enclosing_symbol_id', 'implementation_facts'):
            if binding.get(key):
                value[key] = deepcopy(binding[key])
        if binding.get('symbol_id'):
            value['symbol'] = self._compiler_symbol(binding['symbol_id'])
        if binding.get('domain_facts'):
            value['domain_facts'] = [self._compiler_fact(v) for v in binding['domain_facts']]
        if binding.get('implementation_facts'):
            ids = set(self._symbol_ids(binding['implementation_facts']))
            value['implementation_symbols'] = [self._compiler_symbol(v) for v in sorted(ids)]
        return value

    def _prepare_compiler(self, args, context):
        self._schema(args, self.compiler_tools[context['tool']]['inputSchema'])
        context['arguments'] = deepcopy(args)
        if 'repo' in args:
            context['project'] = self.project(args['repo'])
        if 'path' in args:
            path = relative_file(args['path'])
            require(path in self.projects[context['project']]['files'], 'compiler_request_source_outside_scope')
        if 'symbol_id' in args:
            require(args['symbol_id'] in self.compiler_symbols, 'unissued_compiler_symbol')
        selected = args.get('context_ids', [args['context_id']] if 'context_id' in args else [])
        require(len(set(selected)) == len(selected) and all(v in self.compiler_contexts for v in selected),
                'unissued_compiler_context')
        for identity in selected:
            _, _, repo = self._compiler_context(identity)
            require(context['project'] is None or context['project'] == repo, 'compiler_request_context_repo_mismatch')
        if args.get('raw_sha256') is not None:
            require(args['raw_sha256'] == self.compiler_files.get((context['project'], args.get('path'))),
                    'compiler_request_raw_hash_mismatch')
        context['compiler_selected'] = set(selected)
        return context

    def _compiler_walk(self, value, context, staged_symbols, staged_contexts, inherited=None, matched_fact=None):
        if isinstance(value, list):
            for child in value:
                self._compiler_walk(child, context, staged_symbols, staged_contexts, inherited, matched_fact)
            return
        if not isinstance(value, dict):
            return
        if 'id' in value and 'descriptor' in value:
            require(value == self._compiler_symbol(value['id']), 'compiler_symbol_record_mismatch')
            staged_symbols.add(value['id'])
            return
        context_id = value.get('context_id', inherited)
        if 'context_id' in value:
            frozen, snapshot, repo = self._compiler_context(context_id)
            require(context['project'] is None or context['project'] == repo, 'compiler_reply_repo_mismatch')
            for key, expected in (('repo', repo), ('project', frozen['project']), ('source_snapshot_id', snapshot['id'])):
                if key in value:
                    require(value[key] == expected, 'compiler_context_record_mismatch')
            if context['compiler_selected']:
                require(context_id in context['compiler_selected'], 'unselected_compiler_context')
            staged_contexts.add(context_id)
        if 'occurrence_id' in value:
            expected = self._compiler_binding(value['occurrence_id'])
            require(expected['context_id'] == context_id, 'compiler_occurrence_context_mismatch')
            if context['compiler_selected']:
                require(context_id in context['compiler_selected'], 'unselected_compiler_evidence')
            elif context['tool'] not in ('compiler_symbols', 'compiler_definitions'):
                raise ScopeViolation('compiler_evidence_requires_selected_context')
            if 'source_snapshot_id' in value:
                if matched_fact is not None:
                    require(context['tool'] in ('compiler_contract_impact', 'compiler_contract_paths') and
                            matched_fact in expected.get('domain_facts', []), 'compiler_matched_fact_mismatch')
                    expected['domain_facts'] = [matched_fact]
                require(value == expected, 'compiler_binding_record_mismatch')
            else:
                require(value == {k: expected[k] for k in ('occurrence_id', 'source_id', 'path', 'raw_sha256',
                        'byte_offset', 'byte_length', 'generated', 'reference_kind', 'role', 'binding_status',
                        'method', 'extractor', 'extractor_version')}, 'compiler_location_record_mismatch')
            args = context['arguments']
            if context['tool'] in ('compiler_symbols', 'compiler_definitions'):
                require(expected['role'] == 'declaration', 'compiler_reply_not_declaration')
            if context['tool'] == 'compiler_symbols' and args.get('query'):
                require(args['query'] in expected.get('symbol', {}).get('descriptor', ''),
                        'compiler_symbol_query_mismatch')
            if context['tool'] == 'compiler_definitions':
                require(expected.get('symbol', {}).get('id') == args['symbol_id'], 'compiler_definition_symbol_mismatch')
            if 'path' in args:
                require(expected['path'] == args['path'], 'compiler_reply_path_mismatch')
            if context['tool'] == 'compiler_binding_at':
                require(expected['byte_offset'] == args['byte_offset'], 'compiler_reply_offset_mismatch')
            staged_symbols.update(self._symbol_ids(expected))
            if 'symbol' in expected:
                staged_symbols.add(expected['symbol']['id'])
        elif 'path' in value and 'raw_sha256' in value:
            frozen, snapshot, repo = self._compiler_context(context_id)
            matches = [source_id for source_id in frozen['source_ids']
                       if self.compiler['sources'][source_id]['path'] == value['path']]
            require(len(matches) == 1, 'compiler_context_source_mismatch')
            _, _, source, _ = self._compiler_source(context_id, matches[0])
            expected = {'generated': source['generated'], 'context_id': context_id, 'project': frozen['project'],
                        'repo': repo, 'path': source['path'], 'raw_sha256': source['raw_sha256']}
            require(value == expected, 'compiler_context_source_mismatch')
            if 'path' in context['arguments']:
                require(value['path'] == context['arguments']['path'], 'compiler_reply_path_mismatch')
        for key, child in value.items():
            if key in ('context_ids', 'required_context_ids'):
                for identity in child:
                    self._compiler_context(identity)
                    staged_contexts.add(identity)
            elif key.endswith('symbol_id') or key in ('owner_id', 'contract_id'):
                if child:
                    self._compiler_symbol(child)
                    staged_symbols.add(child)
            self._compiler_walk(child, context, staged_symbols, staged_contexts, context_id,
                                value.get('fact') if key == 'source' and 'contract' in value else None)

    def _compiler_relations(self, value, args, inherited=None):
        """Check assembled evidence against its frozen occurrence, not just IDs."""
        if isinstance(value, list):
            for child in value:
                self._compiler_relations(child, args, inherited)
            return
        if not isinstance(value, dict):
            return
        context_id = value.get('context_id', inherited)
        source = value.get('source')
        if isinstance(source, dict) and 'occurrence_id' in source:
            binding = self.compiler_bindings[source['occurrence_id']]
            if set(value) == {'context_id', 'repo', 'project', 'source_snapshot_id', 'source'}:
                require(binding.get('symbol_id') == args.get('symbol_id') and
                        self.compiler['occurrences'][source['occurrence_id']]['role'] == 'declaration',
                        'compiler_contract_definition_mismatch')
            if 'symbol_id' in value:
                require(value['symbol_id'] == binding.get('symbol_id'), 'compiler_record_symbol_relation_mismatch')
            if 'api_symbol_id' in value:
                require(value['api_symbol_id'] == binding.get('symbol_id'), 'compiler_api_relation_mismatch')
            if 'owner_id' in value:
                require(value['owner_id'] == binding.get('enclosing_symbol_id'), 'compiler_owner_relation_mismatch')
            if 'owner' in value:
                require(value['owner']['id'] == binding.get('enclosing_symbol_id'), 'compiler_owner_relation_mismatch')
            for key in ('domain_facts', 'implementation_facts'):
                if key in value:
                    require(value[key] == binding.get(key, []), 'compiler_record_fact_relation_mismatch')
            if 'fact' in value:
                projected = 'contract' in value
                facts = [self._compiler_fact(v) if projected else v for v in binding.get('domain_facts', [])]
                require(value['fact'] in facts, 'compiler_fact_relation_mismatch')
                raw = binding['domain_facts'][facts.index(value['fact'])]
                roles = [v['role'] for v in raw['targets'] if v['symbol_id'] == args.get('symbol_id')]
                require(roles and value.get('matched_roles') == roles, 'compiler_fact_roles_mismatch')
                if 'contract' in value:
                    require(value['contract']['id'] == args.get('symbol_id'), 'compiler_contract_relation_mismatch')
            if 'implementation_fact_index' in value:
                index = value['implementation_fact_index']
                facts = binding.get('implementation_facts', [])
                require(type(index) is int and 0 <= index < len(facts) and
                        facts[index]['interface_symbol_id'] == args.get('symbol_id'), 'compiler_implementation_relation_mismatch')
            if 'interface_member' in value:
                require(any(value['kind'] == f['kind'] and value['rule'] == f['rule'] and
                            value['evidence_scope'] == f['evidence_scope'] and
                            value['interface_member']['id'] == f['interface_symbol_id'] and
                            value['implementing_type']['id'] == f['implementing_type_symbol_id']
                            for f in binding.get('implementation_facts', [])), 'compiler_implementation_relation_mismatch')
        if 'evidence' in value and isinstance(value['evidence'], list):
            for row in value['evidence']:
                binding = self.compiler_bindings[row['source']['occurrence_id']]
                require(value.get('owner_id', '') == binding.get('enclosing_symbol_id', ''),
                        'compiler_group_owner_mismatch')
        if 'witness' in value:
            witness = value['witness']
            require(value['caller']['id'] == witness.get('enclosing_symbol_id') and
                    value['commit'] == self.projects[witness['repo']]['commit'] and
                    1 <= value['depth'] <= args.get('depth', 1), 'compiler_call_relation_mismatch')
        if 'seed' in value and 'caller' in value:
            caller = value['caller']
            require(value['caller_owner']['id'] == caller.get('enclosing_symbol_id'), 'compiler_wrapper_owner_mismatch')
            target = value['seed'].get('owner', {}).get('id')
            if 'implementation' in value:
                implementation = value['implementation']
                require(implementation['source'].get('symbol', {}).get('id') == target and
                        implementation['source']['context_id'] == value['seed']['source']['context_id'],
                        'compiler_wrapper_implementation_mismatch')
                target = implementation['interface_member']['id']
            require(caller.get('symbol', {}).get('id') == target, 'compiler_wrapper_target_mismatch')
        for child in value.values():
            self._compiler_relations(child, args, context_id)

    def _accept_compiler(self, response, data, context):
        self._schema(data, self.compiler_tools[context['tool']]['outputSchema'])
        identity = self.compiler['identity']
        require(data.get('snapshot_id') == identity['snapshot_id'] and
                data.get('artifact_sha256') == identity['artifact_sha256'], 'compiler_artifact_binding_mismatch')
        meta = response['result'].get('_meta', {}).get('dev.moedex/snapshot')
        require(type(meta) is dict and meta.get('cacheable') is False and
                ('corpus_fingerprint' not in meta or meta['corpus_fingerprint'] == identity['corpus_fingerprint']),
                'compiler_snapshot_binding_mismatch')
        require('error' not in data and data['status'] in {'ok', 'context_required', 'source_not_found',
                'no_binding', 'no_definition', 'no_recorded_declaration', 'no_recorded_evidence',
                'no_recorded_paths', 'no_recorded_calls'}, 'compiler_unproven_status')
        for key, expected in (('evidence', 'recorded-compiler-context'), ('evidence_scope', 'compile_time'),
                              ('path_semantics', 'candidate_static')):
            require(key not in data or data[key] == expected, 'compiler_unproven_evidence_kind')
        args = context['arguments']
        for key in ('contract', 'interface', 'root'):
            if key in data:
                require(data[key]['id'] == args.get('symbol_id'), 'compiler_reply_root_mismatch')
        for key in ('symbol_id', 'contract_id'):
            if data.get(key):
                require(data[key] == args.get('symbol_id'), 'compiler_reply_symbol_mismatch')
        if 'context_ids' in data:
            require(data['context_ids'] == args.get('context_ids', []), 'compiler_reply_context_selection_mismatch')
        staged_symbols, staged_contexts = set(), set()
        self._compiler_walk(data, context, staged_symbols, staged_contexts)
        self._compiler_relations(data, args)
        if context['tool'] == 'compiler_evidence_path':
            allowed_definitions, allowed_calls = {args['symbol_id']}, set()
            for record in data['records']:
                if record['symbol_id'] != args['symbol_id'] or record['source']['role'] != 'declaration':
                    continue
                for fact in record.get('implementation_facts', []):
                    if fact['kind'] == 'interface_default_selection':
                        allowed_definitions.add(fact['default_template_symbol_id'])
                        if fact.get('forwarding'):
                            allowed_definitions.add(fact['forwarding']['implementation_symbol_id'])
                            allowed_calls.add(fact['forwarding']['call_occurrence_id'])
            for record in data['records']:
                require((record['source']['role'] == 'declaration' and record['symbol_id'] in allowed_definitions) or
                        record['source']['occurrence_id'] in allowed_calls or
                        any(target['symbol_id'] == args['symbol_id'] for fact in record.get('domain_facts', [])
                            for target in fact['targets']), 'compiler_evidence_path_unrelated_record')
        if context['tool'] == 'compiler_trace_calls':
            direction, depth = args.get('direction', 'outbound'), args.get('depth', 1)
            require(data['direction'] == direction and data['depth'] == depth, 'compiler_trace_request_mismatch')
            distances = {args['symbol_id']: 0}
            for edge in data['edges']:
                caller, target = edge['caller']['id'], edge['witness']['symbol']['id']
                require((direction != 'inbound' and distances.get(caller) == edge['depth'] - 1) or
                        (direction != 'outbound' and distances.get(target) == edge['depth'] - 1),
                        'compiler_trace_disconnected_edge')
                if direction != 'inbound':
                    distances.setdefault(target, edge['depth'])
                if direction != 'outbound':
                    distances.setdefault(caller, edge['depth'])
        checked = deepcopy(data)
        if 'limitations' in checked:
            checked['limitations'] = ['Historical compile-time metadata only; missing records do not establish absence or runtime behavior.']
        serialized = canonical(checked).decode()
        self.compiler_symbols.update(staged_symbols)
        self.compiler_contexts.update(staged_contexts)
        return {'jsonrpc': '2.0', 'id': response.get('id'), 'result': {
                'content': [{'type': 'text', 'text': serialized}], 'structuredContent': checked}}

    def project(self, value):
        require(type(value) is str and value in self.aliases, 'project_outside_frozen_scope')
        return self.aliases[value]

    def presentation_format(self, tool):
        return 'structured' if (self.backend == 'moedex-index-v1' and
                                tool in ('search_context', 'read_source')) else 'json'

    def catalog(self, tools):
        require({tool['name'] for tool in tools} == self.allowed, 'catalog_scope_roster_mismatch')
        result = deepcopy(tools)
        for tool in result:
            compiler = tool['name'] in COMPILER_TOOLS and self.compiler is not None
            tool['description'] = tool.get('description', '') + (
                '\nFrozen source scope: only approved pinned repositories are visible. '
                'Unknown or unproven provenance is replaced with a recoverable scope error. ') + (
                'Historical compiler metadata requires the exact frozen artifact; generated bodies are unavailable. '
                'Symbol and context IDs must come from this assignment\'s accepted replies.' if compiler else
                'Node IDs and cursors must come from this assignment\'s accepted replies.')
            properties = tool.setdefault('inputSchema', {}).setdefault('properties', {})
            if 'format' in properties:
                presentation = self.presentation_format(tool['name'])
                require('enum' not in properties['format'] or presentation in properties['format']['enum'],
                        'unsupported_structured_presentation')
                properties['format'] = dict(properties['format'], default=presentation, enum=[presentation])
                tool['description'] += f' Use format={presentation}.'
        return result

    def prepare(self, params):
        require(type(params) is dict and set(params) <= {'name', 'arguments', '_meta'}, 'invalid_tool_request')
        require(params.get('name') in self.allowed, 'tool_outside_source_scope')
        args = params.get('arguments', {})
        require(type(args) is dict, 'invalid_tool_arguments')
        presentation = self.presentation_format(params['name'])
        require(args.get('format', presentation) == presentation, 'structured_format_required')
        context = {'tool': params['name'], 'project': None, 'metadata': False}
        if self.backend == 'moedex-index-v1':
            if context['tool'] in COMPILER_TOOLS and self.compiler is not None:
                return self._prepare_compiler(args, context)
            if args.get('repo') is not None:
                context['project'] = self.project(args['repo'])
            if context['tool'] == 'list_repos':
                require(type(args.get('filter', '')) is str, 'invalid_repository_filter')
                context['repository_filter'] = args.get('filter', '')
            return context
        tool = params['name']
        if tool == 'codegraph_search' and args.get('scope') == 'projects':
            require(not any(args.get(key) is not None for key in ('project', 'label', 'nodeId', 'cursor')),
                    'project_search_ignores_scope_argument')
            context['metadata'] = True
            return context
        node, cursor, project = args.get('nodeId'), args.get('cursor'), args.get('project')
        if cursor is not None:
            require(tool == 'graph_source' and type(cursor) is str and cursor in self.cursors and
                    node is None and project is None and args.get('filePath') is None, 'unissued_or_conflicting_cursor')
            context['project'], context['path'] = self.cursors[cursor]
        elif node is not None:
            require(tool in ('graph_source', 'graph_trace') and type(node) is int and node in self.nodes and
                    project is None and args.get('filePath') is None, 'unissued_or_conflicting_node')
            context['project'], context['path'] = self.nodes[node]
        else:
            context['project'] = self.project(project)
            if tool == 'graph_source':
                path = relative_file(args.get('filePath'))
                require(path in self.projects[context['project']]['files'], 'source_not_in_frozen_manifest')
                context['path'] = path
        return context

    @staticmethod
    def error(request_id):
        # Never quote a rejected request, native error or outside project name.
        return {'jsonrpc': '2.0', 'id': request_id, 'result': {'isError': True,
                'content': [{'type': 'text', 'text': 'Source scope policy blocked this exchange. Use an approved project, the format advertised in the tool catalog, and selectors issued in this assignment.'}]}}

    def _structured(self, response):
        require(type(response) is dict and 'error' not in response and type(response.get('result')) is dict,
                'unproven_native_error')
        result = response['result']
        require(result.get('isError') is not True and type(result.get('structuredContent')) is dict,
                'missing_structured_provenance')
        data = result['structuredContent']
        # JSON text mirrors must agree. Markdown is never displayed: regenerate
        # the view solely from the checked native structured object below.
        for item in result.get('content', []):
            require(type(item) is dict and item.get('type') == 'text' and type(item.get('text')) is str,
                    'unsupported_native_content')
            try:
                mirrored = json.loads(item['text'])
            except ValueError:
                continue
            require(mirrored == data, 'conflicting_native_json_mirror')
        return data

    def _anchor(self, repository, record, require_blob=False):
        path = next((record[key] for key in ('path', 'filePath', 'file_path', 'rel_path')
                     if record.get(key) is not None), None)
        path = relative_file(path)
        files = self.projects[repository]['files']
        require(path in files, 'unanchored_source_record')
        blob = record.get('blobSha', record.get('blob_sha'))
        require(not require_blob or blob is not None, 'missing_source_blob_binding')
        if blob is not None:
            require(blob == files[path], 'source_blob_pin_mismatch')
        commit = record.get('commitSha', record.get('commit'))
        if commit is not None:
            require(commit == self.projects[repository]['commit'], 'source_commit_pin_mismatch')
        return path

    def _walk(self, value, bound, nodes, trail=()):
        if isinstance(value, list):
            for item in value:
                self._walk(item, bound, nodes, trail)
            return
        if not isinstance(value, dict):
            return
        repository = None
        for key in PROJECT_KEYS:
            if key in value and value[key] is not None:
                # Empty global query summaries are not source records.
                if trail == ('summary', 'scope') and value[key] in ('', '*'):
                    continue
                current = self.project(value[key])
                require(repository is None or repository == current, 'conflicting_project_identity')
                repository = current
                require(current in bound, 'project_missing_snapshot_pin')
        location = value.get('location')
        if isinstance(location, dict) and location.get('project') is not None:
            located = self.project(location['project'])
            require(located in bound and (repository is None or located == repository), 'conflicting_location_identity')
            repository = located
        node = value.get('nodeId', value.get('id') if 'project' in value and 'name' in value else None)
        if node is not None:
            if set(value) <= {'tool', 'nodeId'}:
                require(type(node) is int and node > 0 and (node in nodes or node in self.nodes),
                        'unanchored_graph_selector')
                return
            require(type(node) is int and node > 0 and repository is not None, 'unanchored_graph_node')
            anchor = location if isinstance(location, dict) else value
            if any(anchor.get(key) is not None for key in ('path', 'filePath', 'file_path', 'rel_path')):
                path = self._anchor(repository, anchor)
            else:
                previous = nodes.get(node, self.nodes.get(node))
                require(previous is not None and previous[0] == repository, 'unanchored_graph_node')
                path = previous[1]
            identity = (repository, path)
            require(node not in self.nodes or self.nodes[node] == identity, 'node_provenance_rebound')
            require(node not in nodes or nodes[node] == identity, 'node_provenance_conflict')
            nodes[node] = identity
        if repository is not None and any(key in value for key in ('path', 'filePath', 'file_path', 'rel_path')):
            self._anchor(repository, value)
        for key, child in value.items():
            if key in PROJECT_LIST_KEYS and isinstance(child, list):
                for member in child:
                    if isinstance(member, str):
                        require(self.project(member) in bound, 'project_missing_snapshot_pin')
            self._walk(child, bound, nodes, trail + (key,))

    def _project_metadata(self, data):
        require(type(data.get('result')) is dict and type(data['result'].get('projects')) is list,
                'unrecognized_project_metadata')
        projects = []
        for row in data['result']['projects']:
            require(type(row) is dict, 'invalid_project_metadata')
            url = row.get('repoUrl', row.get('repositoryUrl'))
            if url not in self.urls:
                continue
            repository = self.urls[url]
            commit = row.get('commitSha', row.get('indexedCommitSha'))
            require(commit == self.projects[repository]['commit'], 'project_metadata_pin_mismatch')
            projects.append({'project': repository, 'repoUrl': self.projects[repository]['url'], 'commitSha': commit})
        # No outside names, counts, free text or opaque selectors escape a
        # global metadata request. Only approved observed rows are rendered.
        return {'tool': 'codegraph_search', 'operation': 'projects', 'formatVersion': 1,
                'result': {'projects': projects, 'shownCount': len(projects)},
                'scopePolicy': {'filtered': True, 'scope': 'approved pinned project metadata only'}}

    def _node_shape(self, node):
        require(type(node) is dict and set(node) <= NODE_FIELDS and
                ('nodeId' in node or 'id' in node) and 'project' in node, 'unknown_graph_node_shape')
        for key in ('nodeId', 'id'):
            if key in node:
                require(type(node[key]) is int and node[key] > 0, 'invalid_graph_node_identity')
        require('nodeId' not in node or 'id' not in node or node['nodeId'] == node['id'],
                'conflicting_graph_node_identity')
        if 'location' in node:
            location = node['location']
            require(type(location) is dict and set(location) <= {'project', 'path', 'startLine',
                    'endLine', 'blobSha', 'commitSha'}, 'unknown_graph_location_shape')
            self._scalars(location, numeric=('startLine', 'endLine'),
                          strings=('project', 'path', 'blobSha', 'commitSha'))
        if 'traceSelector' in node:
            require(type(node['traceSelector']) is dict and set(node['traceSelector']) == {'tool', 'nodeId'}
                    and node['traceSelector']['tool'] == 'graph_trace', 'unknown_graph_selector_shape')
        if 'snippetSelector' in node:
            selector = node['snippetSelector']
            require(type(selector) is dict and set(selector) <= {'tool', 'project', 'filePath', 'startLine', 'endLine'}
                    and selector.get('tool') == 'graph_source', 'unknown_source_selector_shape')
            self._scalars(selector, strings=('project', 'filePath'))
            for key in ('startLine', 'endLine'):
                if key in selector:
                    require(type(selector[key]) is int and selector[key] >= 0, 'invalid_source_selector_scalar')
        for key in ('name', 'qualifiedName', 'label', 'project', 'dotnetProject', 'filePath', 'path', 'edgeType', 'risk'):
            if key in node:
                require(type(node[key]) is str or node[key] is None, 'invalid_graph_node_scalar')
        for key in ('riskFactors',):
            if key in node:
                require(type(node[key]) is list and all(type(v) is str for v in node[key]), 'invalid_graph_node_scalar')
        for key in ('startLine', 'endLine', 'depth'):
            if key in node:
                require(type(node[key]) is int and node[key] >= 0, 'invalid_graph_node_scalar')
        if 'doNotTrust' in node:
            require(type(node['doNotTrust']) is bool, 'invalid_graph_node_scalar')
        if 'trustScore' in node:
            require(type(node['trustScore']) in (int, float) and math.isfinite(node['trustScore']), 'invalid_graph_node_scalar')

    @staticmethod
    def _scalars(value, numeric=(), strings=(), flags=()):
        for key in numeric:
            if key in value:
                require(type(value[key]) in (int, float) and math.isfinite(value[key]) and value[key] >= 0,
                        'invalid_native_metadata_scalar')
        for key in strings:
            if key in value:
                require(type(value[key]) is str, 'invalid_native_metadata_scalar')
        for key in flags:
            if key in value:
                require(type(value[key]) is bool, 'invalid_native_metadata_scalar')

    def _codegraph_shape(self, data, context):
        result = data.get('result')
        require(type(result) is dict, 'unknown_native_result_shape')
        operation = data.get('operation')
        if context['tool'] == 'codegraph_search':
            require(set(result) <= {'totalCount', 'shownCount', 'results'} and
                    type(result.get('results')) is list, 'unknown_node_search_shape')
            for node in result['results']:
                self._node_shape(node)
            self._scalars(result, numeric=('totalCount', 'shownCount'))
        elif context['tool'] == 'graph_source':
            require(set(result) <= {'location', 'language', 'totalLines', 'lines'} and
                    type(result.get('location')) is dict and type(result.get('lines')) is list,
                    'unknown_source_result_shape')
            require(set(result['location']) <= {'project', 'path', 'startLine', 'endLine', 'blobSha', 'commitSha'},
                    'unknown_source_location_shape')
            self._scalars(result['location'], numeric=('startLine', 'endLine'),
                          strings=('project', 'path', 'blobSha', 'commitSha'))
            require(all(type(line) is dict and set(line) == {'number', 'text'} and
                    type(line['number']) is int and line['number'] >= 1 and type(line['text']) is str
                    for line in result['lines']), 'unknown_source_line_shape')
            self._scalars(result, numeric=('totalLines',), strings=('language',))
        elif operation == 'call_path':
            require(set(result) <= {'root', 'direction', 'depth', 'entries'} and
                    type(result.get('entries')) is list, 'unknown_call_path_shape')
            self._node_shape(result.get('root'))
            for entry in result['entries']:
                require(type(entry) is dict and set(entry) <= {'node', 'depth', 'edgeType', 'parentNodeId', 'edgeProperties'}
                        and 'node' in entry, 'unknown_call_entry_shape')
                self._node_shape(entry['node'])
                if 'edgeProperties' in entry:
                    require(type(entry['edgeProperties']) is dict and set(entry['edgeProperties']) <=
                            {'confidence', 'confidence_band', 'confidence_tier'}, 'unknown_graph_edge_properties')
                    self._scalars(entry['edgeProperties'], numeric=('confidence',), strings=('confidence_band', 'confidence_tier'))
                self._scalars(entry, numeric=('depth', 'parentNodeId'), strings=('edgeType',))
                if 'parentNodeId' in entry:
                    require(type(entry['parentNodeId']) is int and entry['parentNodeId'] > 0,
                            'invalid_parent_selector')
            self._scalars(result, numeric=('depth',), strings=('direction',))
        elif operation in ('consumers', 'publishers'):
            require(set(result) <= {'roots', 'count', operation} and type(result.get('roots')) is list and
                    type(result.get(operation)) is list, 'unknown_event_trace_shape')
            for node in result['roots'] + result[operation]:
                self._node_shape(node)
            self._scalars(result, numeric=('count',))
        elif operation == 'impact':
            require(set(result) <= {'depth', 'changedNodes', 'affectedNodes', 'crossRepoImpacts', 'summary'},
                    'unknown_impact_shape')
            for key in ('changedNodes', 'affectedNodes', 'crossRepoImpacts'):
                require(type(result.get(key)) is list, 'unknown_impact_nodes')
                for node in result[key]:
                    self._node_shape(node)
            summary = result.get('summary', {})
            require(type(summary) is dict and set(summary) <= {'totalAffected', 'crossRepoCount', 'criticalCount',
                    'highCount', 'mediumCount', 'lowCount', 'affectedProjects'}, 'unknown_impact_summary')
            self._scalars(result, numeric=('depth',))
            self._scalars(summary, numeric=('totalAffected', 'crossRepoCount', 'criticalCount', 'highCount', 'mediumCount', 'lowCount'))
            if 'affectedProjects' in summary:
                require(type(summary['affectedProjects']) is list and all(type(v) is str for v in summary['affectedProjects']), 'invalid_impact_projects')
        else:
            raise ScopeViolation('unrecognized_or_unscopable_trace_operation')
        if 'selection' in data:
            selection = data['selection']
            require(type(selection) is dict and set(selection) <= {'status', 'matchedVia', 'rootNodeIds', 'candidates'},
                    'unknown_selection_shape')
            require(type(selection.get('candidates', [])) is list, 'invalid_selection_candidates')
            for node in selection.get('candidates', []):
                self._node_shape(node)
            require(type(selection.get('rootNodeIds', [])) is list and
                    all(type(node) is int and node > 0 for node in selection.get('rootNodeIds', [])), 'invalid_root_selector')
            self._scalars(selection, strings=('status', 'matchedVia'))
        if 'page' in data:
            require(type(data['page']) is dict and set(data['page']) <= {'returned', 'total', 'truncated', 'nextCursor'},
                    'unknown_page_shape')
            self._scalars(data['page'], numeric=('returned', 'total'), flags=('truncated',))
            require(data['page'].get('nextCursor') is None or type(data['page']['nextCursor']) is str, 'invalid_page_cursor')
        if 'limits' in data:
            require(type(data['limits']) is dict and set(data['limits']) <= {'maxResults', 'depth', 'edgeTypes', 'filters'},
                    'unknown_limits_shape')
            self._scalars(data['limits'], numeric=('maxResults', 'depth'))
            if 'edgeTypes' in data['limits']:
                require(type(data['limits']['edgeTypes']) is list and all(type(v) is str for v in data['limits']['edgeTypes']), 'invalid_edge_types')
            if 'filters' in data['limits']:
                filters = data['limits']['filters']
                require(type(filters) is dict and set(filters) <= {'project', 'direction'}, 'unknown_filters_shape')
                self._scalars(filters, strings=('project', 'direction'))
                if filters.get('project') is not None:
                    require(self.project(filters['project']) == context['project'], 'filter_project_mismatch')

    def accept(self, response, context):
        data = self._structured(response)
        staged_nodes, staged_cursors, revision = {}, {}, self.revision
        if self.backend == 'codegraph-v1':
            if context['metadata']:
                require(data.get('tool') == context['tool'] and data.get('formatVersion') == 1,
                        'unexpected_native_envelope')
                checked = self._project_metadata(data)
                # These projects were independently frozen before this request.
                # Mixed/global metadata cannot mint selectors, establish source
                # eligibility, or change the assignment's structure revision.
                serialized = canonical(checked).decode()
                return {'jsonrpc': '2.0', 'id': response.get('id'), 'result': {
                    'content': [{'type': 'text', 'text': serialized}], 'structuredContent': checked}}
            require(set(data) <= {'tool', 'operation', 'formatVersion', 'snapshot', 'selection',
                    'limits', 'page', 'warnings', 'durationMs', 'result'}, 'unknown_native_envelope_field')
            require(data.get('tool') == context['tool'] and data.get('formatVersion') == 1, 'unexpected_native_envelope')
            self._scalars(data, strings=('operation',), numeric=('durationMs',))
            snapshot = data.get('snapshot')
            require(type(snapshot) is dict and snapshot.get('cacheable') is True and
                    set(snapshot) <= {'graphRevision', 'projects', 'cacheable', 'resultDigest'} and
                    type(snapshot.get('graphRevision')) is int and snapshot['graphRevision'] >= 0,
                    'unrecorded_or_uncacheable_snapshot')
            if 'resultDigest' in snapshot:
                require(type(snapshot['resultDigest']) is str and SHA.fullmatch(snapshot['resultDigest']), 'invalid_result_digest')
            incoming = snapshot['graphRevision']
            require(revision is None or incoming == revision, 'incompatible_graph_revision')
            revision = incoming
            require(data.get('warnings', []) == [], 'native_warning_unproven_scope')
            rows = snapshot.get('projects')
            result = data.get('result')
            if (context['tool'] == 'codegraph_search' and rows == [] and
                    type(result) is dict and set(result) <= {'totalCount', 'shownCount', 'results'} and
                    result.get('results') == [] and result.get('totalCount', 0) == 0 and
                    result.get('shownCount', 0) == 0 and data.get('selection') is None):
                checked = {'tool': context['tool'], 'operation': 'nodes', 'formatVersion': 1,
                           'result': {'totalCount': 0, 'shownCount': 0, 'results': []}}
                self.revision = revision
                return {'jsonrpc': '2.0', 'id': response.get('id'), 'result': {
                    'content': [{'type': 'text', 'text': canonical(checked).decode()}], 'structuredContent': checked}}
            require(type(rows) is list and rows, 'missing_snapshot_projects')
            bound = set()
            for row in rows:
                require(type(row) is dict and set(row) == {'project', 'commitSha'}, 'invalid_snapshot_project')
                repository = self.project(row['project'])
                require(repository not in bound and row['commitSha'] == self.projects[repository]['commit'], 'snapshot_pin_mismatch')
                bound.add(repository)
            require(context['project'] in bound, 'requested_project_missing_snapshot_pin')
            self._codegraph_shape(data, context)
            if context['tool'] == 'codegraph_search':
                require(type(result) is dict and set(result) <= {'totalCount', 'shownCount', 'results'} and
                        type(result.get('results')) is list and all(type(row) is dict and 'nodeId' in row
                        for row in result['results']), 'unrecognized_node_search_result')
            self._walk(data.get('result'), bound, staged_nodes)
            self._walk(data.get('selection'), bound, staged_nodes)
            if context['tool'] == 'graph_source':
                result = data.get('result', {})
                location = result.get('location')
                require(type(location) is dict and self.project(location.get('project')) == context['project'], 'source_selector_provenance_mismatch')
                path = self._anchor(context['project'], location, require_blob=True)
                require(location.get('commitSha') == self.projects[context['project']]['commit'], 'missing_source_commit_binding')
                require('path' not in context or path == context['path'], 'source_selector_path_mismatch')
                cursor = data.get('page', {}).get('nextCursor')
                if cursor is not None:
                    require(type(cursor) is str and cursor, 'invalid_native_cursor')
                    identity = (context['project'], path)
                    require(cursor not in self.cursors or self.cursors[cursor] == identity, 'cursor_provenance_rebound')
                    staged_cursors[cursor] = identity
            issued = set(self.nodes) | set(staged_nodes)
            require(all(node in issued for node in data.get('selection', {}).get('rootNodeIds', [])),
                    'unanchored_root_selector')
            if data.get('operation') == 'call_path':
                require(all(entry.get('parentNodeId') is None or entry['parentNodeId'] in issued
                            for entry in result['entries']), 'unanchored_parent_selector')
            checked = data
        elif context['tool'] in COMPILER_TOOLS and self.compiler is not None:
            return self._accept_compiler(response, data, context)
        else:
            meta = response['result'].get('_meta', {}).get('dev.moedex/snapshot')
            require(type(meta) is dict and meta.get('cacheable') is True and
                    meta.get('corpus_fingerprint') == self.fingerprint, 'index_snapshot_binding_mismatch')
            self._walk(data, set(self.projects), staged_nodes)
            if context['tool'] == 'list_repos':
                require(type(data.get('repos')) is list, 'unrecognized_repo_catalog')
                identities = [self.project(row.get('name')) for row in data['repos'] if isinstance(row, dict)]
                required = {name for name, project in self.projects.items() if not project.get('metadata_only')}
                repository_filter = context['repository_filter']
                require(len(identities) == len(data['repos']) and set(identities) <= set(self.projects) and
                        len(set(identities)) == len(identities), 'physical_index_scope_mismatch')
                if repository_filter:
                    require(all(repository_filter.lower() in row['name'].lower() for row in data['repos']),
                            'repository_filter_mismatch')
                else:
                    require(required <= set(identities), 'physical_index_scope_mismatch')
            if context['tool'] == 'read_source':
                repository = self.project(data.get('repo'))
                require(repository == context['project'], 'source_selector_provenance_mismatch')
                self._anchor(repository, data, require_blob=True)
            checked = data
        serialized = canonical(checked).decode()
        # Publish selectors atomically only after every returned record passes,
        # including successful finite JSON serialization.
        self.nodes.update(staged_nodes)
        self.cursors.update(staged_cursors)
        self.revision = revision
        visible = {'content': [{'type': 'text', 'text': serialized}], 'structuredContent': checked}
        return {'jsonrpc': '2.0', 'id': response.get('id'), 'result': visible}
