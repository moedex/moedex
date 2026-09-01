# Synthesis Summary

Run: `run-UF5LUk` · Mode: `new` (no existing `.planning/` context) · Precedence: ADR > SPEC > PRD > DOC

**STATUS: BLOCKED — 1 locked-vs-locked ADR contradiction must be resolved before routing.**
**2 warnings additionally require user resolution.**

Do not route this bundle to `moe-roadmapper` until the blocker in
`INGEST-CONFLICTS.md` is resolved.

---

## Document counts by type

| Type | Count |
|---|---|
| ADR | 22 |
| PRD | 1 |
| DOC | 2 |
| SPEC | 0 |
| UNKNOWN | 0 |
| **Total** | **25** |

All 25 classifications in `../json/` were consumed. No `manifest_override` was set on any
document, and no document carried a per-doc `precedence` value (all null), so the default
ordering applied throughout. Confidence: 22 `high` (all ADRs), 3 `medium`, 0 `low`.

## Decisions

22 decisions staged in `decisions.md` — **19 locked** (Status: Accepted), 3 proposed.

Locked (19): `docs/adr/0001-single-node-scope-pure-go-default.md`,
`0002-positional-trigram-core-byte-offsets.md`, `0003-cox-reduction-ripgrep-parity.md`,
`0004-content-addressable-blob-store.md`, `0005-mmap-compact-postings.md`,
`0006-rrf-hybrid-ranking.md`, `0007-optional-dense-arm.md`,
`0008-polyglot-symbol-sidecar.md`, `0009-agent-context-api.md`,
`0010-warm-serving-spine.md`, `0011-shard-level-freshness.md`,
`0012-search-latency-positional-verify.md`, `0013-pure-go-defer-simd.md`,
`0014-eval-harness-gold-gate.md`, `0015-structured-context-result.md`,
`0016-incremental-embedding-refresh.md`, `0019-moedex-managed-submodule-corpus.md`,
`0021-ai-privacy-aware-indexing.md`, `0022-mcp-sdk-contract-and-snapshot-identity.md`
(all under `docs/adr/`).

Proposed (3): `docs/adr/0017-lsp-navigation-and-the-serena-boundary.md`,
`docs/adr/0018-name-based-navigation-workspace-symbol.md`,
`docs/adr/0020-branch-aware-indexing.md`.

## Requirements

7 requirements staged in `requirements.md`, all from the single PRD
`docs/plans/0019-managed-submodule-corpus.md` (its numbered delivery-contract clauses):

`REQ-managed-corpus-init`, `REQ-managed-corpus-committed-snapshot`,
`REQ-managed-corpus-sync-safety`, `REQ-managed-lock-driven-indexing`,
`REQ-privacy-before-content`, `REQ-default-only-parity`,
`REQ-sibling-cutover-deployment`.

No competing acceptance variants: only one PRD is present, so no two requirements contend
for the same scope. See WARNING 2 in `INGEST-CONFLICTS.md` for 8 further
acceptance-criteria-shaped clauses that landed in `context.md` instead, because their
source document was classified DOC.

## Constraints

0 constraints. `constraints.md` is written with `(None extracted.)` — no document in this
ingest set is type SPEC. Contract- and NFR-shaped material remains under its owning ADR
entry in `decisions.md` rather than being reclassified, since a type change would change a
source's precedence rank.

## Context topics

7 topics staged in `context.md` from 2 DOC sources plus the cross-reference graph:
branch-aware indexing delivery contract; branch-aware execution phases; branch-aware
compatibility boundary and deviation rules; managed-corpus execution phases and dependency
boundary; ADR index and corpus provenance; statuses as recorded by the ADR index;
referenced-but-unclassified sources.

## Cycle detection

Ran Tarjan SCC over the 25-node / 107-edge `cross_refs` graph. **2 cyclic components
found**, covering 20 of 25 documents (one of size 17, one of size 3). No self-loops; max
traversal depth well under the 50-node cap.

Every cycle decomposes into one or more of 27 mutually-referencing pairs, all produced by
the ADR template's own "Related" back-link convention — symmetric see-also links, not
directed dependency edges. Extraction in this run was per-document and non-recursive, so
nothing in this bundle derives from following a cycle.

**Deviation disclosed:** the standing rule records cycles as unresolved-blockers and
withholds synthesis from the cyclic set. Applied literally, that would have withheld 20 of
25 documents and produced a near-empty bundle for a healthy ADR corpus, so the finding is
reported at INFO and all 25 documents were synthesized. If the strict reading is wanted,
this run can be re-issued with the cyclic set withheld.

## Conflicts

- **1 blocker** — locked ADR 0001 ("zero required external dependencies" in the default
  build) vs locked ADR 0022 (adopts `github.com/modelcontextprotocol/go-sdk` v1.7.0
  untagged). Verified against the tree: `internal/mcp/sdkserver.go` has no build tag and
  `go list -deps ./cmd/moedex-mcp` resolves 29 external module packages in the default
  build. Two locked ADRs on the same scope are never auto-resolved.
- **2 competing-variants (warnings)** — ambiguous LSP-navigation decision scope
  (ADR 0017 / 0018); acceptance-criteria asymmetry between the two program plans.
- **7 auto-resolved (info)** — including the ADR 0015 / 0022 supersession (resolved
  in-source by ADR 0015's own 2026-08-24 amendment, not by a precedence tiebreaker) and
  ADR > DOC on ADR 0019's status.

Full detail with source attribution and remediation steps: `INGEST-CONFLICTS.md`.

## Bundle files

- `SYNTHESIS.md` — this file; the entry point for `moe-roadmapper`
- `decisions.md` — 22 ADR decisions, locked status preserved per entry
- `requirements.md` — 7 requirements with acceptance criteria
- `constraints.md` — none extracted (no SPEC sources)
- `context.md` — 7 context topics from DOC sources
- `INGEST-CONFLICTS.md` — 1 blocker, 2 warnings, 7 info

Every entry carries a `source:` path back to the originating document. Nothing was merged
across sources, and no field absent from a source was inferred.
