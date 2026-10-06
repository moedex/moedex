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
    def __init__(self, policy, policy_sha256=None):
        require(type(policy) is dict and set(policy) == {'schema', 'backend', 'allowed_tools',
                'projects', 'corpus_fingerprint', 'graph_revision'}, 'invalid_scope_policy')
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
            require(type(row) is dict and set(row) == {'repository', 'url', 'commit', 'aliases', 'files'}, 'invalid_scope_project')
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
            require(type(row['files']) is dict and row['files'], 'missing_source_manifest')
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
        require(policy_sha256 is None or type(policy_sha256) is str and SHA.fullmatch(policy_sha256),
                'invalid_policy_file_binding')
        self.policy_sha256 = policy_sha256 if policy_sha256 is not None else digest(policy)

    def project(self, value):
        require(type(value) is str and value in self.aliases, 'project_outside_frozen_scope')
        return self.aliases[value]

    def catalog(self, tools):
        require({tool['name'] for tool in tools} == self.allowed, 'catalog_scope_roster_mismatch')
        result = deepcopy(tools)
        for tool in result:
            tool['description'] = tool.get('description', '') + (
                '\nFrozen source scope: only approved pinned repositories are visible. '
                'Unknown or unproven provenance is replaced with a recoverable scope error. '
                'Use format=json. Node IDs and cursors must come from this assignment\'s accepted replies.')
            properties = tool.setdefault('inputSchema', {}).setdefault('properties', {})
            if 'format' in properties:
                properties['format'] = dict(properties['format'], default='json', enum=['json'])
        return result

    def prepare(self, params):
        require(type(params) is dict and set(params) <= {'name', 'arguments', '_meta'}, 'invalid_tool_request')
        require(params.get('name') in self.allowed, 'tool_outside_source_scope')
        args = params.get('arguments', {})
        require(type(args) is dict, 'invalid_tool_arguments')
        require(args.get('format', 'json') == 'json', 'structured_format_required')
        context = {'tool': params['name'], 'project': None, 'metadata': False}
        if self.backend == 'moedex-index-v1':
            if args.get('repo') is not None:
                context['project'] = self.project(args['repo'])
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
                'content': [{'type': 'text', 'text': 'Source scope policy blocked this exchange. Use an approved project, JSON format, and selectors issued in this assignment.'}]}}

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
        else:
            meta = response['result'].get('_meta', {}).get('dev.moedex/snapshot')
            require(type(meta) is dict and meta.get('cacheable') is True and
                    meta.get('corpus_fingerprint') == self.fingerprint, 'index_snapshot_binding_mismatch')
            self._walk(data, set(self.projects), staged_nodes)
            if context['tool'] == 'list_repos':
                require(type(data.get('repos')) is list, 'unrecognized_repo_catalog')
                identities = [self.project(row.get('name')) for row in data['repos'] if isinstance(row, dict)]
                require(len(identities) == len(data['repos']) and set(identities) == set(self.projects) and
                        len(set(identities)) == len(identities), 'physical_index_scope_mismatch')
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
