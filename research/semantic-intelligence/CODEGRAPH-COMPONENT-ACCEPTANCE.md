# CodeGraph extraction component comparison

Recorded October 1, 2026. Local development diagnostic; no deployment or product-level win.

Moedex and unchanged CodeGraph extraction components were evaluated against the
same 19 source observations in pinned MassTransit Sample-Outbox. Moedex records
all 19 with exact source occurrences. CodeGraph records eight at owner level and
four as partial evidence; seven required semantic assertions are not represented.
The result demonstrates the selected compiler
evidence capabilities. Outbox guided Moedex development, so it cannot establish
held-out quality or performance on agent tasks.

## Frozen comparison

The [protocol](results/codegraph-component-outbox-20261001/protocol.json) was frozen
before inspecting CodeGraph output, SHA-256
`c249d3d5f8eb4101c9c0f68cd1739847afd2251be72ece43e1b89a4c96bfb644`.
It uses source commit `1ab8e66ebf96e5733e68c2f4d2201276f38ed9c5`, three projects
in Debug/net8.0, and 26 reviewed C# source hashes. Observations comprise 15 domain
facts, two interface calls, and two exact method implementation relationships.
Identical observations across Moedex's four retained compilation contexts count
once in the primary table; each context remains in the evidence.

| Selected observation family | Moedex exact occurrence | CodeGraph owner level | CodeGraph partial | CodeGraph not represented |
| --- | ---: | ---: | ---: | ---: |
| Domain observations (15) | 15 | 6 | 2 | 7 |
| Interface calls (2) | 2 | 2 | 0 | 0 |
| Method implementation relationships (2) | 2 | 0 | 2 | 0 |

Owner-level evidence supports the qualified semantic relationship but does not
identify an exact invocation span. Native type-level `IMPLEMENTS` does not prove
which method implements a particular interface member. Configuration facts
remain separate from direct publication and consumption. Missing native facts
are not evidence of runtime absence.

Four negative controls produce no forbidden assertions in Moedex. CodeGraph
attributes both wrapper owners and emits no forbidden direct publisher edge for
their selected messages. Its edge schema cannot attribute assertions to the two
individual logging/configuration invocations, so those negatives remain
attribution-unavailable rather than passing by absence.

Review corrected the first CodeGraph score: two qualified EF API calls provide
partial bound-API evidence even though they lack entity-use/mapping relations.
The initial report and scorer are retained alongside the corrected version.
Three additional configuration API calls have native class-level attribution,
which cannot be promoted to the required constructor identity; they remain
supplemental evidence. This correction changes partial coverage, not the eight
fully supported semantic assertions.

The independent extras review retains 36 additional CodeGraph type-level
`IMPLEMENTS` edges: six direct source interfaces, four record-generated
`IEquatable` relationships, and 26 framework/inherited relationships outside the
selected gold. These are unscored capabilities, not false positives. Moedex has
no additional domain or implementation facts within the reviewed closure;
ordinary invocation inventory is retained separately. Neither arm receives a
broad precision score from this selected diagnostic.

## Native execution and provenance

The [component runner](codegraph-component/README.md) compiles 17 byte-identical
reference files with the original six direct public dependency versions. The
reference copy has no Git metadata; compiled-file rosters and hashes identify
the source. The host calls native `SolutionAnalyzer.AnalyzeSolutionAsync`,
`CodeGraphSyntaxWalker`, and `GraphBuffer.ResolveEdges`. Raw extraction and
resolved-buffer outputs remain separate.

The production CodeGraph service depends on private packages and MySQL. This
run excludes its private call resolver, store, query/MCP service, cross-repository
linker, and model-backed enrichment. It is not a replacement implementation of
those components. All 40 tracked application files remain unchanged. A separate
workspace probe sees all 26 reviewed C# files, three compilations, and no compiler
errors; it does not create graph evidence or prove native visitation of every
document. Native extraction returns 35 document results.

Moedex uses the previously accepted worker6 artifact, SHA-256
`24a95f5ad7307d6f3fdaf151e230b26e5cf72d2fec41971476fc4f7135cdac70`.
The scorer checks its envelope, exact baseline hash, complete worker version,
project/configuration roster, source closure in every context, and frozen source
hashes. No detector changes were made for this comparison. Both arms preserve
native identities and evidence; missing relations or callsite spans are never
reconstructed from gold.

Nineteen scorer tests cover overload and namespace separation, project ambiguity,
unresolved calls, source spans, context omissions, lifetime mismatch, wrapper
misclassification, and extra facts on otherwise matching bindings. All pass.
The [evidence manifest](results/codegraph-component-outbox-20261001/result.json) retains
the protocol, arm reports, extras review, and test log. Raw captures and build
dependencies remain in the ignored persistent `.local/application-impact/`
workspace. No GPU or embeddings are involved.

## Remaining acceptance

No end-to-end latency comparison is claimed: compiler versions, build closures,
and processing stages differ. A discovery-inclusive agent comparison remains
separate and requires the real CodeGraph product surface. The twelve proposed
Outbox journeys are developmental pilots, not executed tasks or the PROGRAM's
30-task gate.

The next independent capability test uses eShop's Webhooks.API at
`f2369529433374a01b864b6fa1499ad894756f53`. Its source-only labels cover 57 C# files
in five projects and were frozen before capture. That gate must report both
supported cases and custom event-bus/Aspire/EF callback gaps without tuning the
detectors against its first result.
