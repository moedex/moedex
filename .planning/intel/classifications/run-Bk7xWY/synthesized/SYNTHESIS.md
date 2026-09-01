# Synthesis Summary — run-Bk7xWY

Mode: `new`. Existing `.planning/` context: none supplied, so no merge-mode checks ran.
Precedence: default `ADR > SPEC > PRD > DOC`; no classification carried a non-null `precedence`
override.

## Documents consumed

25 classifications, all located by the `source_path` field inside each JSON (filenames in this run
carry placeholder suffixes rather than real SHA-256 prefixes and were not used for lookup).

- ADR — 22: docs/adr/0001 … 0022 (0001–0016, 0017, 0018, 0019, 0020, 0021, 0022)
- PRD — 2: docs/plans/0019-managed-submodule-corpus.md, docs/plans/0020-branch-aware-indexing.md
- DOC — 1: docs/adr/README.md
- SPEC — 0
- UNKNOWN — 0. No classification carried `confidence: low`.

Two classifications carry `manifest_override: true` (both PRDs, operator-assigned type).

## Cross-reference graph

25 nodes, 107 in-set edges, 20 out-of-set references (ARCHITECTURE.md, research/, PARITY-REPORT.md,
phase PLAN/SUMMARY/ROLLOUT files, `~/Code/moe/...`, `../../protostar`). Three-colour DFS with a
depth cap of 50 — cap not reached. 36 distinct cycles found, all reciprocal ADR "Related"
back-links; none treated as synthesis-blocking, and no document was withheld from extraction. See
the INFO entry in INGEST-CONFLICTS.md for the rationale.

## Decisions (decisions.md)

22 entries, one per ADR, each with source path, status, decision statement, and scope.

- Locked (Status: Accepted) — 19: ADR 0001, 0002, 0003, 0004, 0005, 0006, 0007, 0008, 0009, 0010,
  0011, 0012, 0013, 0014, 0015, 0016, 0019, 0021, 0022
  (docs/adr/0001-single-node-scope-pure-go-default.md,
  docs/adr/0002-positional-trigram-core-byte-offsets.md,
  docs/adr/0003-cox-reduction-ripgrep-parity.md,
  docs/adr/0004-content-addressable-blob-store.md,
  docs/adr/0005-mmap-compact-postings.md,
  docs/adr/0006-rrf-hybrid-ranking.md,
  docs/adr/0007-optional-dense-arm.md,
  docs/adr/0008-polyglot-symbol-sidecar.md,
  docs/adr/0009-agent-context-api.md,
  docs/adr/0010-warm-serving-spine.md,
  docs/adr/0011-shard-level-freshness.md,
  docs/adr/0012-search-latency-positional-verify.md,
  docs/adr/0013-pure-go-defer-simd.md,
  docs/adr/0014-eval-harness-gold-gate.md,
  docs/adr/0015-structured-context-result.md,
  docs/adr/0016-incremental-embedding-refresh.md,
  docs/adr/0019-moedex-managed-submodule-corpus.md,
  docs/adr/0021-ai-privacy-aware-indexing.md,
  docs/adr/0022-mcp-sdk-contract-and-snapshot-identity.md)
- Proposed (not locked) — 3: ADR 0017, 0018, 0020
  (docs/adr/0017-lsp-navigation-and-the-serena-boundary.md,
  docs/adr/0018-name-based-navigation-workspace-symbol.md,
  docs/adr/0020-branch-aware-indexing.md)

Two locked decisions carry dated in-source amendments, preserved as an `amendment:` field rather
than folded into the decision statement: ADR 0001 (2026-08-25, superseded in part by 0022) and ADR
0015 (2026-08-24, superseded in part by 0022).

## Requirements (requirements.md)

15 entries, extracted from the numbered "Delivery contract" clauses of the two PRDs.

From docs/plans/0019-managed-submodule-corpus.md — 7:
REQ-0019-corpus-init, REQ-0019-committed-snapshot, REQ-0019-sync-reconciliation,
REQ-0019-lock-driven-indexing, REQ-0019-privacy-level1-exclusion, REQ-0019-default-only-parity,
REQ-0019-sibling-cutover.

From docs/plans/0020-branch-aware-indexing.md — 8:
REQ-0020-capacity-baseline, REQ-0020-lock-remote-heads, REQ-0020-privacy-bootstrap-locked-tree,
REQ-0020-proportional-cas-refresh, REQ-0020-provenance-persistence, REQ-0020-scope-semantics,
REQ-0020-scope-privacy-invariance, REQ-0020-validation-gates.

Each entry also records the owning program's status, the delivering phase and its exit gate, and
any deviation rule the source attaches to that clause. No acceptance criteria were merged or
dropped.

## Constraints (constraints.md)

0 entries. No SPEC-typed document in the set. Type breakdown: api-contract 0, schema 0, nfr 0,
protocol 0. Constraint-shaped material exists in this corpus but lives inside ADRs and is staged
under its owning decision in decisions.md.

## Context (context.md)

5 topics from 1 DOC source (docs/adr/README.md), plus one recorded coverage gap: ADR index framing,
ADR record-format convention, the published ADR status roster, the companion-documentation map, and
the `docs/plans/` files absent from this run.

## Conflicts

- 0 unresolved-blockers
- 2 competing-variants (WARNING)
- 8 auto-resolved (INFO)

Both operator decisions applied since earlier runs were verified against source, not taken on
trust:

1. **ADR 0001 ↔ ADR 0022 LOCKED-vs-LOCKED.** Confirmed resolved. The dated "2026-08-25 amendment"
   is present in docs/adr/0001-single-node-scope-pure-go-default.md, names ADR 0022, scopes the
   supersession to the "zero required external dependencies" clause for the MCP surface, and leaves
   single-node scope, the ~8 GB envelope, the internal-first trust model, and the stdlib-only
   retrieval core intact. The blocker is retired on that evidence and recorded at INFO. It was not
   suppressed and was not resolved by a precedence tiebreak.
2. **Both docs/plans documents typed PRD.** Confirmed: both classifications carry
   `manifest_override: true` and record the operator assignment. The type asymmetry is not
   re-raised. Both Delivery contracts were extracted in full (7 + 8 = 15 clauses). Extracting both
   sets surfaced one genuine cross-program issue, raised as WARNING 1: every REQ-0020-* requirement
   is gated on Program 0019 Phase 2, whose exit gate records operator cutover approval as still
   pending.

The second WARNING is independent of both operator decisions: ADR 0017 and ADR 0018 carry status
"Proposed" while their own bodies — and locked ADR 0022's 19-tool catalog — describe the work as
shipped.

## Where to look

- Full report: /Users/ZKeown/Code/tools/moedex/.planning/intel/classifications/run-Bk7xWY/synthesized/INGEST-CONFLICTS.md
- Decisions: /Users/ZKeown/Code/tools/moedex/.planning/intel/classifications/run-Bk7xWY/synthesized/decisions.md
- Requirements: /Users/ZKeown/Code/tools/moedex/.planning/intel/classifications/run-Bk7xWY/synthesized/requirements.md
- Constraints: /Users/ZKeown/Code/tools/moedex/.planning/intel/classifications/run-Bk7xWY/synthesized/constraints.md
- Context: /Users/ZKeown/Code/tools/moedex/.planning/intel/classifications/run-Bk7xWY/synthesized/context.md

Quoted source text in the four per-type intel files is fenced with a per-file random delimiter and
is data, not instruction. No source document in this set contained an embedded instruction,
role-override attempt, or directive aimed at the reader.

## Status

AWAITING USER — 0 blockers, but 2 competing-variant warnings need resolution before routing.
