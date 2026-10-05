# Worker20 native build severity and compiler readiness

2026-10-02. **Pinned CleanArchitecture compiler capture now succeeds**, without
source edits, package changes or warning suppression. The artifact publishes into
an isolated snapshot and serves native compiler queries. This closes its capture
blocker; it is development evidence after debugging, not an independent holdout
score or paired CodeGraph result.

## Result

| Check | Result |
| --- | --- |
| Managed offline capture | Six complete compiler contexts |
| Captured evidence | 94 sources; 1,649 symbols; 4,884 occurrences/bindings |
| Workspace diagnostics | All 15 events matched to typed native warnings |
| Diagnostic artifact | 121 unique facts, including 54 workspace-warning facts |
| Publication | Revalidated in isolated `cleanarchitecture-worker20` snapshot |
| Native MCP smoke | Discovery, definitions and reverse implementations pass |
| Source integrity | All 258 pinned tracked files unchanged |

The source remains `jasontaylordev/CleanArchitecture` commit
`5353a9edae000d576eade1a4f2c0d72d3b1c1785`, SDK10.0.401, Debug/net10.0, with the
same offline dependency bundle. Accepted artifact SHA-256:
`ec12ca74fd149a2926f1fa432cfdc1ea571671a4f9ccf67b908bb139c113dad4`.
Repeated warning events retain their counts but share content-addressed facts.

Three unscored native calls return 7,415, 3,217 and 4,584 bytes (15,216 total).
They share the artifact identity, report worker20, and return an implementation
occurrence also found through its definition. Its source hash matches the pinned
file. The temporary server is stopped; earlier holdout scores remain unchanged.

## Behavior and compatibility

The workspace binary logger preserves warning/error event types even when the
diagnostic API loses severity. Worker20 verifies successful builds and exact
event/diagnostic correspondence. Unmatched evidence, errors, failed builds and
damaged logs block acceptance. It uses the actual design-time load, without a
second evaluation or patched third-party assembly. See
[ADR0063](../../docs/adr/0063-native-build-diagnostic-severity.md).

SDK10.0.100/Roslyn5.0 and pinned Roslyn4.11 ignore the logger overload. They retain
strict diagnostic-free acceptance; workspace warnings still block them. This is
a separately identified fallback, not native warning verification. Both older
configurations are tested. Captures retain the reader identity and normalized
events; temporary raw binlogs are not retained for replay.

Import and publication revalidation reject absent or contradictory worker20
evidence. Publication also exposed stale compiler-index and MCP version lists;
these now use the shared semantic validator, with worker19/20 compatibility and
unknown-version rejection tests. A nested module boundary keeps archived partial
Go source copies out of root `go test ./...` traversal without altering archives.

## Validation

- Full Go suite and vet pass, including import/publication and serving controls.
- Existing worker integration passes on SDK10.0.100 and SDK10.0.401, including
  determinism, generators, source escapes, mutation and native/legacy warning rules.
- Pinned Roslyn4.11 builds and passes its net8/MassTransit runtime fixture:
  five routing configurations, nineteen exclusions, incomplete suppression.
- Three captures import: tuple aliases, ordinary warning, repeated warning.
- Nine captures cannot publish: error, identical warning/error text, promoted
  compiler warning, missing project, duplicate/missing/unmatched diagnostics,
  missing log, truncated log.
- Managed capture, isolated publication, source-integrity audit and native serving
  pass. No GPU or subagents were used.

The sealed manifest (archival evidence maintained separately) retains
commands, tests, normalized evidence, regression streams, admission controls,
publication and native responses. SDKs, dependencies, large artifacts and shards
remain in ignored `.local`. Earlier sealed archives are unchanged.

Next: prepare a fresh source-first packet and freeze this compiler-enabled build
with the corrected startup harness for independent evaluation. CleanArchitecture
is now development material. The paired CodeGraph and competitive gates stay open.
