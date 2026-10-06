# Frozen native source scope

`native_scope.py` provides an opt-in host broker boundary for future isolated
assignments. It checks native provenance before applying the model display cap.
Historical freezes without a scope policy retain their existing behavior.
Corpus comparisons run manually; the tests use synthetic records only.

The policy covers the full shared background corpus at matching source pins.
A task's target repository subset does not reduce that background. Freeze the
policy file's `{path, sha256}` reference as `source_scope_policy` in both the
execution freeze and execution configuration, add `native_scope.py` to frozen
runner hashes, and retain independent setup review as `source_scope_review`.
The driver rejects a policy whose tools, background roster or pins differ from
the contract and creates fresh selector state for every assignment.

## Policy format

```json
{
  "schema": "native-source-scope-v1",
  "backend": "codegraph-v1",
  "allowed_tools": ["codegraph_search", "graph_source", "graph_trace"],
  "projects": [{
    "repository": "example/api",
    "url": "https://gitlab.example.com/example/api",
    "commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "aliases": ["Example.Api"],
    "files": {"src/api.cs": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
  }],
  "corpus_fingerprint": null,
  "graph_revision": null
}
```

Repository identities may be namespace paths or the exact HTTPS URL. Aliases
must be explicitly observed and unique; substring and case guessing are
rejected. Files use normalized relative paths and 40-character content keys.
For `moedex-index-v1`, freeze the actual 64-character index corpus fingerprint
and leave `graph_revision` null. Verify emitted Moedex content keys against the
served manifest: ingest strips a leading UTF-8 BOM, so a served key may differ
from the original Git blob. Record that normalization in private setup evidence.

The coordinator must independently verify source manifests, access, source pins,
server identity and deployment. Native structured provenance is a server
assertion; this module does not authenticate it. A Moedex deployment must contain
only the approved physical background corpus, including every derived graph or
compiler artifact. An unfiltered repository catalog must match the policy roster.
Filtered catalogs may return a unique pinned subset or an empty result; every
returned repository must match the requested case-insensitive substring.

## Model boundary and accounting

The catalog advertises each tool's native structured presentation: `json` for
CodeGraph and `structured` for Moedex `search_context` and `read_source`. The
adapter rejects incompatible advertised format enums before solving starts.
Omitting the format remains supported because native structured content is
validated independently of the text fallback. Requests naming outside repositories, unissued
node IDs or cursors are denied before product dispatch. A denied intent consumes
one native call attempt and zero native response bytes. Dispatched responses are
retained in full and charged in full even when provenance fails. The model gets
a fixed recoverable scope error without outside names, quoted arguments or
native error prose. Late and budget-crossing replies cannot register selectors
or be displayed.

CodeGraph project discovery emits only approved URL and commit metadata. A row
may name its recorded commit directly, or omit it when a cacheable, warning-free
snapshot uniquely binds that exact project to its frozen commit. Explicit null,
wrong or conflicting pins are rejected. This metadata projection does not mint
selectors or update the assignment's source graph revision. Node-search `label`
and string-encoded `exact` response filters must agree with the request, including
the default `exact=false`; other provenance and tracked-file checks still apply.

Every displayed native exchange has a hashed decision receipt bound to the
policy file, response ordinal and raw hash, displayed bytes, revision and selector
counts. Audits verify those bindings. Native Markdown is discarded and the
display is regenerated from the validated structured object; JSON text mirrors
must agree. This presentation change must be disclosed in the protocol.

## CodeGraph compatibility limits

Content-bearing replies require cacheable structured provenance, exact approved
project pins, source anchors and no warnings. Known search, source, call-path,
publisher, consumer and impact shapes are checked; unknown fact-bearing fields
and unsupported trace shapes are withheld. A neighbor without a file anchor is
admitted only if its identical node was previously anchored in this assignment.
No hidden provenance lookup calls are made. These limits can withhold useful
native results and must be reported with failure and overhead denominators.
RAG, database, documentation, telemetry and other unscopable global tools are
outside the supported primary tool roster.

Node IDs and source cursors become usable only after the entire reply passes.
Root and parent references also need accepted anchors. A fixed graph revision is
enforced when supplied; otherwise the first accepted structural reply locks the
assignment revision. Later revision drift produces a recoverable error. Live
publication churn can therefore reduce observed coverage; retain and disclose
those failures without resetting the assignment or silently changing revision.

Global project metadata is an explicit exception: it renders only rows whose
canonical URL and exact pin were independently frozen beforehand. Outside names,
global counts, warnings, prose and unknown fields are stripped. Mixed or
uncacheable global metadata cannot mint selectors, establish source eligibility
or change the structural revision. Valid source-free empty node searches retain
an empty result view; other unproven statuses are withheld.

Run the generic tests with:

```sh
python3 -B -m unittest discover -s research/semantic-intelligence/agent-journeys -p 'test_*.py'
```
