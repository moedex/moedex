# Generic handler correspondence acceptance

Date: 2026-10-01. Status: accepted for bounded compiler correspondence.
Single-agent, CPU-only execution.

## Result

The frozen eShopOnWeb `orders-handler` anchor now records the exact implementation
of `IRequestHandler<GetMyOrders, IEnumerable<OrderViewModel>>`. Public MCP verifies
that both closed interface arguments equal the request and response keys from the
existing dispatch observation, then navigates the interface slot to the handler's
source definition at byte 624. The frozen file SHA256 remains
`a395b93091bae562db99e47e4efca592dc9a11a5b8d5909aa034e02364589940`.

| Frozen application | Requested facts | Resolved anchors | Remaining gaps |
| --- | --- | --- | --- |
| eShopOnWeb | 6/8, previously 5/8 | 10/10 | Assembly scan, open-generic repository registration |
| ForkJoint | 8/11, unchanged | 11/11 | Consumer scan, activity scan, routing slip |
| Combined | 14/19 | 21/21 | Five |

Two binding-only expectations are excluded from the fact denominator. These are
source-authored development observations, not independent solver completions.
Independent solver coverage remains 3/12. The paired CodeGraph comparison still
requires its private dependencies and database setup.

## Rule and identity bounds

Worker14 emits `interface_method_implementation` under
`csharp-interface-closed-v2`, with `compile_time` scope. Its
`constructed_interface_method_v2` identity carries the original method ID and
qualified argument keys. One constructed named type level is allowed per argument,
with at most eight arguments at each level and a 32 KiB outer descriptor limit.
At least one constructed argument is required; simple closed interfaces retain
their old v1 identities.

The source method must be ordinary, nongeneric, nonstatic and concrete, on a
nongeneric class. Roslyn must map the exact abstract interface member to that
source method. No inherited declaration is fabricated. Open/deeper generic,
array, tuple, overly wide and explicit generic class mappings are excluded; the
32-fact limit and previous default selection/forwarding bounds remain.

The shared bounded type builder preserves the existing MediatR response keys.
Dispatch argument equality and declaration correspondence do not establish which
handler a runtime container selects, or whether a call executes.

## Validation and evidence

- Full Go suite, vet and semantic/import/index/MCP race checks with all eight
  native fixtures enabled.
- New native fixture: three v2 handler mappings, stable v1 mapping, nine exclusions,
  distinct qualified response types, multiple handlers, interface call-key equality,
  dispatch response-key equality, and incomplete-compilation suppression.
- Native import, artifact write/read, persisted index reopen and public MCP
  `compiler_implementations` navigation, including response schema validation.
- Seven earlier native fixtures and their retained worker13 public captures pass.
- Worker6–14 persisted implementation compatibility and new-rule downgrade/future
  version rejection; pinned and package-free worker builds have zero warnings/errors.
- Both application captures preserve frozen commits, source hashes and source gold.
  The orders-handler observation is the only changed frozen expectation.

Evidence: manifest (archival evidence maintained separately),
public handler navigation (archival evidence maintained separately),
eShopOnWeb observations (archival evidence maintained separately),
ForkJoint observations (archival evidence maintained separately).
The archive includes final source snapshots, native streams, capture manifests,
public requests/responses and validation logs. Previous milestone archives remain
unchanged. Large dependency caches, corpora, binaries and index shards stay local.

## Next move

Bounded open-generic DI registration evidence is the next tractable frozen gap.
Treat it as a registration template with explicit type-parameter correspondence;
do not manufacture closed runtime services or silently extend existing simple DI
facts. Assembly scanning and routing-slip evidence remain separate work.
