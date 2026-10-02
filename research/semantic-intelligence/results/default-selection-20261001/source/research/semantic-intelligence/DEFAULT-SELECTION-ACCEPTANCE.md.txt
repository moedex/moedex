# Closed-class default selection acceptance

Date: 2026-10-01. Single-threaded, CPU-only development successor; no new agent
evaluation, heldout score, or competitive CodeGraph claim.

Worker8 closes the selection gap left by worker7: all three eShop Webhooks handler
classes now identify the closed default method Roslyn selects for the nongeneric
`IIntegrationEventHandler.Handle` entry point. The selected method preserves the
qualified event type. A separate source-template identity resolves its body.

## Public observations

| Handler event | Calls | Complete response bytes | Result |
|---|---:|---:|---|
| OrderStatusChangedToPaidIntegrationEvent | 9 | 48,318 | pass |
| OrderStatusChangedToShippedIntegrationEvent | 8 | 45,943 | pass |
| ProductPriceChangedIntegrationEvent | 7 | 39,096 | pass |

Each source-authored walkthrough starts with search, inspects captured declarations,
discovers reverse-query contexts, selects the handler project, and resolves the
default template. It verifies that the selection and handler correspondence carry
the same qualified event argument. The returned template source is hash-checked,
including its UTF8 BOM, before querying the exact forwarding-call offset. Every
compiler response in a case has one snapshot/artifact identity.

The three workflows total 24 calls and 133,357 bytes across **separate**
24-call/131,072-byte budgets. Catalog retrieval is separately retained and uncharged.
The different call counts include same-basename search candidates with no captured
compiler declaration. These are scripted development checks, not independent
solvers or a controlled before/after efficiency comparison.

## Meaning and boundary

`interface_default_selection` is anchored at a class declaration and contains
the entry point, concrete class, closed selected default, and source template.
It does not replace worker7's method correspondence or open-template fact.
Reverse lookup can return all these separately labelled kinds.

Selection does not imply forwarding: a default may have an arbitrary body. The
eShop walkthrough checks its actual cast and call separately. Automatic forwarding
edges, successful runtime casts, the actual receiver type, broker activation,
and execution remain unproved. `compiler_contract_paths` excludes template and
selection facts from implementation hops, including rejection at the MCP reader
boundary. See [ADR 0048](../../docs/adr/0048-closed-class-default-interface-selection.md).

Next: a bounded, versioned forwarding observation carrying the template call,
receiver, cast/substitution obligations, and selected target correspondence.
Fresh independent tasks and paired CodeGraph evaluation remain separate gates.

## Capture and compatibility

- Unchanged eShop pin `f2369529433374a01b864b6fa1499ad894756f53`;
  1,057 tracked source-file hashes preserved, including 57 reviewed C# sources.
- Five complete contexts, 73 captured sources, 1,487 symbols, 3,946 bindings,
  and 67 existing hidden diagnostics. Three new class-selection facts.
- SDK8.0.400 with pinned Roslyn4.11 assets; offline dependency manifest
  `0ce95ba1f33fcc5b5bc162733a7a2a831439718fd8a7686238463ea30034e736`.
- Artifact SHA256:
  `ebc7ce4bdfa72eae75ccb9f4800fb467613397601bbc60fd6432c17dec72750f`.
- Separate local capture/index under `.local/default-selection/eshop`;
  graph omitted, embeddings disabled. Prior snapshots and gold remain untouched.
- Existing nine supported Webhooks assertions and two HTTP negatives pass.
  Five original domain gaps retain their old labels: new selection evidence is
  not relabelled as message-consumer evidence.
- Preserved worker6 public reverse queries and worker7 native public round trips
  pass with the updated reader. Index layout remains v5; new observations require
  worker8 capture and a supporting reader. Old readers reject the new rule/fields.

## Validation

- Native two-project fixture: six positive selections; distinct qualified
  arguments, most-specific override, shared diamond, inherited default, arbitrary
  body; eight excluded classes/structs; 33-fact overflow; metadata-only bodies;
  ambiguous compilation suppression in the affected project.
- Native artifact import/write/read/index/open and public symbol/reverse/definition
  tests, including cross-project template lookup and no implicit closed-to-open
  definition collapse. Expanded fixture also passes under the race detector.
- Malformed semantic and MCP evidence checks: missing witnesses, wrong anchors,
  wrong slots/bodies/namespaces, metadata substitution, rule/version downgrade,
  and runtime-evidence claims.
- Worker7 framework fixture and legacy correspondence fixture pass under worker8.
  The legacy harness initially selected the wrong restore SDK; running restore
  in its pinned fixture directory corrected this. The failed attempt is retained.
- SDK10 native worker regression passes its existing 19 labels and capture,
  generator, deterministic identity, UTF8, context, and failure controls.
- Full `go test ./...` (native public gates enabled), `go vet ./...`,
  `go build ./...`, and affected five-package race suite pass.

Independent agent coverage remains **3/12**. No additional large-corpus resource
measurement or production deployment is claimed.

[Hashed evidence inventory](results/default-selection-20261001/result.json)
retains public transcripts, native captures, test logs, provenance, implementation
source copies, and predecessor inventory verification. Expensive binaries and
indexes remain local, outside the tracked evidence archive.
