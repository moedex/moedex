## Conflict Detection Report

Mode: new. Classifications consumed: 25 (ADR 22, PRD 2, DOC 1). Existing context: none.
Bucket mapping: unresolved-blockers = BLOCKERS, competing-variants = WARNINGS, auto-resolved = INFO.

### BLOCKERS (0)

No unresolved blockers. Both locked-vs-locked pairs found in this ingest set are reconciled by
dated amendments inside the higher-numbered ADRs' counterparties, verified against source below;
neither was auto-resolved by a precedence tiebreak. No UNKNOWN or low-confidence classification is
present. No cross-reference cycle was treated as synthesis-blocking (see INFO).

### WARNINGS (2)

[WARNING] Program 0020's entire requirement set is gated on an unsatisfied Program 0019 dependency
  Found: docs/plans/0020-branch-aware-indexing.md declares "Depends on: Program 0019 Phase 2" and
    "Status: Proposed"; all eight of its Delivery contract clauses (REQ-0020-*) are staged.
  Found: docs/plans/0019-managed-submodule-corpus.md declares "Status: In progress (Phase 2
    automation complete; operator cutover approval pending)" and a Dependency boundary reading
    "ADR 0020 work is blocked until Phase 2 demonstrates that a managed corpus containing only
    locked default commits preserves existing search behavior."
  Found: the same document's deviation rules add "If managed default-only parity differs, branch
    acquisition remains blocked until the difference is fixed or explicitly adjudicated."
  Impact: The Phase 2 exit gate is "Automated gates pass; operator scope/cutover approval pending"
    — a human approval that the sources do not record as given. Routing the eight REQ-0020-*
    requirements into a schedule now would sequence work the source documents explicitly forbid
    starting, and dropping them would lose intent. Synthesis cannot pick.
  → Decide before routing: hold all REQ-0020-* behind an explicit Program 0019 Phase 2 approval
    gate, or record the operator cutover approval in docs/plans/0019-managed-submodule-corpus.md
    and re-run ingest.

[WARNING] ADR 0017 and ADR 0018 carry status "Proposed" while their own bodies describe shipped work
  Found: docs/adr/0017-lsp-navigation-and-the-serena-boundary.md — "Status: Proposed. All three
    conditions are met and the spike has been **productionized** in-process (`internal/navigate`,
    build tag `lsp`, `cmd/moedex-nav`), proven under `make test-lsp` (race-enabled)"; the same
    header closes with "This is still a `Proposed` decision: the engineering proof exists, but the
    strategic move (retiring the Serena seam in Protostar) is not taken here."
  Found: docs/adr/0018-name-based-navigation-workspace-symbol.md — "Status: Proposed (2026-07-01;
    implemented same day: `Symbol` type, `LSP`/`Pool` `WorkspaceSymbol`/`DocumentSymbol`, and the
    `find_symbol`/`symbols_overview` MCP tools in `internal/navigate` + `cmd/moedex-serve/nav_lsp.go`,
    all TDD'd against a real gopls)".
  Found: docs/adr/0022-mcp-sdk-contract-and-snapshot-identity.md (Accepted, locked) treats those
    tools as shipped — "This applies to all 19 tools, including `find_symbol` and
    `symbols_overview`, whose structured form is now `{\"status\":\"...\",\"symbols\":[...]}`."
  Impact: Status is the field a roadmapper uses to separate built work from planned work. Taken at
    face value these two decisions read as unbuilt, which would schedule navigation work that a
    locked ADR already describes as serving in production. Taken as shipped, a decision the author
    deliberately left open (retiring the Serena seam) would be treated as settled.
  → Split the two axes before routing: confirm whether "Proposed" here means "implementation
    shipped, strategic adoption undecided", and if so re-tag status per ADR (or add a dated
    amendment in the ADR bodies) so downstream planning does not re-schedule shipped code.

### INFO (8)

[INFO] Auto-resolved: ADR 0022 supersedes ADR 0001's zero-dependency clause for the MCP surface
  Note: Both ADRs are locked (Accepted), and their decision statements conflict on the scope
    "required external dependencies of the default build" — docs/adr/0001-single-node-scope-pure-go-default.md
    decides "a pure-Go standard-library default build with zero required external dependencies",
    while docs/adr/0022-mcp-sdk-contract-and-snapshot-identity.md decides "Use
    `github.com/modelcontextprotocol/go-sdk` v1.7.0 for MCP lifecycle and transport". This pair was
    raised as a LOCKED-vs-LOCKED blocker by an earlier run. It is resolved in-source, not by a
    precedence tiebreak: docs/adr/0001-single-node-scope-pure-go-default.md now carries a dated
    "2026-08-25 amendment" naming 0022 as superseding the "zero required external dependencies"
    clause for the MCP surface, verified in the ADR body and scoped by `go list -deps`
    (`cmd/moedex-mcp`, `cmd/moedex-serve`, `cmd/moedex-index` link 29 external module packages
    across 9 module roots; `cmd/moedex`, `cmd/moedex-corpus`, `cmd/moedex-parity` remain
    stdlib-only). Single-node scope, the ~8 GB envelope, the internal-first trust model, and the
    stdlib-only retrieval core stand unchanged, so no blocker remains.
  Note: Two locked ADRs restate the superseded clause in passing and are now narrower than they
    read — docs/adr/0007-optional-dense-arm.md ("the project's identity is a pure-Go,
    zero-required-dependency build") and docs/adr/0015-structured-context-result.md ("stdlib
    `encoding/json` only — keeps the pure-Go, zero-dep invariant"). Both restatements remain true
    for the arms they govern (dense arm, structured DTO); neither is an independent decision, so
    neither is treated as a further conflict.

[INFO] Auto-resolved: ADR 0022 supersedes the opt-in portion of ADR 0015
  Note: Both ADRs are locked (Accepted) and conflict on the scope "`search_context` structured
    output" — docs/adr/0015-structured-context-result.md decides "Add an **opt-in structured result
    mode** to `search_context`. Default behavior is unchanged (text)", while
    docs/adr/0022-mcp-sdk-contract-and-snapshot-identity.md decides "Every successful call returns
    both `structuredContent` and the existing text `content` fallback. The compatibility `format`
    argument changes only the fallback presentation; it never suppresses structured content."
    Resolved in-source: 0015 carries a dated "2026-08-24 amendment" recording that 0022 supersedes
    the opt-in portion, that `structuredContent` is now always present, and that blocks also carry
    the canonical indexed `blob_sha`. The 0015 record is not marked Superseded as a whole, so its
    non-superseded parts (per-arm score threading, deliberate deferral of `SymbolCoverage` /
    `PathCoverage` / per-arm ranks) remain live and are staged in decisions.md.

[INFO] Auto-resolved: ADR > DOC on the published status of ADR 0019
  Note: docs/adr/README.md (classified DOC, confidence medium) lists ADR 0019 as "Proposed
    ([program and phases](../plans/0019-managed-submodule-corpus.md))", while
    docs/adr/0019-moedex-managed-submodule-corpus.md declares "**Status:** Accepted" and is
    classified `locked: true`. Default precedence ADR > DOC applies with no per-doc override in
    play; the ADR wins and decisions.md stages ADR 0019 as locked (Accepted). Every other status in
    the index table matches its ADR. The stale index row is worth a one-line fix at source but
    gates nothing.

[INFO] Auto-resolved: locked ADR 0021 imposes a workspace privacy gate on the non-locked LSP arm
  Note: docs/adr/0021-ai-privacy-aware-indexing.md (locked, Accepted) states "The optional
    LSP-tagged live-navigation arm reads working trees through an external language server and is
    not made privacy-safe by index filtering alone. Default builds do not include that arm. Before
    an LSP-enabled deployment is used with governed corpora, it must conservatively reject
    workspaces containing level-1 paths or operate on a proven policy-sanitized workspace; this
    gate is carried into the branch serving plan rather than being implied by this ingestion
    change." The navigation arm it constrains is decided in ADR 0017 and ADR 0018, both non-locked
    (Proposed). LOCKED beats non-LOCKED, so the 0021 gate governs; it is not a contradiction of
    either nav ADR, which neither claims privacy-safety nor default-build inclusion.
    docs/plans/0020-branch-aware-indexing.md carries the gate forward by listing "non-default LSP
    navigation" among its non-goals.

[INFO] Ripgrep ground truth is scoped to privacy-eligible content
  Note: docs/adr/0003-cox-reduction-ripgrep-parity.md (locked) makes ripgrep "immovable ground
    truth" and rules that "Oracles, the seeded battery, and the corpus are never weakened to make a
    run pass", while docs/adr/0021-ai-privacy-aware-indexing.md (locked) requires that "Direct,
    parity, CAS, served-export, scale, and eval paths share the same ingest enforcement" and that
    level-1 content "never enters an index, mirror, CAS reference set, embedding input, or served
    shard". Not treated as a locked-vs-locked contradiction: the two are reconciled by
    docs/plans/0019-managed-submodule-corpus.md Delivery contract clause 6, which defines the
    comparison universe as "equivalent **privacy-eligible** exact-search results", and by ADR
    0021's own Evidence ("Parity freshness tests prove restricted repositories retain identity and
    become indexable after a permitted policy relaxation"). Recorded so downstream planning keeps
    the AC-D3 no-under-approximation invariant stated over the privacy-eligible universe rather
    than over raw filesystem contents.

[INFO] Cross-reference cycles detected (36) — reciprocal "Related" links, not synthesis-blocking
  Note: Three-colour DFS over the 25-node cross_refs graph (107 in-set edges, max traversal depth
    50, not exceeded) found 36 distinct cycles. Every one is a symmetric ADR "Related" back-link —
    e.g. 0004 → 0005 → 0004, 0006 → 0008 → 0006, 0009 → 0022 → 0009, 0019(adr) → plans/0019 →
    0019(adr) — and the largest spans eleven nodes across the retrieval/ranking/serving spine.
    These are the standard ADR relatedness idiom, not definitional dependencies: extraction in this
    run reads each document's own Decision section and never resolves content by following a
    cross-reference, so no synthesis loop is reachable and no cyclic document was withheld from
    extraction. Judgment call, flagged deliberately: a strict reading of the cycle rule would make
    all 36 blockers and withhold nearly the whole ingest set, which the graph shape does not
    justify. Recorded at INFO rather than suppressed.

[INFO] Managed-lock schema evolves across the two PRDs rather than competing
  Note: docs/plans/0019-managed-submodule-corpus.md Delivery contract clause 2 requires that
    "`.gitmodules`, gitlinks, configuration, and an atomic lock describe one committed snapshot",
    while docs/plans/0020-branch-aware-indexing.md clause 2 requires that "the managed lock records
    a deterministic and complete remote-head snapshot" — non-identical acceptance criteria over the
    same artifact. Not staged as competing variants: 0020 names the artifact "managed lock v2
    remote heads" in its Phase 3 deliverable and declares "Depends on: Program 0019 Phase 2", and
    docs/adr/0019-moedex-managed-submodule-corpus.md anticipates the bump directly ("one gitlink
    records one commit, while a branch-aware corpus contains multiple branch tips. The corpus
    therefore needs both a superproject and a Moedex-owned lock"). Both clauses are preserved
    verbatim in requirements.md as REQ-0019-committed-snapshot and REQ-0020-lock-remote-heads; the
    ordering between them is a version sequence, not a choice. The related privacy clauses
    (REQ-0019-privacy-level1-exclusion, REQ-0020-privacy-bootstrap-locked-tree) extend the same
    rule from working trees to locked trees and likewise do not compete.

[INFO] Operator type assignment applied to both docs/plans documents
  Note: docs/plans/0019-managed-submodule-corpus.md and docs/plans/0020-branch-aware-indexing.md
    both carry `manifest_override: true` with type PRD assigned by the operator rather than
    inferred; their classification records state the override explicitly ("TYPE OVERRIDE: PRD").
    The earlier type-asymmetry warning between these two structurally identical program plans is
    settled and is not re-raised. Both Delivery contracts were extracted in full — seven clauses
    from Program 0019, eight from Program 0020, fifteen requirements total — and extracting both
    sets surfaced no contradiction between them beyond the sequencing gate recorded above under
    WARNINGS and the lock-schema evolution recorded in this section.
