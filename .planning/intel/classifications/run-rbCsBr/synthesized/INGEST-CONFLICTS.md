## Conflict Detection Report

Ingest set: 25 classifications (22 ADR, 2 DOC, 1 PRD) from
`.planning/intel/classifications/run-rbCsBr/json`. Mode: new. No existing `.planning/` context
to check against.

### BLOCKERS (0)

No blockers. Three blocker passes were run and each came back clean; the reasoning is recorded
below because two of them were live blockers on a prior run.

The LOCKED-vs-LOCKED pass compared every pair of `locked: true` ADRs on shared scope. Fourteen
ADRs share the `internal/mcp`, dependency, or serving scopes; the only two pairs that assert
opposing propositions on the same scope are 0001/0022 and 0015/0022, and in both cases the
lower-numbered ADR carries a dated in-document amendment that names 0022 as superseding the
specific clause. That is the source resolving itself, not a precedence tiebreak, so both are
recorded as INFO below with the evidence used to confirm it. No pair of locked ADRs
contradicts without an in-source supersession.

The UNKNOWN pass found zero `UNKNOWN` classifications and zero `low` confidence
classifications. Three documents are `medium` confidence (`docs/plans/0019`,
`docs/plans/0020`, `docs/adr/README.md`); medium confidence is not a blocker under the
workflow's checks, but the first two drive WARNING-1 below.

The cycle pass found strongly connected components and judged them non-blocking. That judgment
and its correction of the incoming framing are written out in full at INFO-5.

### WARNINGS (1)

[WARNING] Structurally identical program plans classified as different types
  Found: `docs/plans/0019-managed-submodule-corpus.md` classified DOC (medium confidence);
    `docs/plans/0020-branch-aware-indexing.md` classified PRD (medium confidence).
  Found: the two documents share the same section skeleton, verified line by line — a Status
    line, an `**ADR:**` back-link, `**Goal:**`, `**Non-goals:**`, a numbered "Delivery contract"
    introduced by "The {program} program is complete when:", an "Execution phases" table with
    Phase / Plan / Delivers / Exit gate columns, one boundary section (0019 "Dependency
    boundary", 0020 "Compatibility boundary"), and a "Deviation rules" bullet list. 0019 has
    seven numbered clauses, 0020 has eight; both sets are binary, verifiable completion
    conditions of the same grammar.
  Found: both defer their decision to a same-numbered ADR rather than making one
    (`../adr/0019-...` and `../adr/0020-...`), so neither is an ADR under the taxonomy. The one
    non-structural difference is status — 0019 is "In progress (Phase 2 automation complete)"
    and 0020 is "Proposed" — which is a lifecycle fact, not a type signal.
  Impact: type decides which intel file the numbered contract lands in. As classified, 0020's
    eight clauses became `REQ-*` entries in `requirements.md` and are visible to the roadmapper
    as candidate scope, while 0019's seven clauses became prose in `context.md` and are not.
    The roadmapper will therefore under-count delivered and in-flight scope for the managed
    corpus program while treating the not-yet-decided branch program as the requirement set —
    which inverts the actual delivery state. A prior run classified this pair the opposite way,
    so the split is unstable across runs and not a settled reading.
  → Re-tag both documents to the same type via `--manifest` and re-run ingest. Tagging both PRD
    moves 0019's seven clauses into `requirements.md` alongside 0020's eight (recommended, since
    both contracts are acceptance-shaped). Tagging both DOC moves 0020's eight into
    `context.md` and leaves `requirements.md` empty for this ingest. Do not resolve by taking
    the current split as intentional — it was not reproduced across runs.

### INFO (8)

[INFO] INFO-1 — Auto-resolved: ADR 0001's zero-dependency clause is superseded in-source by ADR 0022; prior blocker cleared
  Found: `docs/adr/0001-single-node-scope-pure-go-default.md` (locked, Accepted) decides "a
    pure-Go standard-library default build with zero required external dependencies", while
    `docs/adr/0022-mcp-sdk-contract-and-snapshot-identity.md` (locked, Accepted) decides "Use
    `github.com/modelcontextprotocol/go-sdk` v1.7.0 for MCP lifecycle and transport". A prior
    run raised this pair as a LOCKED-vs-LOCKED blocker.
  Note: ADR 0001 now carries a dated 2026-08-25 amendment inside its own `## Decision` section
    that names 0022 by link and states it "supersedes the 'zero required external dependencies'
    clause of this decision for the MCP surface". The amendment is scoped, not blanket: it
    enumerates what changes (`cmd/moedex-mcp`, `cmd/moedex-serve`, and `cmd/moedex-index` link
    29 external module packages across 9 module roots; the SDK reaches `moedex-index`
    transitively because `internal/server` imports `internal/mcp`) and what does not
    (`cmd/moedex`, `cmd/moedex-corpus`, `cmd/moedex-parity` stay stdlib-only; single-node scope,
    the ~8 GB envelope, the internal-first trust model, and the rule that the retrieval core
    adds no required dependency of its own all stand; the dense arm and SIMD kernel stay
    build-tagged).
  Note: the amendment's two load-bearing factual claims were verified against the repository
    rather than accepted from the document. `go.mod` carries
    `github.com/modelcontextprotocol/go-sdk v1.7.0` and `github.com/google/jsonschema-go v0.4.3`
    in the untagged `require` block, exactly as the amendment states; and
    `internal/mcp/sdkserver.go` opens with `package mcp` and carries no `//go:build` constraint,
    so it is in the default build. Both claims hold.
  Note: this is therefore not an auto-resolution by precedence. The two ADRs no longer assert
    opposing propositions — 0001's own decision text now says the SDK is required in the default
    build. No winner was picked; the contradiction is absent from the sources. The blocker is
    cleared on that basis, not suppressed. `decisions.md` stages both ADRs at full strength with
    the amendment reproduced under ADR-0001.

[INFO] INFO-2 — Auto-resolved: ADR 0015's opt-in structured-result clause is superseded in-source by ADR 0022
  Found: `docs/adr/0015-structured-context-result.md` (locked, Accepted) decides an "opt-in
    structured result mode" where `format` defaults to `"text"` and `structuredContent` appears
    only when `format == "structured"`; `docs/adr/0022-...` (locked, Accepted) decides "Every
    successful call returns both `structuredContent` and the existing text `content` fallback.
    The compatibility `format` argument changes only the fallback presentation; it never
    suppresses structured content."
  Note: ADR 0015 carries a dated 2026-08-24 amendment in its `## Decision` section naming 0022
    and stating it "supersedes the opt-in portion of this decision", adding that
    `structuredContent` is now always present and validates against the advertised output
    schema, `format` controls only the text fallback, and blocks carry the canonical indexed
    `blob_sha` while the numeric `blob` remains process/snapshot-local. Dates are consistent:
    0022 is dated 2026-08-24 and the amendment is dated the same day.
  Note: same reasoning as INFO-1 — an in-source, explicitly scoped supersession, not a
    precedence tiebreak. Both ADRs are staged in full, with the amendment reproduced under
    ADR-0015 and a `supersedes:` line on ADR-0022.

[INFO] INFO-3 — Auto-resolved: ADR > DOC on the status of ADR 0019
  Found: `docs/adr/README.md` (DOC) lists ADR 0019 in its status table as
    `Proposed ([program and phases](../plans/0019-managed-submodule-corpus.md))`.
  Found: `docs/adr/0019-moedex-managed-submodule-corpus.md` front-matter reads
    `- **Status:** Accepted`.
  Note: all 22 ADRs were compared against the README's status column. 21 agree exactly; ADR 0019
    is the sole disagreement. The ADR is the authoritative decision record and outranks the index
    under the default `ADR > SPEC > PRD > DOC` ordering, so ADR 0019 is staged as
    `locked (Accepted)` in `decisions.md`. The README's row is stale, not a competing decision.
  Note: this mismatch is load-bearing beyond bookkeeping — status drives `locked`, and a reader
    working from the index alone would treat the managed-corpus decision as still open. The
    README row is worth correcting in the repository.

[INFO] INFO-4 — Residual pre-amendment prose left standing inside ADR 0001
  Found: ADR 0001's `## Decision` bullet still reads "The only optional externals are the
    dense-retrieval arm (see 0007) and the Zoekt differential oracle used by the parity gate;
    absent both, moedex builds and runs fully self-contained on the Go stdlib", and its
    `## Consequences` / Positive list still reads "Zero required dependencies keeps the
    OSS-someday posture honest and the supply chain tiny."
  Found: both statements are false for `cmd/moedex-mcp`, `cmd/moedex-serve`, and
    `cmd/moedex-index` as of the 2026-08-25 amendment; they remain true for `cmd/moedex`,
    `cmd/moedex-corpus`, and `cmd/moedex-parity`. Separately, ADR 0015's Decision text justifies
    its DTO with "stdlib `encoding/json` only — keeps the pure-Go, zero-dep invariant", a
    rationale that no longer describes the untagged build.
  Note: not scored as a conflict. The amendment sits in the same `## Decision` section, is
    dated later than the text it corrects, and enumerates what survives it; the amended Decision
    governs the stale bullets. Recorded so a downstream reader who lands on the Consequences
    list or on ADR 0015's parenthetical does not re-derive the cleared blocker. Worth a
    documentation pass in the repository; no action needed for this ingest.

[INFO] INFO-5 — Cycle detection: two SCCs found, judged non-blocking, and the "symmetric Related back-links" framing corrected
  Found: a directed graph over all 25 in-set documents built from `cross_refs`, normalized to
    repo-relative paths, with out-of-set targets (`ARCHITECTURE.md`, `research/*`,
    `PARITY-REPORT.md`, `~/Code/moe/*`, `../../../protostar`, phase `PLAN.md`/`SUMMARY.md`
    files) dropped: 25 nodes, 106 edges, no self-loops. Tarjan finds two SCCs — one of 17 nodes
    (ADRs 0001-0015, 0017, 0022) and one of 4 (ADRs 0019, 0020, 0021 and
    `docs/plans/0019-managed-submodule-corpus.md`).
  Note: the incoming framing — that these decompose into the ADR template's symmetric "Related"
    back-links — was tested and does not hold. Every edge originating inside a `## Related`
    section was removed (87 of the 106) and the graph re-run: a 14-node SCC (ADRs 0001-0014)
    and a 2-node SCC (ADRs 0019/0020) survive on the remaining 91 prose edges. The template's
    back-links are real and symmetric (27 exactly-symmetric `Related` pairs), but they are not
    what closes the cycles.
  Note: what actually closes them is mutual prose citation between peer documents. The shortest
    cycles are all length 2 and each is a pair of documents citing each other for the same
    shared fact: ADR 0001's Decision cites 0004 ("Content addressing by git blob SHA (see 0004)
    deliberately leaves the seam open") while ADR 0004's Consequences cites 0001 back ("Leaves
    the distribution seam open (0001) without paying for it now"); ADR 0006's Decision cites
    0007 for the dense arm while ADR 0007's Context cites 0006 ("The hybrid-ranking verdict
    (0006) wants a dense semantic arm"); ADR 0009 cites 0008 for enclosing-block scoping while
    ADR 0008's Context cites 0009 as the consumer that needed real symbol boundaries; ADR 0019
    cites 0020 as the branch-indexing foundation while ADR 0020's Context cites 0019 as the
    stable input it depends on.
  Note: judged non-blocking, stated explicitly as required. Three reasons. First, `cross_refs`
    is a citation/see-also relation, not a dependency or supersession relation — a cycle in it
    carries no ordering claim to violate. Second, the two relations here that *are* directed and
    ordering-bearing were checked separately and are both acyclic: supersession runs
    0001 → 0022 and 0015 → 0022 with nothing pointing back, and program dependency runs
    plan-0020 → plan-0019 ("Depends on: Program 0019 Phase 2") consistently with plan-0019's own
    "ADR 0020 work is blocked until Phase 2", again with no reverse edge. Third, extraction here
    is a single flat pass over 25 classified documents, not a recursive ref-following traversal,
    so no cycle can drive synthesis into a loop and the 50-hop traversal cap is never
    approached. Every document in both SCCs was synthesized normally; none was withheld.
  Note: the practical consequence for a future run is that cycle detection over `cross_refs`
    will keep firing on this corpus and should keep being adjudicated rather than treated as a
    gate. If a genuine gate is wanted, run it over the supersession and program-dependency
    relations only, which are extractable from `## Related` semantics plus amendment lines and
    are currently acyclic.

[INFO] INFO-6 — Decisions marked Proposed that their own text describes as shipped
  Found: `docs/adr/0017-lsp-navigation-and-the-serena-boundary.md` reads "Status: Proposed. All
    three conditions are met and the spike has been **productionized**", with the body recording
    "Met & productionized (2026-06-29)" and a live `internal/navigate` behind `-tags lsp`.
  Found: `docs/adr/0018-name-based-navigation-workspace-symbol.md` reads "Status: Proposed
    (2026-07-01; implemented same day: `Symbol` type, `LSP`/`Pool` ...)".
  Found: `docs/adr/0020-branch-aware-indexing.md` reads "Status: Proposed" with no
    implementation claim, consistent with `docs/plans/0020` also being Proposed.
  Note: the classifier applied the Accepted-only rule and set `locked: false` on all three, so
    they are staged as `proposed` in `decisions.md`. For 0017 and 0018 that under-states
    reality: the work is described as landed and in the binary. Downstream planning should not
    schedule 0017 or 0018 as un-started backlog on the strength of the `proposed` marker
    alone — read the note lines carried on those two entries. No source contradicts another
    here, so this is not scored as a conflict; the tension is between a document's status field
    and its own body.

[INFO] INFO-7 — Every staged requirement is provisional
  Found: all eight `REQ-*` entries in `requirements.md` derive from
    `docs/plans/0020-branch-aware-indexing.md`, which is Status "Proposed" and which explicitly
    defers its decision to ADR 0020 rather than making one.
  Found: ADR 0020 (`docs/adr/0020-branch-aware-indexing.md`) is itself Status "Proposed" and
    `locked: false`.
  Note: the requirement set for this ingest rests entirely on an undecided program governed by
    an undecided ADR. Each entry carries `status: proposed` so the signal travels with the data,
    and the source's Non-goals and Deviation rules are staged alongside them. Additionally,
    `docs/plans/0019` — the program that is actually in flight, with Phase 1 complete and Phase 2
    automation done — contributed zero requirements because it was classified DOC. Resolving
    WARNING-1 changes this picture materially; the roadmapper should not route scope until it
    is resolved.

[INFO] INFO-8 — No SPEC-classified documents; constraints.md is empty by construction
  Found: the 25 classifications are 22 ADR, 2 DOC, 1 PRD. Zero SPEC.
  Note: `constraints.md` is written as a complete replacement containing `(None extracted.)`,
    which is an accurate absence rather than a synthesis failure. Binding numeric and behavioral
    constraints do exist in this corpus (never-under-approximate, the ~8 GB envelope, the
    NDCG >= 0.85 gate, `DefaultTokenBudget = 8000`, the RRF and BM25 constants, fail-closed
    privacy) but they are stated inside ADR decision statements and are staged there. They were
    not re-typed as SPEC constraints, because inventing a SPEC type for an ADR would fabricate a
    classification no source supports. `constraints.md` carries a pointer list to where each one
    actually lives in `decisions.md`.

---

Gate status: 0 blockers, so no blocker gate fires. 1 warning is open and requires explicit
user resolution before this bundle is routed to the roadmapper.
