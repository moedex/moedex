# Staged Context

Two DOC-classified documents. Content is source-attributed and reproduced in substance;
nothing here is a decision or a requirement.

---

## Topic: Managed submodule corpus — program and delivery state (Program 0019)
- source: docs/plans/0019-managed-submodule-corpus.md
- type: program/execution plan (classified DOC, medium confidence)
- status recorded by the source: In progress (Phase 2 automation complete; operator cutover approval pending)
- governing decision: ADR 0019 (docs/adr/0019-moedex-managed-submodule-corpus.md), Accepted

**Goal (verbatim in substance):** A fresh authenticated operator can ask Moedex to create,
snapshot, synchronize, validate, and index its own local corpus without adopting an existing
directory.

**Non-goals:** non-default branches, tags/MR refs, in-place adoption, a shared superproject
remote, and implicit pruning.

**Delivery contract — the managed-corpus program is complete when:**

1. `moedex-corpus init` creates a marked local superproject and curated submodules in a fresh root;
2. `.gitmodules`, gitlinks, configuration, and an atomic lock describe one committed snapshot;
3. `sync` safely handles add, update, rename, missing, explicit prune, partial fetch failure, and force-pushed default branches;
4. `moedex-index` consumes the managed lock without indexing the superproject or omitting submodules whose `.git` is a file;
5. every managed and conventional indexing path applies `.ai-privacy.yml` before content and excludes effective level-1 files;
6. default-only managed and conventional corpora produce equivalent privacy-eligible exact-search results; and
7. deployment builds a sibling corpus/index and switches only after automated and human checks.

**Execution phases**

- Phase 1 — Managed corpus foundation (`./phases/01-managed-corpus-foundation/PLAN.md`, with SUMMARY): versioned marker/lock, stable project identity, hermetic fixtures, `init`, failure-safe reconciliation. Exit gate: **Complete**.
- Phase 2 — Managed corpus integration and rollout (`./phases/02-managed-corpus-integration/PLAN.md`, with SUMMARY and ROLLOUT): CLI/doctor, lock-driven privacy-aware indexing, default-only parity, deployment integration, sibling cutover. Exit gate: **automated gates pass; operator scope/cutover approval pending**.

**Dependency boundary:** ADR 0020 work is blocked until Phase 2 demonstrates that a managed
corpus containing only locked default commits preserves existing search behavior. This
isolates submodule/discovery regressions from later branch/provenance changes.

**Deviation rules:**

- If partial submodule failure cannot carry forward the prior project snapshot, stop and revise ADR 0019 rather than weakening the safety rule.
- If managed default-only parity differs, branch acquisition remains blocked until the difference is fixed or explicitly adjudicated.
- If any effective level-1 content reaches an index, CAS reference, shard, or sidecar input, stop rollout and preserve the prior live snapshot.
- If an existing root contains any user-owned or unmanaged files, leave it untouched and use a sibling root.

**Synthesis note:** this document's numbered "Delivery contract" is structurally identical to
the one in `docs/plans/0020-branch-aware-indexing.md`, which was classified PRD. Because this
one was classified DOC, its seven clauses are staged here as context rather than as
`REQ-*` entries in `requirements.md`. That asymmetry is unresolved — see INGEST-CONFLICTS.md
WARNING-1. If a user re-tags this document PRD, these seven clauses should move to
`requirements.md`.

---

## Topic: ADR index and decision-status register
- source: docs/adr/README.md
- type: index / table of contents (classified DOC, medium confidence)
- role: enumerates ADRs 0001-0022 with decision titles and a status column, and links the companion architecture, plans, research, and parity docs.

**What it provides:** a single register mapping each ADR number to a one-line decision title
and a status. It carries no `Context`/`Decision`/`Consequences` sections and no status of its
own; the individual ADRs it links carry the locked decisions.

**Outbound links beyond the ADR set:** `../../ARCHITECTURE.md`, `../plans/`, `../../research/`,
`../../PARITY-REPORT.md`, plus `../plans/0019-managed-submodule-corpus.md` and
`../plans/0020-branch-aware-indexing.md`.

**Status column vs the ADRs' own front-matter** — checked entry by entry across all 22 ADRs.
21 of 22 agree. One disagrees:

- ADR 0019: README index says `Proposed ([program and phases](../plans/0019-managed-submodule-corpus.md))`; `docs/adr/0019-moedex-managed-submodule-corpus.md` says `Status: Accepted`.

The ADR is the authoritative record and outranks the index under `ADR > DOC` precedence, so
`decisions.md` stages ADR 0019 as `locked (Accepted)`. See INGEST-CONFLICTS.md INFO-3.

**Status annotations the README carries that the ADR front-matter compresses:** the README
expands ADR 0017's status to note all three conditions met and productionized in-process
(`internal/navigate`, `-tags lsp`, multi-language, pooled, incremental sync), and ADR 0018's
to note `WorkspaceSymbol`/`DocumentSymbol` plus MCP tools implemented under `-tags lsp`. Both
remain `Proposed`, so both are staged `proposed` in `decisions.md`. See INGEST-CONFLICTS.md
INFO-6.

---

## Topic: Cross-reference graph shape (derived during synthesis, not from any one document)
- source: cross_refs across all 25 classifications in .planning/intel/classifications/run-rbCsBr/json
- content: 25 nodes, 106 in-set directed edges. Tarjan SCC finds two strongly connected components: one of 17 nodes (ADRs 0001-0015, 0017, 0022) and one of 4 nodes (ADRs 0019, 0020, 0021 and docs/plans/0019). 28 edge pairs are exactly symmetric. Removing every edge that originates inside an ADR's `## Related` section does not eliminate the components: a 14-node and a 2-node SCC survive on prose citations alone. The surviving cycles are mutual prose citations between peer documents (for example ADR 0001's Decision cites 0004 for the content-addressing seam, and ADR 0004's Consequences cites 0001 back for the same fact). No self-loops. See INGEST-CONFLICTS.md INFO-5 for how this was adjudicated.
