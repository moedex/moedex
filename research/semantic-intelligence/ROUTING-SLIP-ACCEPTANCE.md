# Bounded routing-slip configuration acceptance

Date: 2026-10-01. Status: accepted for compiler-grounded configuration witnesses.
Single-agent, CPU-only execution.

## Result

The frozen ForkJoint routing-slip anchor now links GrillBurgerActivity,
GrillBurgerArguments, BurgerItineraryPlanner._grillAddress and the exact
IEndpointNameFormatter.ExecuteActivity API. The activity named at AddActivity
matches the constructor formatter's type argument. The declared URI uses the
exchange prefix; no final endpoint string or deployment reachability is asserted.

The frozen call remains at byte 1003 in
`src/ForkJoint.Api/Components/ItineraryPlanners/BurgerItineraryPlanner.cs`, SHA256
`d1f3dd05432cf43ddcb94d8bf80c5a3f26d8af116da46a5308a9bbeeac4b6494`.
Only the routing-slip expectation changes from the worker17 baseline.

| Frozen application | Requested facts | Resolved anchors |
| --- | --- | --- |
| eShopOnWeb | 8/8, unchanged | 10/10 |
| ForkJoint | 11/11, previously 10/11 | 11/11 |
| Combined | 19/19 | 21/21 |

Two binding-only expectations remain outside the fact denominator. This completes
the frozen source-authored observation set, not the overall CodeGraph objective.
Independent solver coverage remains 3/12; the paired CodeGraph comparison still
requires private dependencies and its database setup.

## Rule boundary

Worker18 adds `routing_slip_activity_configuration`, rule `csharp-routing-slip-v1`.
Targets are activity, arguments, address_field and formatter_api, all preserving
qualified identities. The API must be the exact IItineraryBuilder object-payload
overload, with a nameof type label and a current-instance private readonly field.

A single source constructor must reference that uninitialized field exactly once,
in a direct top-level assignment on the same instance. Constructor analysis is
limited to 32 statements and 1,024 descendant syntax nodes. The assignment must be
Uri(string) over exactly `exchange:` plus a direct generic ExecuteActivity call.
No interpolation alignment/formatting is allowed. Activity and argument types must
be simple named types, and the label/formatter activity identities must agree.

This does not validate payload shape, resolve the actual formatter or URI, execute
the itinerary, or prove compensation, reachability or delivery. Multiple constructors,
conditional/repeated assignments, aliases, mutable/public/initialized fields,
other-instance access, factories, alternative URI patterns and API overloads are
excluded. Compiler provenance establishes the cross-method witness; public source
navigation exposes its declarations for inspection.

## Validation and evidence

- Full Go suite, vet, and semantic/import/index/MCP race checks with all twelve
  native fixtures enabled.
- New native fixture: five positives, nineteen exclusions, constructor statement
  and node limits, exact role identities and incomplete-compilation suppression.
- Artifact persistence, index reopening, public binding/definition/impact/context
  navigation and response-schema validation.
- Eleven earlier native fixtures pass on worker18; all eleven retained worker17
  streams pass public import compatibility checks.
- Worker6–18 persisted implementation compatibility; new-rule downgrade/future
  rejection and field/formatter target validation. Both worker builds are clean.
- Frozen commits, tracked source bytes and gold remain unchanged. All previous
  observations persist and the routing-slip observation is the sole addition.

Evidence: manifest (archival evidence maintained separately),
public navigation (archival evidence maintained separately),
eShopOnWeb observations (archival evidence maintained separately),
ForkJoint observations (archival evidence maintained separately).
The archive preserves final sources, native streams, public requests/responses,
capture metadata and validation logs. Older milestone archives remain unchanged;
large caches, binaries, corpora and generated indices stay local.

## Independent evaluation readiness

The next gate is a fresh task-level solver evaluation through the logged public MCP
client, not a coordinator-authored walkthrough. The existing harness allows 24
calls, 131,072 serialized response bytes and 600 seconds per task; it records raw
requests/responses and does not enforce host filesystem isolation. That limitation
must remain disclosed and solver transcripts reviewed.

The coordinator has seen source and gold and cannot supply an independent score.
The user's single-threaded/no-subagents constraint remains in force, so no fresh
solver was launched. Before a trial, select and freeze its task prompts and source
gold separately, publish the corresponding current snapshot, and supply only the
prompt and public catalog to a fresh solver. The existing 3/12 score comes from the
separate journey suite and must not be replaced with this 19/19 observation count.
