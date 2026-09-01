# Synthesis Summary — run-rbCsBr

Entry point for downstream consumers. Mode: `new`. No existing `.planning/` context was
supplied, so no merge-mode checks against ROADMAP/PROJECT/REQUIREMENTS/CONTEXT were run.

- Classifications consumed: `.planning/intel/classifications/run-rbCsBr/json` (25 of 25)
- Staged intel: `.planning/intel/classifications/run-rbCsBr/synthesized/`
- Conflicts report: `.planning/intel/classifications/run-rbCsBr/synthesized/INGEST-CONFLICTS.md`

## Doc counts by type

| Type | Count |
|---|---|
| ADR | 22 |
| DOC | 2 |
| PRD | 1 |
| SPEC | 0 |
| UNKNOWN | 0 |

Confidence: 22 high, 3 medium, 0 low. No document required type-tagging as a blocker.

## Decisions

22 staged in `decisions.md`. **19 locked** (source `Status: Accepted`), 3 proposed.

Locked: `docs/adr/0001-single-node-scope-pure-go-default.md`,
`0002-positional-trigram-core-byte-offsets.md`, `0003-cox-reduction-ripgrep-parity.md`,
`0004-content-addressable-blob-store.md`, `0005-mmap-compact-postings.md`,
`0006-rrf-hybrid-ranking.md`, `0007-optional-dense-arm.md`,
`0008-polyglot-symbol-sidecar.md`, `0009-agent-context-api.md`,
`0010-warm-serving-spine.md`, `0011-shard-level-freshness.md`,
`0012-search-latency-positional-verify.md`, `0013-pure-go-defer-simd.md`,
`0014-eval-harness-gold-gate.md`, `0015-structured-context-result.md`,
`0016-incremental-embedding-refresh.md`, `0019-moedex-managed-submodule-corpus.md`,
`0021-ai-privacy-aware-indexing.md`, `0022-mcp-sdk-contract-and-snapshot-identity.md`.

Proposed (not locked): `0017-lsp-navigation-and-the-serena-boundary.md`,
`0018-name-based-navigation-workspace-symbol.md`, `0020-branch-aware-indexing.md`.
0017 and 0018 describe their own work as productionized/implemented despite the Proposed
status — see INFO-6 before scheduling either as backlog.

Two locked ADRs carry dated in-document amendments naming ADR 0022 as superseding a specific
clause: ADR 0001 (2026-08-25, the zero-required-dependency clause, for the MCP surface only)
and ADR 0015 (2026-08-24, the opt-in structured-result clause). Both amendments are reproduced
verbatim in substance in `decisions.md`; neither ADR was dropped or overwritten.

## Requirements

8 staged in `requirements.md`, all from the single PRD-classified document
`docs/plans/0020-branch-aware-indexing.md`:

`REQ-branch-capacity-baseline`, `REQ-branch-lock-remote-heads`,
`REQ-branch-privacy-first-ingestion`, `REQ-branch-proportional-cas-refresh`,
`REQ-branch-provenance-persistence`, `REQ-branch-scoped-query-semantics`,
`REQ-branch-scope-cannot-widen-privacy`, `REQ-branch-release-gates`.

All eight are `status: proposed` — the source plan is Proposed and defers to ADR 0020, which
is also Proposed. **Do not route these as committed scope** (INFO-7). No competing acceptance
variants exist, because only one document in this set is classified PRD.

## Constraints

0 staged. No document was classified SPEC, so `constraints.md` reads `(None extracted.)` by
construction, not by omission. Real binding constraints live inside ADR decision statements;
`constraints.md` carries a pointer list to each one's location in `decisions.md` (INFO-8).

## Context topics

3 in `context.md`: the Program 0019 managed-submodule-corpus plan and its delivery state; the
ADR index and decision-status register (`docs/adr/README.md`); and the derived cross-reference
graph shape.

## Conflicts

**0 blockers, 1 competing/ambiguous warning, 8 auto-resolved or informational.**

The one open warning is a type asymmetry: `docs/plans/0019` (DOC) and `docs/plans/0020` (PRD)
carry structurally identical numbered "Delivery contract" sections but were classified
differently, and a prior run classified them the opposite way. Type decides whether those
clauses become `REQ-*` entries or prose, so as staged the roadmapper sees the not-yet-decided
branch program as the requirement set while the actually-in-flight managed-corpus program
contributes none. Resolve via `--manifest` and re-run before routing.

Two prior-run blockers were re-derived from source rather than taken on trust and are now
cleared, with the reasoning and the repository evidence written out at INFO-1 and INFO-2. The
ADR 0001 / ADR 0022 dependency contradiction is resolved by ADR 0001's own dated, explicitly
scoped amendment; the amendment's factual claims were checked against `go.mod` (SDK v1.7.0 and
`jsonschema-go` in the untagged `require` block) and `internal/mcp/sdkserver.go` (no
`//go:build` constraint), and both hold.

Cycle detection found two SCCs (17 nodes and 4 nodes) and they were judged **non-blocking**,
stated explicitly at INFO-5. The incoming framing that they reduce to the ADR template's
symmetric "Related" back-links was tested and does not hold — removing all 87 `Related`-section
edges leaves a 14-node and a 2-node SCC standing on prose citations. The correct reading is
that `cross_refs` is a citation relation carrying no ordering claim; the two relations that do
carry ordering (supersession, program dependency) were checked separately and are both acyclic.

## Where to read next

| File | Contents |
|---|---|
| `INGEST-CONFLICTS.md` | Full three-bucket report with per-entry evidence |
| `decisions.md` | 22 ADR entries, amendments preserved |
| `requirements.md` | 8 provisional `REQ-*` entries plus source non-goals and deviation rules |
| `constraints.md` | Empty by construction; pointer list to constraints held in `decisions.md` |
| `context.md` | 3 topics, source-attributed |
